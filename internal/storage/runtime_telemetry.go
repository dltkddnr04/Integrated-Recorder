package storage

// RuntimeStorageTelemetry is the transport-neutral boundary between a Store
// and Runtime Host's process-wide storage I/O projection. Implementations
// should use bounded calls and bind reports to the calling process identity.
// Counters are cumulative and monotonically increasing for one process
// instance; callers publish them only at sampling points or on metrics reads.
type RuntimeStorageTelemetry interface {
	ReportStorageIOTotals(readBytes, writeBytes, errors uint64, ingest IngestSnapshot) error
	StorageTelemetrySnapshot() (RuntimeStorageTelemetrySnapshot, error)
}

// RuntimeStorageTelemetrySnapshot contains aggregate Host-owned I/O and ingest
// state. Physical capacity remains Store-owned and is never replaced by this
// process-wide projection.
type RuntimeStorageTelemetrySnapshot struct {
	Throughput       PoolThroughput
	EstimatedCeiling EstimatedCeiling
	ErrorsTotal      uint64
	Samples          []PoolSample
	Ingest           IngestSnapshot
}
