package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestObservedCeilingRequiresSustainedRecorderTraffic(t *testing.T) {
	samples := make([]PoolSample, 59)
	for i := range samples {
		samples[i] = PoolSample{ReadBytesPerSecond: 1_000_000, WriteBytesPerSecond: 2_000_000}
	}
	if got := estimateCeiling(samples); got.Source != "unknown" {
		t.Fatalf("ceiling with insufficient samples = %#v", got)
	}
	samples = append(samples, PoolSample{ReadBytesPerSecond: 1_000_000, WriteBytesPerSecond: 2_000_000})
	if got := estimateCeiling(samples); got.Source != "observed" || got.ReadBytesPerSecond != 1_000_000 || got.WriteBytesPerSecond != 2_000_000 {
		t.Fatalf("sustained ceiling = %#v", got)
	}
}

func TestPoolMetricsExposeRecorderHealthAndNoPhysicalPath(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	pool := store.PoolMetrics()
	if pool.ID != PoolIDLocalPrimary || pool.Kind != "local" || pool.Health != "healthy" {
		t.Fatalf("local pool identity/health = %#v", pool)
	}
	if pool.DisplayName != "기본 보관 저장소" {
		t.Fatalf("display name = %q", pool.DisplayName)
	}
	backend := store.StorageBackend.(*LocalFilesystemBackend)
	backend.telemetry.recordError()
	if got := store.PoolMetrics().Health; got != "degraded" {
		t.Fatalf("health after I/O error = %q", got)
	}
	backend.telemetry.recordWrite(4, time.Millisecond)
	if got := store.PoolMetrics().Health; got != "healthy" {
		t.Fatalf("health did not recover after successful durable write: %q", got)
	}
	encoded, err := json.Marshal(store.PoolMetrics())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(root)) || strings.Contains(string(encoded), "archive_root") {
		t.Fatalf("pool metrics exposed physical path: %s", encoded)
	}
}

func TestTelemetrySamplesAreBoundedAndCarryQueueState(t *testing.T) {
	telemetry := newTelemetry()
	start := time.Now().UTC()
	state := IngestSnapshot{BufferUsedBytes: 5, QueueBytes: 7}
	telemetry.sample(start, state)
	telemetry.recordWrite(500, time.Second)
	throughput, _, samples := telemetry.sample(start.Add(5*time.Second), state)
	if throughput.WriteBytesPerSecond != 100 || len(samples) != 1 || samples[0].BufferUsedBytes != 5 || samples[0].PersistQueueBytes != 7 {
		t.Fatalf("throughput/sample = %#v %#v", throughput, samples)
	}
	telemetry.mu.Lock()
	telemetry.samples = make([]PoolSample, maxPoolSamples+1)
	for i := range telemetry.samples {
		telemetry.samples[i].At = start.Add(time.Duration(i) * poolSamplePeriod)
	}
	telemetry.mu.Unlock()
	_, _, samples = telemetry.sample(start.Add(time.Duration(maxPoolSamples+2)*poolSamplePeriod), state)
	if len(samples) != maxPoolSamples {
		t.Fatalf("sample history length=%d want=%d", len(samples), maxPoolSamples)
	}
}

func TestTelemetryUsesConfiguredCadenceAndRetention(t *testing.T) {
	telemetry := newTelemetryWithRetention(time.Second, 2*time.Second)
	start := time.Now().UTC()
	telemetry.sample(start, IngestSnapshot{})
	telemetry.recordWrite(100, time.Millisecond)
	_, _, samples := telemetry.sample(start.Add(500*time.Millisecond), IngestSnapshot{})
	if len(samples) != 0 {
		t.Fatalf("sample was recorded before configured interval: %d", len(samples))
	}
	for _, offset := range []time.Duration{time.Second, 2 * time.Second, 3 * time.Second} {
		telemetry.sample(start.Add(offset), IngestSnapshot{})
	}
	_, _, samples = telemetry.sample(start.Add(3*time.Second), IngestSnapshot{})
	if len(samples) != 2 {
		t.Fatalf("retained samples = %d, want bounded configured window of 2", len(samples))
	}
	for _, sample := range samples {
		if !sample.At.After(start.Add(time.Second)) {
			t.Fatalf("sample outside configured retention remained: %s", sample.At)
		}
	}
	if telemetry.maxSamples != 2 {
		t.Fatalf("max samples = %d, want 2", telemetry.maxSamples)
	}
}

func TestDirectIngestConstructorConfiguresTelemetry(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultIngestOptions()
	options.SampleInterval = 2 * time.Second
	options.MetricsRetention = 10 * time.Second
	service, err := NewIngestService(store, options)
	if err != nil {
		t.Fatal(err)
	}
	backend := store.StorageBackend.(*LocalFilesystemBackend)
	backend.telemetry.mu.Lock()
	interval, retention, maxSamples := backend.telemetry.sampleInterval, backend.telemetry.retention, backend.telemetry.maxSamples
	backend.telemetry.mu.Unlock()
	if interval != options.SampleInterval || retention != options.MetricsRetention || maxSamples != 5 {
		t.Fatalf("configured telemetry = interval %s retention %s max %d", interval, retention, maxSamples)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestIngestSamplesWithoutPoolMetricsRequests(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewIngestService(store, DefaultIngestOptions())
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

	ticks := make(chan time.Time, 2)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		service.runSampler(ticks, stop)
		close(done)
	}()
	start := time.Now().UTC()
	ticks <- start
	baselineDeadline := time.After(time.Second)
	for {
		telemetry := store.StorageBackend.(*LocalFilesystemBackend).telemetry
		telemetry.mu.Lock()
		baselineReady := telemetry.lastAt.Equal(start)
		telemetry.mu.Unlock()
		if baselineReady {
			break
		}
		select {
		case <-baselineDeadline:
			t.Fatal("service sampler did not establish baseline")
		case <-time.After(time.Millisecond):
		}
	}
	store.StorageBackend.(*LocalFilesystemBackend).telemetry.recordWrite(500, time.Second)
	ticks <- start.Add(poolSamplePeriod)

	deadline := time.After(time.Second)
	for {
		telemetry := store.StorageBackend.(*LocalFilesystemBackend).telemetry
		telemetry.mu.Lock()
		samples := append([]PoolSample(nil), telemetry.samples...)
		telemetry.mu.Unlock()
		if len(samples) >= 1 {
			if samples[0].WriteBytesPerSecond != 100 {
				t.Fatalf("service sample write rate = %d", samples[0].WriteBytesPerSecond)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("service sampling did not advance without a metrics request")
		case <-time.After(time.Millisecond):
		}
	}
	close(stop)
	<-done
}

func TestReadLatencyExcludesConsumerThinkTime(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "0123456789abcdef0123456789abcdef"
	if err := store.NewRecordingDir(id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SavePayload(id, "tracks/main/latency.ts", bytes.NewReader([]byte("payload")), 32); err != nil {
		t.Fatal(err)
	}
	reader, err := store.OpenPayloadReader(id, "tracks/main/latency.ts")
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1)
	if n, err := reader.Read(buffer); err != nil || n != 1 {
		t.Fatal(err)
	}
	backend := store.StorageBackend.(*LocalFilesystemBackend)
	backend.telemetry.mu.Lock()
	readOps, readNanos := backend.telemetry.readOps, backend.telemetry.readNanos
	backend.telemetry.mu.Unlock()
	if readOps != 1 || readNanos == 0 {
		t.Fatalf("read telemetry was not recorded immediately: ops=%d latency=%d", readOps, readNanos)
	}
	time.Sleep(30 * time.Millisecond)
	backend.telemetry.mu.Lock()
	latencyAfterThink := backend.telemetry.readNanos
	backend.telemetry.mu.Unlock()
	if latencyAfterThink != readNanos {
		t.Fatal("reader latency changed while consumer was not reading")
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	backend.telemetry.mu.Lock()
	readOps, totalReadNanos := backend.telemetry.readOps, backend.telemetry.readNanos
	backend.telemetry.mu.Unlock()
	if readOps != 1 || totalReadNanos != readNanos {
		t.Fatalf("read telemetry ops=%d latency=%d, want 1 / %d", readOps, totalReadNanos, readNanos)
	}
}

func TestOpenReaderReadIsSampledBeforeCloseWithoutDoubleCount(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "0123456789abcdef0123456789abcdef"
	if err := store.NewRecordingDir(id); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("r"), 1<<20)
	if _, err := store.SavePayload(id, "tracks/main/playback.ts", bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	backend := store.StorageBackend.(*LocalFilesystemBackend)
	start := time.Now().UTC()
	backend.telemetry.sample(start, IngestSnapshot{}) // establish a clean read baseline
	reader, err := store.OpenPayloadReader(id, "tracks/main/playback.ts")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := io.Copy(io.Discard, reader); err != nil || n != int64(len(payload)) {
		t.Fatalf("read payload: bytes=%d err=%v", n, err)
	}
	beforeClose, _, samples := backend.telemetry.sample(start.Add(poolSamplePeriod), IngestSnapshot{})
	if beforeClose.ReadBytesTotal != uint64(len(payload)) || beforeClose.ReadBytesPerSecond == 0 || len(samples) != 1 || samples[0].ReadBytesPerSecond == 0 {
		t.Fatalf("open-reader reads were not visible to sampling: throughput=%#v samples=%#v", beforeClose, samples)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	afterClose, _, _ := backend.telemetry.sample(start.Add(2*poolSamplePeriod), IngestSnapshot{})
	if afterClose.ReadBytesTotal != uint64(len(payload)) || afterClose.ReadBytesPerSecond != 0 {
		t.Fatalf("Close double-counted read bytes: before=%#v after=%#v", beforeClose, afterClose)
	}
}
