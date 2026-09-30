package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func testCoordinator(t *testing.T, global, perRecording int64, queue int) *Coordinator {
	t.Helper()
	c, err := New(Limits{
		GlobalBufferBytes:       global,
		PerRecordingBufferBytes: perRecording,
		QueueObjects:            queue,
		WriterConcurrency:       1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testTelemetryCoordinator(t *testing.T, interval, retention time.Duration) *Coordinator {
	t.Helper()
	coordinator, err := newCoordinator(Limits{
		GlobalBufferBytes: 2 << 30, PerRecordingBufferBytes: 1536 << 20,
		QueueObjects: 128, WriterConcurrency: 1,
	}, interval, retention, false)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func TestTelemetryAggregatesOwnerDeltasIdempotentlyAcrossGenerations(t *testing.T) {
	coordinator := testTelemetryCoordinator(t, 5*time.Second, 24*time.Hour)
	start := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	coordinator.lastTelemetryAt = start
	for _, report := range []struct {
		owner  string
		read   uint64
		write  uint64
		errors uint64
	}{{"engine-a-instance-1", 100, 500, 1}, {"engine-b-instance-1", 300, 1000, 2}} {
		if err := coordinator.ReportTelemetry(report.owner, report.read, report.write, report.errors); err != nil {
			t.Fatal(err)
		}
	}
	// Repeating the exact cumulative reports must add no bytes or errors.
	if err := coordinator.ReportTelemetry("engine-a-instance-1", 100, 500, 1); err != nil {
		t.Fatal(err)
	}
	coordinator.SampleTelemetryAt(start.Add(5 * time.Second))
	snapshot := coordinator.TelemetrySnapshot()
	if snapshot.Throughput.ReadBytesTotal != 400 || snapshot.Throughput.WriteBytesTotal != 1500 || snapshot.ErrorsTotal != 3 {
		t.Fatalf("aggregate totals = %+v errors=%d", snapshot.Throughput, snapshot.ErrorsTotal)
	}
	if snapshot.Throughput.ReadBytesPerSecond != 80 || snapshot.Throughput.WriteBytesPerSecond != 300 {
		t.Fatalf("aggregate rates = %+v", snapshot.Throughput)
	}
	if len(snapshot.Samples) != 1 || snapshot.Samples[0].At != start.Add(5*time.Second) {
		t.Fatalf("aggregate sample projection = %+v", snapshot.Samples)
	}
	if snapshot.Samples[0].BufferUsedBytes != 0 || snapshot.Samples[0].PersistQueueBytes != 0 {
		t.Fatalf("telemetry sample leaked non-aggregate queue data: %+v", snapshot.Samples[0])
	}
}

func TestTelemetryAggregatesQueueGaugesAndHostIngestSnapshot(t *testing.T) {
	coordinator := testTelemetryCoordinator(t, 5*time.Second, time.Hour)
	ctx := context.Background()
	for _, reservation := range []struct {
		owner string
		rid   string
		bytes int64
	}{{"engine-a-instance", "0123456789abcdef0123456789abcdef", 4096}, {"engine-b-instance", "abcdef0123456789abcdef0123456789", 8192}} {
		if err := coordinator.SetReservation(ctx, reservation.owner, reservation.rid, "reservation-"+reservation.owner, reservation.bytes); err != nil {
			t.Fatal(err)
		}
	}
	if err := coordinator.AcquireQueue(ctx, "engine-a-instance", "queue-a"); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.AcquireQueue(ctx, "engine-b-instance", "queue-b"); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ReportProcessTelemetry("engine-a-instance", 0, 0, 0, 3000, 4); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ReportProcessTelemetry("engine-b-instance", 0, 0, 0, 7000, 9); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	coordinator.lastTelemetryAt = start
	coordinator.SampleTelemetryAt(start.Add(5 * time.Second))
	snapshot := coordinator.Snapshot()
	if snapshot.UsedBytes != 12<<10 || snapshot.QueueObjects != 2 || snapshot.QueueBytes != 10_000 || snapshot.OldestAgeSeconds != 9 || snapshot.ActiveWriters != 0 {
		t.Fatalf("aggregate runtime resource snapshot = %+v", snapshot)
	}
	telemetry := coordinator.TelemetrySnapshot()
	if len(telemetry.Samples) != 1 || telemetry.Samples[0].BufferUsedBytes != 12<<10 || telemetry.Samples[0].PersistQueueBytes != 10_000 {
		t.Fatalf("host aggregate sample omitted ingest gauges: %+v", telemetry.Samples)
	}
	if err := coordinator.ReleaseOwner("engine-a-instance"); err != nil {
		t.Fatal(err)
	}
	after := coordinator.Snapshot()
	if after.QueueBytes != 7000 || after.QueueObjects != 1 || after.UsedBytes != 8192 || after.OldestAgeSeconds != 9 {
		t.Fatalf("owner cleanup did not remove only its live gauges: %+v", after)
	}
	if retained := coordinator.TelemetrySnapshot(); retained.Throughput.ReadBytesTotal != telemetry.Throughput.ReadBytesTotal || len(retained.Samples) != len(telemetry.Samples) {
		t.Fatalf("owner cleanup lost cumulative telemetry: before=%+v after=%+v", telemetry, retained)
	}
}

func TestProcessTelemetryRejectsInvalidQueueGauge(t *testing.T) {
	coordinator := testTelemetryCoordinator(t, time.Second, time.Minute)
	for _, test := range []struct {
		bytes int64
		age   float64
	}{{-1, 0}, {MaxGlobalBufferBytes + 1, 0}, {0, math.NaN()}, {0, math.Inf(1)}, {0, -1}} {
		if err := coordinator.ReportProcessTelemetry("engine-a", 1, 2, 3, test.bytes, test.age); !errors.Is(err, ErrInvalidTelemetryGauge) {
			t.Errorf("invalid gauge bytes=%d age=%v error=%v", test.bytes, test.age, err)
		}
	}
}

func TestTelemetrySampleRingIsBoundedByRetentionAndOwnerCleanupKeepsHistory(t *testing.T) {
	coordinator := testTelemetryCoordinator(t, 5*time.Second, 10*time.Second)
	start := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	coordinator.lastTelemetryAt = start
	for i := uint64(1); i <= 4; i++ {
		if err := coordinator.ReportTelemetry("engine-old-instance", i, i*10, i); err != nil {
			t.Fatal(err)
		}
		coordinator.SampleTelemetryAt(start.Add(time.Duration(i*5) * time.Second))
	}
	before := coordinator.TelemetrySnapshot()
	if len(before.Samples) != 2 {
		t.Fatalf("retention ring kept %d samples, want 2: %+v", len(before.Samples), before.Samples)
	}
	if err := coordinator.ReleaseOwner("engine-old-instance"); err != nil {
		t.Fatal(err)
	}
	after := coordinator.TelemetrySnapshot()
	if after.Throughput.ReadBytesTotal != before.Throughput.ReadBytesTotal || after.ErrorsTotal != before.ErrorsTotal || len(after.Samples) != len(before.Samples) {
		t.Fatalf("owner cleanup erased aggregate history: before=%+v after=%+v", before, after)
	}
	if err := coordinator.ReportTelemetry("engine-old-instance", 1, 1, 0); err != nil {
		t.Fatalf("owner baseline was not released: %v", err)
	}
}

func TestTelemetryRejectsRegressingTotalsWithoutChangingProjection(t *testing.T) {
	coordinator := testTelemetryCoordinator(t, 5*time.Second, 10*time.Minute)
	if err := coordinator.ReportTelemetry("engine-a", 100, 200, 3); err != nil {
		t.Fatal(err)
	}
	before := coordinator.TelemetrySnapshot()
	if err := coordinator.ReportTelemetry("engine-a", 99, 201, 4); !errors.Is(err, ErrTelemetryRegression) {
		t.Fatalf("regressing report error = %v", err)
	}
	after := coordinator.TelemetrySnapshot()
	if after.Throughput.ReadBytesTotal != before.Throughput.ReadBytesTotal || after.Throughput.WriteBytesTotal != before.Throughput.WriteBytesTotal || after.ErrorsTotal != before.ErrorsTotal {
		t.Fatalf("rejected report changed totals: before=%+v after=%+v", before, after)
	}
}

func TestTelemetryRejectsAggregateOverflowAndNeverProjectsOwnerIdentity(t *testing.T) {
	coordinator := testTelemetryCoordinator(t, 5*time.Second, 24*time.Hour)
	if err := coordinator.ReportTelemetry("engine-a-secret-instance", math.MaxUint64, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ReportTelemetry("engine-b-instance", 1, 0, 0); !errors.Is(err, ErrTelemetryOverflow) {
		t.Fatalf("aggregate overflow error = %v, want ErrTelemetryOverflow", err)
	}
	if snapshot := coordinator.TelemetrySnapshot(); snapshot.Throughput.ReadBytesTotal != math.MaxUint64 {
		t.Fatalf("overflowing report changed aggregate total: %+v", snapshot)
	}
	encoded, err := json.Marshal(coordinator.TelemetrySnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "engine-a-secret-instance") || strings.Contains(string(encoded), "recording") || strings.Contains(string(encoded), "/") {
		t.Fatalf("aggregate telemetry leaked owner, recording identity, or path: %s", encoded)
	}
}

func TestTelemetryCeilingUsesActualCadenceAndDirectionalMinimum(t *testing.T) {
	testCases := []struct {
		name     string
		interval time.Duration
		count    int
		want     string
	}{
		{name: "default five minutes", interval: 5 * time.Second, count: 60, want: "observed"},
		{name: "short one second history", interval: time.Second, count: 60, want: "unknown"},
		{name: "fast cadence after five minutes", interval: time.Second, count: 300, want: "observed"},
		{name: "one hour cadence within one day retention", interval: time.Hour, count: 24, want: "observed"},
	}
	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			samples := make([]TelemetrySample, test.count)
			for i := range samples {
				samples[i] = TelemetrySample{
					At: time.Unix(int64(i+1), 0), WriteBytesPerSecond: 100,
					ObservedForNanos: int64(test.interval),
				}
			}
			if got := estimateTelemetryCeiling(samples).Source; got != test.want {
				t.Fatalf("ceiling source=%q want %q", got, test.want)
			}
		})
	}
	shortDirectional := make([]TelemetrySample, 60)
	for i := range shortDirectional {
		shortDirectional[i] = TelemetrySample{WriteBytesPerSecond: 100, ObservedForNanos: int64(5 * time.Second)}
	}
	if got := estimateTelemetryCeiling(shortDirectional); got.Source != "observed" || got.WriteBytesPerSecond != 100 || got.ReadBytesPerSecond != 0 {
		t.Fatalf("directional ceiling did not preserve p95 minimum semantics: %+v", got)
	}
}

func TestNewValidatesExistingIngestBounds(t *testing.T) {
	valid := Limits{GlobalBufferBytes: 2 << 30, PerRecordingBufferBytes: 1536 << 20, QueueObjects: 128, WriterConcurrency: 1}
	if _, err := New(valid); err != nil {
		t.Fatalf("maximum current limits rejected: %v", err)
	}
	invalid := []Limits{
		{},
		{GlobalBufferBytes: 10, PerRecordingBufferBytes: 11, QueueObjects: 1, WriterConcurrency: 1},
		{GlobalBufferBytes: (2 << 30) + 1, PerRecordingBufferBytes: 1, QueueObjects: 1, WriterConcurrency: 1},
		{GlobalBufferBytes: 10, PerRecordingBufferBytes: (1536 << 20) + 1, QueueObjects: 1, WriterConcurrency: 1},
		{GlobalBufferBytes: 10, PerRecordingBufferBytes: 10, QueueObjects: 0, WriterConcurrency: 1},
		{GlobalBufferBytes: 10, PerRecordingBufferBytes: 10, QueueObjects: 129, WriterConcurrency: 1},
		{GlobalBufferBytes: 10, PerRecordingBufferBytes: 10, QueueObjects: 1, WriterConcurrency: 0},
		{GlobalBufferBytes: 10, PerRecordingBufferBytes: 10, QueueObjects: 1, WriterConcurrency: 2},
	}
	for i, limits := range invalid {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			if _, err := New(limits); !errors.Is(err, ErrInvalidLimits) {
				t.Fatalf("New(%+v) error = %v, want ErrInvalidLimits", limits, err)
			}
		})
	}
}

func TestSetReservationEnforcesGlobalAndPerRecordingLimitsAcrossOwners(t *testing.T) {
	c := testCoordinator(t, 10, 8, 2)
	ctx := context.Background()
	if err := c.SetReservation(ctx, "owner-a", "recording-a", "payload-a", 4); err != nil {
		t.Fatal(err)
	}
	blocked, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := c.SetReservation(blocked, "owner-b", "recording-a", "payload-b", 5); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("per-recording overcommit error = %v, want deadline", err)
	}
	blocked, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := c.SetReservation(blocked, "owner-b", "recording-b", "payload-b", 7); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("global overcommit error = %v, want deadline", err)
	}
	if got := c.Snapshot(); got.UsedBytes != 4 || got.Limits.GlobalBufferBytes != 10 {
		t.Fatalf("blocked reservations changed usage: %+v", got)
	}
}

func TestReservationResizeIsIdempotentAndChecksIdentity(t *testing.T) {
	c := testCoordinator(t, 10, 8, 2)
	ctx := context.Background()
	if err := c.SetReservation(ctx, "owner-a", "recording-a", "payload", 4); err != nil {
		t.Fatal(err)
	}
	if err := c.SetReservation(ctx, "owner-a", "recording-a", "payload", 4); err != nil {
		t.Fatalf("duplicate set was not idempotent: %v", err)
	}
	if got := c.Snapshot().UsedBytes; got != 4 {
		t.Fatalf("duplicate set double counted bytes: %d", got)
	}
	if err := c.SetReservation(ctx, "owner-b", "recording-a", "payload", 4); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("owner mismatch error = %v", err)
	}
	if err := c.SetReservation(ctx, "owner-a", "recording-b", "payload", 4); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("recording mismatch error = %v", err)
	}
	if err := c.SetReservation(ctx, "owner-a", "recording-a", "payload", 6); err != nil {
		t.Fatalf("increase failed: %v", err)
	}
	if got := c.Snapshot().UsedBytes; got != 6 {
		t.Fatalf("increase accounted %d bytes, want 6", got)
	}
	if err := c.SetReservation(ctx, "owner-a", "recording-a", "payload", 2); err != nil {
		t.Fatalf("decrease failed: %v", err)
	}
	if got := c.Snapshot().UsedBytes; got != 2 {
		t.Fatalf("decrease accounted %d bytes, want 2", got)
	}
	if err := c.SetReservation(ctx, "owner-a", "recording-a", "empty", 0); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("zero reservation error = %v", err)
	}
	if err := c.ReleaseReservation("owner-b", "recording-a", "payload"); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("release owner mismatch error = %v", err)
	}
	if err := c.ReleaseReservation("owner-a", "recording-b", "payload"); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("release recording mismatch error = %v", err)
	}
	if err := c.ReleaseReservation("owner-a", "recording-a", "payload"); err != nil {
		t.Fatal(err)
	}
	if err := c.ReleaseReservation("owner-a", "recording-a", "payload"); err != nil {
		t.Fatalf("duplicate release not idempotent: %v", err)
	}
	if got := c.Snapshot().UsedBytes; got != 0 {
		t.Fatalf("release leaked %d bytes", got)
	}
}

func TestReservationBackpressureCancellationAndReleaseWake(t *testing.T) {
	c := testCoordinator(t, 5, 5, 1)
	if err := c.SetReservation(context.Background(), "owner-a", "recording-a", "payload-a", 5); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.SetReservation(ctx, "owner-b", "recording-b", "payload-b", 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("full buffer should honor cancellation, got %v", err)
	}

	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		result <- c.SetReservation(context.Background(), "owner-b", "recording-b", "payload-b", 5)
	}()
	<-started
	select {
	case err := <-result:
		t.Fatalf("reservation passed backpressure before capacity was released: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := c.ReleaseReservation("owner-a", "recording-a", "payload-a"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("waiter did not resume after release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reservation waiter was not woken after release")
	}
}

func TestQueueCapacityIdempotencyAndReleaseWake(t *testing.T) {
	c := testCoordinator(t, 10, 10, 1)
	ctx := context.Background()
	if err := c.AcquireQueue(ctx, "owner-a", "job-a"); err != nil {
		t.Fatal(err)
	}
	if err := c.AcquireQueue(ctx, "owner-a", "job-a"); err != nil {
		t.Fatalf("duplicate queue acquire failed: %v", err)
	}
	if got := c.Snapshot().QueueObjects; got != 1 {
		t.Fatalf("duplicate queue lease counted %d objects", got)
	}
	result := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		result <- c.AcquireQueue(context.Background(), "owner-b", "job-b")
	}()
	<-started
	select {
	case err := <-result:
		t.Fatalf("queue passed backpressure before capacity was released: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := c.ReleaseQueue("owner-a", "job-a"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("queue waiter did not resume: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queue waiter was not woken after release")
	}
	if got := c.Snapshot().QueueObjects; got != 1 {
		t.Fatalf("queue usage=%d want=1", got)
	}
}

func TestWriterPermitIsGlobalAndOwnerCleanupReclaimsLeases(t *testing.T) {
	c := testCoordinator(t, 10, 10, 2)
	ctx := context.Background()
	if err := c.AcquireWriter(ctx, "owner-a", "writer-a"); err != nil {
		t.Fatal(err)
	}
	if err := c.AcquireWriter(ctx, "owner-a", "writer-a"); err != nil {
		t.Fatalf("duplicate writer acquire failed: %v", err)
	}
	if err := c.SetReservation(ctx, "owner-a", "recording-a", "payload", 3); err != nil {
		t.Fatal(err)
	}
	if err := c.AcquireQueue(ctx, "owner-a", "job-a"); err != nil {
		t.Fatal(err)
	}
	blockedCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.AcquireWriter(blockedCtx, "owner-b", "writer-b"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second writer should block, got %v", err)
	}
	if err := c.ReleaseOwner("owner-a"); err != nil {
		t.Fatal(err)
	}
	if err := c.ReleaseOwner("owner-a"); err != nil {
		t.Fatalf("owner release should be idempotent: %v", err)
	}
	if got := c.Snapshot(); got.UsedBytes != 0 || got.QueueObjects != 0 || got.ActiveWriters != 0 {
		t.Fatalf("owner cleanup left leases: %+v", got)
	}
	if err := c.AcquireWriter(ctx, "owner-b", "writer-b"); err != nil {
		t.Fatalf("writer unavailable after owner cleanup: %v", err)
	}
}

func TestQueueAndWriterWaitersHonorCancellation(t *testing.T) {
	c := testCoordinator(t, 10, 10, 1)
	if err := c.AcquireQueue(context.Background(), "owner-a", "job-a"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.AcquireQueue(ctx, "owner-b", "job-b"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("full queue should honor cancellation, got %v", err)
	}
	if err := c.ReleaseQueue("owner-a", "job-a"); err != nil {
		t.Fatal(err)
	}
	if err := c.AcquireWriter(context.Background(), "owner-a", "writer-a"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.AcquireWriter(ctx, "owner-b", "writer-b"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("busy writer should honor cancellation, got %v", err)
	}
}

func TestOwnerCleanupWakesWaiters(t *testing.T) {
	c := testCoordinator(t, 5, 5, 1)
	if err := c.SetReservation(context.Background(), "owner-a", "recording-a", "payload-a", 5); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- c.SetReservation(context.Background(), "owner-b", "recording-b", "payload-b", 5) }()
	if err := c.ReleaseOwner("owner-a"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("waiter failed after owner cleanup: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("owner cleanup did not wake reservation waiter")
	}
}

func TestInvalidIdentitiesAndSnapshotDoesNotExposeIDs(t *testing.T) {
	c := testCoordinator(t, 10, 10, 1)
	invalid := []string{"", ".", "../recording", "https://host", "contains space", "x\x00y", "x" + string(make([]byte, 129))}
	for _, id := range invalid {
		if err := c.AcquireQueue(context.Background(), id, "job"); !errors.Is(err, ErrInvalidIdentity) {
			t.Errorf("AcquireQueue(%q) error = %v, want ErrInvalidIdentity", id, err)
		}
	}
	if err := c.AcquireQueue(nil, "owner", "job"); err == nil {
		t.Fatal("nil context accepted")
	}
	if err := c.AcquireQueue(context.Background(), "owner", "job"); err != nil {
		t.Fatal(err)
	}
	snapshot := c.Snapshot()
	if snapshot.QueueObjects != 1 || snapshot.ActiveWriters != 0 || snapshot.UsedBytes != 0 {
		t.Fatalf("unexpected aggregate snapshot: %+v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, privateIdentity := range []string{"owner-a", "job-a", "recording-a", "reservation-a"} {
		if strings.Contains(string(encoded), privateIdentity) {
			t.Fatalf("aggregate snapshot leaked %q: %s", privateIdentity, encoded)
		}
	}
}

func TestConcurrentReservationsNeverExceedGlobalBudget(t *testing.T) {
	const workers = 80
	c := testCoordinator(t, workers/2, workers/2, 4)
	start := make(chan struct{})
	release := make(chan struct{})
	type admittedLease struct{ owner, recording, reservation string }
	admitted := make(chan admittedLease, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			owner := fmt.Sprintf("owner-%d", i)
			recording := fmt.Sprintf("recording-%d", i)
			reservation := fmt.Sprintf("payload-%d", i)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := c.SetReservation(ctx, owner, recording, reservation, 1); err != nil {
				t.Errorf("SetReservation: %v", err)
				return
			}
			admitted <- admittedLease{owner, recording, reservation}
			<-release
			if err := c.ReleaseReservation(owner, recording, reservation); err != nil {
				t.Errorf("ReleaseReservation: %v", err)
			}
		}(i)
	}
	close(start)
	leases := make([]admittedLease, 0, workers/2)
	for len(leases) < workers/2 {
		select {
		case lease := <-admitted:
			leases = append(leases, lease)
		case <-time.After(5 * time.Second):
			close(release)
			wg.Wait()
			t.Fatal("coordinator did not admit available reservations")
		}
	}
	if got := c.Snapshot(); got.UsedBytes != workers/2 {
		t.Fatalf("concurrent usage = %+v, want %d", got, workers/2)
	}
	close(release)
	wg.Wait()
	if got := c.Snapshot(); got.UsedBytes != 0 {
		t.Fatalf("concurrent release leaked reservations: %+v", got)
	}
}
