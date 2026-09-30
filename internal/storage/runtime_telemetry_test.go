package storage

import (
	"testing"
	"time"
)

type fakeRuntimeStorageTelemetry struct {
	readBytes, writeBytes, errors uint64
	ingest                        IngestSnapshot
	snapshot                      RuntimeStorageTelemetrySnapshot
	reports                       int
}

func (f *fakeRuntimeStorageTelemetry) ReportStorageIOTotals(readBytes, writeBytes, errors uint64, ingest IngestSnapshot) error {
	f.readBytes, f.writeBytes, f.errors = readBytes, writeBytes, errors
	f.ingest = ingest
	f.reports++
	return nil
}

func (f *fakeRuntimeStorageTelemetry) StorageTelemetrySnapshot() (RuntimeStorageTelemetrySnapshot, error) {
	return f.snapshot, nil
}

func TestPoolMetricsOverlaysRuntimeTelemetryAndKeepsLocalCapacity(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := store.StorageBackend.(*LocalFilesystemBackend)
	backend.telemetry.recordRead(111, 1, time.Millisecond)
	backend.telemetry.recordWrite(222, time.Millisecond)
	backend.telemetry.recordError()
	fake := &fakeRuntimeStorageTelemetry{snapshot: RuntimeStorageTelemetrySnapshot{
		Throughput: PoolThroughput{
			ReadBytesPerSecond: 300, WriteBytesPerSecond: 400,
			ReadBytesTotal: 3000, WriteBytesTotal: 4000,
		},
		EstimatedCeiling: EstimatedCeiling{ReadBytesPerSecond: 500, WriteBytesPerSecond: 600, Source: "observed"},
		ErrorsTotal:      7,
		Samples:          []PoolSample{{At: time.Now().UTC(), ReadBytesPerSecond: 300, WriteBytesPerSecond: 400}},
		Ingest: IngestSnapshot{
			BufferCapacityBytes: 2 << 30, PerRecordingCapacityBytes: 1536 << 20,
			BufferUsedBytes: 1000, ReservedBytes: 1200,
			QueueObjects: 3, QueueBytes: 4000, OldestPersistAgeSeconds: 2.5,
			ActiveWriters: 1, WriterConcurrency: 1, StorageErrorsTotal: 7,
		},
	}}
	if err := store.ConfigureRuntimeStorageTelemetry(fake); err != nil {
		t.Fatal(err)
	}
	pool := store.PoolMetrics()
	if fake.reports != 1 || fake.readBytes != 111 || fake.writeBytes != 222 || fake.errors != 1 {
		t.Fatalf("local cumulative counters were not published: %+v", fake)
	}
	if fake.ingest.BufferCapacityBytes != DefaultIngestGlobalBytes || fake.ingest.QueueBytes != 0 {
		t.Fatalf("local ingest gauges were not included in report: %+v", fake.ingest)
	}
	if pool.Throughput.ReadBytesPerSecond != 300 || pool.Throughput.WriteBytesTotal != 4000 || pool.EstimatedCeiling.Source != "observed" || pool.ErrorsTotal != 7 || len(pool.Samples) != 1 {
		t.Fatalf("Host telemetry did not replace local projection: %+v", pool)
	}
	if pool.Capacity.TotalBytes == 0 || pool.Capacity.AvailableBytes == 0 {
		t.Fatalf("local capacity was lost while overlaying Host telemetry: %+v", pool.Capacity)
	}
	if pool.Buffer.CapacityBytes != 2<<30 || pool.Buffer.UsedBytes != 1000 || pool.Buffer.ReservedBytes != 1200 || pool.Queue.Objects != 3 || pool.Queue.Bytes != 4000 || pool.Queue.OldestAgeSeconds != 2.5 || pool.Writers.Active != 1 || pool.Writers.Limit != 1 {
		t.Fatalf("Host-global ingest gauges were not overlaid: buffer=%+v queue=%+v writers=%+v", pool.Buffer, pool.Queue, pool.Writers)
	}
}

func TestPoolMetricsWithoutRuntimeBridgeKeepLocalProjection(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := store.StorageBackend.(*LocalFilesystemBackend)
	start := time.Now().Add(-10 * time.Second)
	backend.telemetry.sample(start, IngestSnapshot{})
	backend.telemetry.recordWrite(10_000, time.Millisecond)
	pool := store.PoolMetrics()
	if pool.Throughput.WriteBytesTotal != 10_000 || pool.Throughput.WriteBytesPerSecond == 0 {
		t.Fatalf("standalone Store did not keep local telemetry: %+v", pool.Throughput)
	}
	if pool.EstimatedCeiling.Source != "unknown" {
		t.Fatalf("standalone Store ceiling = %q, want unknown without a sufficient window", pool.EstimatedCeiling.Source)
	}
}
