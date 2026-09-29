package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

func newSmallIngest(t *testing.T, attempts int) *IngestService {
	t.Helper()
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewIngestService(store, IngestOptions{
		QueueObjects: 2, GlobalBytes: 16, PerRecordingBytes: 8,
		MaxPayloadBytes: 8, Writers: 1, PersistAttempts: attempts, RetryBase: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.Close(ctx); err != nil {
			t.Errorf("close ingest service: %v", err)
		}
	})
	return service
}

func TestIngestByteBudgetsGrowUnderHintAndResumeAfterRelease(t *testing.T) {
	service := newSmallIngest(t, 1)
	first, err := service.ReadPayload(context.Background(), "recording-a", bytes.NewReader([]byte("12345678")), 8, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := service.Snapshot(); got.BufferUsedBytes != 8 || got.ReservedBytes != 8 {
		t.Fatalf("underestimated hint did not grow its reservation: %#v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = service.ReadPayload(ctx, "recording-a", bytes.NewReader([]byte("x")), 8, -1, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same-recording full budget error = %v", err)
	}
	first.Release()
	second, err := service.ReadPayload(context.Background(), "recording-a", bytes.NewReader([]byte("x")), 8, -1, 1)
	if err != nil {
		t.Fatalf("admission did not resume after release: %v", err)
	}
	second.Release()
	if got := service.Snapshot(); got.BufferUsedBytes != 0 || got.ReservedBytes != 0 {
		t.Fatalf("budget leaked after release: %#v", got)
	}
}

func TestIngestGrowthAccountsForOldAndNewBackingArrays(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const budget = 384 << 10 // 128 KiB + 256 KiB maximum growth peak.
	service, err := NewIngestService(store, IngestOptions{
		QueueObjects: 2, GlobalBytes: budget, PerRecordingBytes: budget,
		MaxPayloadBytes: 256 << 10, Writers: 1, PersistAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.Close(ctx); err != nil {
			t.Errorf("close ingest service: %v", err)
		}
	})
	started, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var transientReserved int64
	service.reallocationHook = func(oldCapacity, newCapacity int64) {
		if oldCapacity == ingestAllocationChunk {
			snapshot := service.Snapshot()
			transientReserved = snapshot.ReservedBytes
			close(started)
			<-release
		}
	}
	reader := io.MultiReader(bytes.NewReader(bytes.Repeat([]byte("x"), int(ingestAllocationChunk))), bytes.NewReader([]byte("y")))
	readDone := make(chan resultPayload, 1)
	go func() {
		payload, readErr := service.ReadPayload(context.Background(), "growth", reader, 256<<10, -1, 0)
		readDone <- resultPayload{payload: payload, err: readErr}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("growth boundary hook was not reached")
	}
	if want := ingestAllocationChunk + 2*ingestAllocationChunk; transientReserved != want {
		t.Fatalf("transient old+new reservation=%d want=%d", transientReserved, want)
	}
	if transientReserved > budget || transientReserved > service.options.PerRecordingBytes {
		t.Fatalf("transient reservation exceeded a configured bound: %d", transientReserved)
	}
	close(release)
	got := <-readDone
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.payload == nil || len(got.payload.Bytes()) != int(ingestAllocationChunk)+1 {
		t.Fatalf("grown payload length = %v", got.payload)
	}
	if snapshot := service.Snapshot(); snapshot.ReservedBytes != 2*ingestAllocationChunk || snapshot.BufferUsedBytes != ingestAllocationChunk+1 {
		t.Fatalf("post-growth accounting = %#v", snapshot)
	}
	got.payload.Release()
	if snapshot := service.Snapshot(); snapshot.ReservedBytes != 0 || snapshot.BufferUsedBytes != 0 {
		t.Fatalf("released growth accounting leaked: %#v", snapshot)
	}
}

func TestIngestOptionsRequireMaximumReallocationPeak(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewIngestService(store, IngestOptions{
		QueueObjects: 2, GlobalBytes: 256 << 10, PerRecordingBytes: 256 << 10,
		MaxPayloadBytes: 256 << 10, Writers: 1, PersistAttempts: 1,
	})
	if err == nil {
		t.Fatal("configuration below the old+new growth peak was accepted")
	}
}

func TestCapacityFromBlocksRejectsInvalidAndOverflowingStatfs(t *testing.T) {
	got, ok := capacityFromBlocks(10, 3, 2, 4096)
	if !ok || got.TotalBytes != 40960 || got.UsedBytes != 28672 || got.AvailableBytes != 8192 {
		t.Fatalf("valid capacity = %#v, valid=%v", got, ok)
	}
	for name, input := range map[string][4]uint64{
		"free exceeds total":                {2, 3, 1, 4096},
		"available exceeds free":            {10, 3, 4, 4096},
		"zero block size":                   {2, 1, 1, 0},
		"total multiplication overflow":     {^uint64(0), 0, 0, 2},
		"used multiplication overflow":      {^uint64(0) / 2, ^uint64(0)/2 - 1, 0, 4},
		"available multiplication overflow": {^uint64(0) / 2, 0, ^uint64(0) / 2, 4},
	} {
		t.Run(name, func(t *testing.T) {
			capacity, valid := capacityFromBlocks(input[0], input[1], input[2], input[3])
			if valid || capacity != (PoolCapacity{}) {
				t.Fatalf("invalid capacity = %#v, valid=%v", capacity, valid)
			}
		})
	}
}

func TestKnownSmallHintsAllowFourSameRecordingCaptures(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewIngestService(store, IngestOptions{
		QueueObjects: 4, GlobalBytes: DefaultIngestGlobalBytes,
		PerRecordingBytes: DefaultIngestPerRecordingBytes,
		MaxPayloadBytes:   DefaultMaxIngestPayloadBytes, Writers: 1,
		PersistAttempts: 1, RetryBase: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.Close(ctx); err != nil {
			t.Errorf("close ingest service: %v", err)
		}
	})

	start := make(chan struct{})
	type result struct {
		payload *IngestPayload
		err     error
	}
	results := make(chan result, 4)
	for i := 0; i < cap(results); i++ {
		go func() {
			<-start
			payload, readErr := service.ReadPayload(context.Background(), "same-recording", bytes.NewReader([]byte("data")), DefaultMaxIngestPayloadBytes, -1, 4)
			results <- result{payload: payload, err: readErr}
		}()
	}
	close(start)
	var payloads []*IngestPayload
	for i := 0; i < cap(results); i++ {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatalf("concurrent known-length capture failed: %v", got.err)
			}
			payloads = append(payloads, got.payload)
		case <-time.After(time.Second):
			t.Fatal("four known-length bodies did not finish concurrently")
		}
	}
	if snapshot := service.Snapshot(); snapshot.BufferUsedBytes != 16 {
		t.Fatalf("buffered bytes = %d, want 16", snapshot.BufferUsedBytes)
	}
	for _, payload := range payloads {
		payload.Release()
	}
}

func TestUnknownLengthReservesIncrementallyAsBytesArrive(t *testing.T) {
	service := newSmallIngest(t, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	readCount := 0
	reader := readerFunc(func(p []byte) (int, error) {
		readCount++
		if readCount == 1 {
			return copy(p, []byte("data")), nil
		}
		if readCount > 2 {
			return 0, io.EOF
		}
		close(started)
		<-release
		return 0, io.EOF
	})
	firstDone := make(chan resultPayload, 1)
	go func() {
		payload, err := service.ReadPayload(context.Background(), "recording-a", reader, 8, -1, 0)
		firstDone <- resultPayload{payload: payload, err: err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first unknown-length body did not start reading")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := service.ReadPayload(ctx, "recording-a", bytes.NewReader([]byte("x")), 8, -1, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second unknown body did not backpressure on full reservation: %v", err)
	}
	close(release)
	got := <-firstDone
	if got.err != nil {
		t.Fatal(got.err)
	}
	got.payload.Release()
}

type resultPayload struct {
	payload *IngestPayload
	err     error
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestIngestGlobalBudgetCancellationAndOversizeRelease(t *testing.T) {
	service := newSmallIngest(t, 1)
	first, err := service.ReadPayload(context.Background(), "recording-a", bytes.NewReader([]byte("12345678")), 8, -1, 8)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.ReadPayload(context.Background(), "recording-b", bytes.NewReader([]byte("abcdefgh")), 8, -1, 8)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = service.ReadPayload(ctx, "recording-c", bytes.NewReader([]byte("z")), 8, -1, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("global full budget error = %v", err)
	}
	first.Release()
	second.Release()
	if _, err = service.ReadPayload(context.Background(), "recording-c", bytes.NewReader([]byte("123456789")), 8, -1, 1); !errors.Is(err, ErrIngestTooLarge) {
		t.Fatalf("oversized source error = %v", err)
	}
	if got := service.Snapshot(); got.BufferUsedBytes != 0 || got.ReservedBytes != 0 {
		t.Fatalf("budget leaked after cancellation/error: %#v", got)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestIngestReadErrorReleasesReservation(t *testing.T) {
	service := newSmallIngest(t, 1)
	want := errors.New("read failed")
	if _, err := service.ReadPayload(context.Background(), "recording-a", failingReader{err: want}, 8, -1, 8); !errors.Is(err, want) {
		t.Fatalf("read error = %v", err)
	}
	if got := service.Snapshot(); got.BufferUsedBytes != 0 || got.ReservedBytes != 0 {
		t.Fatalf("reservation leaked after reader error: %#v", got)
	}
}

func TestSubmitCompletedPayloadIgnoresAcquisitionCancellationWhileQueueFull(t *testing.T) {
	service := newSmallIngest(t, 1)
	writerStarted, releaseWriter := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-releaseWriter:
		default:
			close(releaseWriter)
		}
	}()
	if err := service.SubmitCommit(context.Background(), "blocker", func() error {
		close(writerStarted)
		<-releaseWriter
		return nil
	}, func(error) {}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-writerStarted:
	case <-time.After(time.Second):
		t.Fatal("blocking commit did not start")
	}
	for i := 0; i < 2; i++ {
		if err := service.SubmitCommit(context.Background(), "queued", func() error { return nil }, func(error) {}); err != nil {
			t.Fatalf("fill queue %d: %v", i, err)
		}
	}
	if got := service.Snapshot().QueueObjects; got != 2 {
		t.Fatalf("queue objects=%d want=2", got)
	}
	payload, err := service.ReadPayload(context.Background(), "recording", bytes.NewReader([]byte("complete")), 8, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	completed := make(chan error, 1)
	var persistCalls atomic.Int32
	submitDone := make(chan error, 1)
	go func() {
		close(started)
		submitDone <- service.Submit(ctx, payload, func(data []byte) (PayloadResult, error) {
			persistCalls.Add(1)
			if !bytes.Equal(data, []byte("complete")) {
				return PayloadResult{}, errors.New("payload changed")
			}
			return PayloadResult{Size: int64(len(data))}, nil
		}, func(_ PayloadResult, persistErr error) { completed <- persistErr })
	}()
	<-started
	select {
	case err := <-submitDone:
		t.Fatalf("full queue unexpectedly accepted payload: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-submitDone:
		t.Fatalf("acquisition cancellation discarded complete payload: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := service.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		closeCancel()
		t.Fatalf("Close while a pre-existing payload waited returned %v, want deadline", err)
	}
	closeCancel()
	if err := service.SubmitCommit(context.Background(), "new-admission", func() error { return nil }, func(error) {}); !errors.Is(err, ErrIngestClosed) {
		t.Fatalf("post-close submission = %v, want ErrIngestClosed", err)
	}
	close(releaseWriter)
	select {
	case err := <-submitDone:
		if err != nil {
			t.Fatalf("submit after writer capacity returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("completed payload did not enter queue after capacity returned")
	}
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("completed payload did not persist")
	}
	if persistCalls.Load() != 1 {
		t.Fatalf("completed payload persisted %d times, want exactly once", persistCalls.Load())
	}
	select {
	case <-service.Done():
	case <-time.After(time.Second):
		t.Fatal("close coordinator did not join after the accepted payload drained")
	}
}

func TestIngestRetriesSameVolatileBytesAndReleasesOnce(t *testing.T) {
	service := newSmallIngest(t, 3)
	payload, err := service.ReadPayload(context.Background(), "recording-a", bytes.NewReader([]byte("source")), 8, -1, 6)
	if err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	completed := make(chan error, 1)
	err = service.Submit(context.Background(), payload, func(data []byte) (PayloadResult, error) {
		if !bytes.Equal(data, []byte("source")) {
			t.Fatalf("retry bytes = %q", data)
		}
		if attempts.Add(1) == 1 {
			return PayloadResult{}, errors.New("transient storage error")
		}
		return PayloadResult{Size: int64(len(data)), SHA256: "digest"}, nil
	}, func(_ PayloadResult, persistErr error) { completed <- persistErr })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-completed:
	case <-time.After(time.Second):
		t.Fatal("persistence callback did not complete")
	}
	if err != nil || attempts.Load() != 2 {
		t.Fatalf("attempts=%d err=%v", attempts.Load(), err)
	}
	if got := service.Snapshot(); got.BufferUsedBytes != 0 || got.ReservedBytes != 0 || got.StorageErrorsTotal != 0 {
		t.Fatalf("successful retry accounting = %#v", got)
	}
}

func TestIngestPermanentStorageFailureReleasesVolatileBytes(t *testing.T) {
	service := newSmallIngest(t, 2)
	payload, err := service.ReadPayload(context.Background(), "recording-a", bytes.NewReader([]byte("source")), 8, -1, 6)
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	err = service.Submit(context.Background(), payload, func([]byte) (PayloadResult, error) {
		return PayloadResult{}, errors.New("storage unavailable")
	}, func(_ PayloadResult, persistErr error) { completed <- persistErr })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-completed:
	case <-time.After(time.Second):
		t.Fatal("failed persistence callback did not complete")
	}
	if err == nil || err.Error() != "canonical storage commit failed after bounded retries" {
		t.Fatalf("public persistence error = %v", err)
	}
	if got := service.Snapshot(); got.BufferUsedBytes != 0 || got.ReservedBytes != 0 || got.StorageErrorsTotal != 1 {
		t.Fatalf("failed persistence accounting = %#v", got)
	}
}

func TestIngestCloseDeadlineCanBeRepeatedAfterWriterUnblocks(t *testing.T) {
	service := newSmallIngest(t, 1)
	payload, err := service.ReadPayload(context.Background(), "recording-a", bytes.NewReader([]byte("data")), 8, -1, 4)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	if err = service.Submit(context.Background(), payload, func(data []byte) (PayloadResult, error) {
		close(started)
		<-release
		return PayloadResult{Size: int64(len(data))}, nil
	}, func(PayloadResult, error) {}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err = service.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first close error = %v", err)
	}
	close(release)
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err = service.Close(ctx2); err != nil {
		t.Fatalf("repeated close error = %v", err)
	}
	if got := service.Snapshot(); got.BufferUsedBytes != 0 || got.ReservedBytes != 0 {
		t.Fatalf("closed service retained volatile bytes: %#v", got)
	}
}

var _ io.Reader = failingReader{}
