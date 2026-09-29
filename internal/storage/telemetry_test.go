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
	active := func(interval time.Duration, count int) []PoolSample {
		samples := make([]PoolSample, count)
		for i := range samples {
			samples[i] = PoolSample{ReadBytesPerSecond: 1_000_000, WriteBytesPerSecond: 2_000_000, observedFor: interval}
		}
		return samples
	}
	tests := []struct {
		name     string
		samples  []PoolSample
		observed bool
	}{
		{"default 5s below five minutes", active(5*time.Second, 59), false},
		{"default 5s reaches five minutes", active(5*time.Second, 60), true},
		{"1s cadence does not use sample count", active(time.Second, 60), false},
		{"1s cadence reaches five minutes", active(time.Second, 300), true},
		{"10s cadence reaches five minutes without 60 samples", active(10*time.Second, 30), true},
		{"1m cadence reaches five minutes with enough directional samples", active(time.Minute, 20), true},
		{"1h cadence fits 24h retention", active(time.Hour, 20), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := estimateCeiling(test.samples)
			if (got.Source == "observed") != test.observed {
				t.Fatalf("estimated ceiling = %#v, observed want %v", got, test.observed)
			}
		})
	}
	got := estimateCeiling(active(5*time.Second, 60))
	if got.ReadBytesPerSecond != 1_000_000 || got.WriteBytesPerSecond != 2_000_000 {
		t.Fatalf("sustained ceiling = %#v", got)
	}
}

func TestObservedCeilingCountsOnlyActualActiveIntervals(t *testing.T) {
	samples := make([]PoolSample, 19)
	for i := range samples {
		samples[i] = PoolSample{ReadBytesPerSecond: 500, observedFor: 15 * time.Second}
	}
	samples = append(samples, PoolSample{observedFor: time.Hour}) // Idle time must not qualify.
	samples = append(samples, PoolSample{ReadBytesPerSecond: 500, observedFor: 15 * time.Second})
	if got := estimateCeiling(samples); got.Source != "observed" || got.ReadBytesPerSecond != 500 {
		t.Fatalf("active-time ceiling = %#v", got)
	}
	samples[len(samples)-1].observedFor = 14 * time.Second
	if got := estimateCeiling(samples); got.Source != "unknown" {
		t.Fatalf("idle interval incorrectly counted toward active observation: %#v", got)
	}
}

func TestObservedCeilingKeepsDirectionalSampleMinimumAndPercentile(t *testing.T) {
	var samples []PoolSample
	for i := uint64(1); i <= minDirectionSamples; i++ {
		samples = append(samples, PoolSample{ReadBytesPerSecond: i, WriteBytesPerSecond: i * 10, observedFor: 15 * time.Second})
	}
	got := estimateCeiling(samples)
	if got.Source != "observed" || got.ReadBytesPerSecond != 19 || got.WriteBytesPerSecond != 190 {
		t.Fatalf("directional p95 ceiling = %#v", got)
	}
	got = estimateCeiling(samples[:minDirectionSamples-1])
	if got.Source != "unknown" {
		t.Fatalf("insufficient observation and directional samples = %#v", got)
	}
	var longButSparse []PoolSample
	for i := uint64(1); i < minDirectionSamples; i++ {
		longButSparse = append(longButSparse, PoolSample{ReadBytesPerSecond: i, observedFor: 20 * time.Second})
	}
	if got := estimateCeiling(longButSparse); got.Source != "unknown" {
		t.Fatalf("elapsed time bypassed the directional sample minimum: %#v", got)
	}
}

func TestTelemetryUsesActualElapsedIntervalsForCeiling(t *testing.T) {
	telemetry := newTelemetryWithRetention(time.Second, 24*time.Hour)
	start := time.Unix(100, 0).UTC()
	telemetry.sample(start, IngestSnapshot{})
	for i := 0; i < 20; i++ {
		telemetry.recordWrite(15000, time.Millisecond)
		// Deliberately use 15s windows despite the 1s configured minimum cadence.
		telemetry.sample(start.Add(time.Duration(i+1)*15*time.Second), IngestSnapshot{})
	}
	telemetry.mu.Lock()
	samples := append([]PoolSample(nil), telemetry.samples...)
	telemetry.mu.Unlock()
	if len(samples) != 20 {
		t.Fatalf("recorded telemetry samples = %d, want 20", len(samples))
	}
	for i, sample := range samples {
		if sample.observedFor != 15*time.Second {
			t.Fatalf("sample %d observed interval = %s, want 15s", i, sample.observedFor)
		}
	}
	if got := estimateCeiling(samples); got.Source != "observed" {
		t.Fatalf("ceiling ignored actual timestamp intervals: %#v", got)
	}
}

func TestHourlyTelemetryRetainsEnoughSamplesForObservedCeiling(t *testing.T) {
	telemetry := newTelemetryWithRetention(time.Hour, 24*time.Hour)
	start := time.Unix(200, 0).UTC()
	telemetry.sample(start, IngestSnapshot{})
	for hour := 1; hour <= 20; hour++ {
		telemetry.recordWrite(3600, time.Millisecond)
		telemetry.sample(start.Add(time.Duration(hour)*time.Hour), IngestSnapshot{})
	}

	telemetry.mu.Lock()
	samples := append([]PoolSample(nil), telemetry.samples...)
	maxSamples := telemetry.maxSamples
	telemetry.mu.Unlock()
	if maxSamples != 24 || len(samples) != 20 {
		t.Fatalf("hourly telemetry retained %d samples (capacity %d), want 20 within 24h", len(samples), maxSamples)
	}
	if got := estimateCeiling(samples); got.Source != "observed" || got.WriteBytesPerSecond != 1 {
		t.Fatalf("hourly telemetry ceiling = %#v, want an observed one-byte/s write ceiling", got)
	}
}

func TestPoolSampleObservationIntervalIsNotSerialized(t *testing.T) {
	encoded, err := json.Marshal(PoolSample{At: time.Unix(1, 0), observedFor: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("observedFor")) || bytes.Contains(encoded, []byte("observed_for")) {
		t.Fatalf("internal observation interval leaked to API JSON: %s", encoded)
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
