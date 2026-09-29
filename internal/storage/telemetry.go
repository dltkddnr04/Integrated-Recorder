package storage

import (
	"math"
	"sort"
	"sync"
	"syscall"
	"time"
)

const (
	PoolIDLocalPrimary  = "local-primary"
	poolSamplePeriod    = DefaultPoolSampleInterval
	maxPoolSamples      = int(DefaultPoolMetricsRetention / poolSamplePeriod)
	minDirectionSamples = 20
)

const minCeilingObservation = 5 * time.Minute

// MinimumMetricsRetention returns the shortest sample history that can retain
// enough non-idle observation time and directional samples for an observed
// throughput ceiling. The five-minute window is rounded up to whole sampling
// intervals because retention capacity is a whole-sample count. Sampling
// intervals are validated by the caller before this helper is used for
// configuration acceptance.
func MinimumMetricsRetention(sampleInterval time.Duration) time.Duration {
	if sampleInterval <= 0 {
		return minCeilingObservation
	}
	observationSamples := minCeilingObservation / sampleInterval
	if minCeilingObservation%sampleInterval != 0 {
		observationSamples++
	}
	minimumForObservation := observationSamples * sampleInterval
	minimumForDirections := sampleInterval * minDirectionSamples
	if minimumForDirections < minimumForObservation {
		return minimumForObservation
	}
	return minimumForDirections
}

// PoolSnapshot describes I/O performed through Integrated Recorder's local
// archive boundary. It is not an OS-wide device throughput measurement.
// Physical paths are intentionally absent.
type PoolSnapshot struct {
	ID               string           `json:"id"`
	DisplayName      string           `json:"display_name"`
	Kind             string           `json:"kind"`
	Role             string           `json:"role"`
	Health           string           `json:"health"`
	Capacity         PoolCapacity     `json:"capacity"`
	Throughput       PoolThroughput   `json:"throughput"`
	EstimatedCeiling EstimatedCeiling `json:"estimated_ceiling"`
	Buffer           PoolBuffer       `json:"buffer"`
	Queue            PoolQueue        `json:"queue"`
	Writers          PoolWriters      `json:"writers"`
	ErrorsTotal      uint64           `json:"errors_total"`
	Samples          []PoolSample     `json:"samples"`
}

type PoolBuffer struct {
	UsedBytes                 int64 `json:"used_bytes"`
	CapacityBytes             int64 `json:"capacity_bytes"`
	ReservedBytes             int64 `json:"reserved_bytes"`
	PerRecordingCapacityBytes int64 `json:"per_recording_capacity_bytes"`
}

type PoolQueue struct {
	Objects          int     `json:"objects"`
	Bytes            int64   `json:"bytes"`
	OldestAgeSeconds float64 `json:"oldest_age_seconds"`
}

type PoolWriters struct {
	Active int `json:"active"`
	Limit  int `json:"limit"`
}

type PoolCapacity struct {
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsageRatio     float64 `json:"usage_ratio"`
}

type PoolThroughput struct {
	ReadBytesPerSecond  uint64  `json:"read_bytes_per_second"`
	WriteBytesPerSecond uint64  `json:"write_bytes_per_second"`
	ReadBytesTotal      uint64  `json:"read_bytes_total"`
	WriteBytesTotal     uint64  `json:"write_bytes_total"`
	ReadLatencyMillis   float64 `json:"read_latency_ms"`
	WriteLatencyMillis  float64 `json:"write_latency_ms"`
}

type EstimatedCeiling struct {
	ReadBytesPerSecond  uint64 `json:"read_bytes_per_second,omitempty"`
	WriteBytesPerSecond uint64 `json:"write_bytes_per_second,omitempty"`
	Source              string `json:"source"`
}

type PoolSample struct {
	At                  time.Time `json:"at"`
	ReadBytesPerSecond  uint64    `json:"read_bytes_per_second"`
	WriteBytesPerSecond uint64    `json:"write_bytes_per_second"`
	BufferUsedBytes     int64     `json:"buffer_used_bytes"`
	PersistQueueBytes   int64     `json:"persist_queue_bytes"`
	observedFor         time.Duration
}

type telemetry struct {
	mu                            sync.Mutex
	readBytes, writeBytes         uint64
	readOps, writeOps             uint64
	readNanos, writeNanos         uint64
	errors                        uint64
	lastAt                        time.Time
	lastReadBytes, lastWriteBytes uint64
	lastReadOps, lastWriteOps     uint64
	lastReadNanos, lastWriteNanos uint64
	current                       PoolThroughput
	samples                       []PoolSample
	ioDegraded                    bool
	sampleInterval                time.Duration
	retention                     time.Duration
	maxSamples                    int
}

func newTelemetry() *telemetry {
	return newTelemetryWithRetention(DefaultPoolSampleInterval, DefaultPoolMetricsRetention)
}

func newTelemetryWithRetention(sampleInterval, retention time.Duration) *telemetry {
	maxSamples := int(retention / sampleInterval)
	return &telemetry{samples: make([]PoolSample, 0, maxSamples), sampleInterval: sampleInterval, retention: retention, maxSamples: maxSamples}
}

func (t *telemetry) configure(sampleInterval, retention time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sampleInterval = sampleInterval
	t.retention = retention
	t.maxSamples = int(retention / sampleInterval)
	if cap(t.samples) < t.maxSamples {
		grown := make([]PoolSample, len(t.samples), t.maxSamples)
		copy(grown, t.samples)
		t.samples = grown
	}
	if len(t.samples) > t.maxSamples {
		t.samples = append([]PoolSample(nil), t.samples[len(t.samples)-t.maxSamples:]...)
	}
}

// recordRead adds one or more completed filesystem Read calls immediately.
// Callers pass only time spent inside those calls, never consumer wait time.
func (t *telemetry) recordRead(bytes, operations uint64, elapsed time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.readBytes += bytes
	t.readOps += operations
	if elapsed > 0 {
		t.readNanos += uint64(elapsed)
	}
	t.mu.Unlock()
}

func (t *telemetry) recordWrite(bytes uint64, elapsed time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.writeBytes += bytes
	t.writeOps++
	t.ioDegraded = false
	if elapsed > 0 {
		t.writeNanos += uint64(elapsed)
	}
	t.mu.Unlock()
}

func (t *telemetry) recordError() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.errors++
	t.ioDegraded = true
	t.mu.Unlock()
}

func (t *telemetry) degraded() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	degraded := t.ioDegraded
	t.mu.Unlock()
	return degraded
}

func (t *telemetry) sample(now time.Time, ingest IngestSnapshot) (PoolThroughput, uint64, []PoolSample) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastAt.IsZero() {
		t.lastAt = now
		t.lastReadBytes, t.lastWriteBytes = t.readBytes, t.writeBytes
		t.lastReadOps, t.lastWriteOps = t.readOps, t.writeOps
		t.lastReadNanos, t.lastWriteNanos = t.readNanos, t.writeNanos
	} else if now.Sub(t.lastAt) >= t.sampleInterval {
		elapsed := now.Sub(t.lastAt)
		read := delta(t.readBytes, t.lastReadBytes)
		write := delta(t.writeBytes, t.lastWriteBytes)
		readOps := delta(t.readOps, t.lastReadOps)
		writeOps := delta(t.writeOps, t.lastWriteOps)
		t.current = PoolThroughput{
			ReadBytesPerSecond: rate(read, elapsed), WriteBytesPerSecond: rate(write, elapsed),
			ReadLatencyMillis:  meanMillis(delta(t.readNanos, t.lastReadNanos), readOps),
			WriteLatencyMillis: meanMillis(delta(t.writeNanos, t.lastWriteNanos), writeOps),
		}
		t.samples = append(t.samples, PoolSample{At: now.UTC(), ReadBytesPerSecond: t.current.ReadBytesPerSecond, WriteBytesPerSecond: t.current.WriteBytesPerSecond, BufferUsedBytes: ingest.BufferUsedBytes, PersistQueueBytes: ingest.QueueBytes, observedFor: elapsed})
		cutoff := now.Add(-t.retention)
		first := 0
		for first < len(t.samples) && t.samples[first].At.Before(cutoff) {
			first++
		}
		if first > 0 {
			t.samples = append([]PoolSample(nil), t.samples[first:]...)
		}
		if len(t.samples) > t.maxSamples {
			t.samples = append([]PoolSample(nil), t.samples[len(t.samples)-t.maxSamples:]...)
		}
		t.lastAt = now
		t.lastReadBytes, t.lastWriteBytes = t.readBytes, t.writeBytes
		t.lastReadOps, t.lastWriteOps = t.readOps, t.writeOps
		t.lastReadNanos, t.lastWriteNanos = t.readNanos, t.writeNanos
	}
	copySamples := append([]PoolSample(nil), t.samples...)
	throughput := t.current
	throughput.ReadBytesTotal = t.readBytes
	throughput.WriteBytesTotal = t.writeBytes
	return throughput, t.errors, copySamples
}

func delta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func rate(bytes uint64, elapsed time.Duration) uint64 {
	if elapsed <= 0 {
		return 0
	}
	return uint64(float64(bytes) / elapsed.Seconds())
}

func meanMillis(nanos, operations uint64) float64 {
	if operations == 0 {
		return 0
	}
	return float64(nanos) / float64(operations) / float64(time.Millisecond)
}

// PoolMetrics returns recorder-originated I/O rates, bounded recent samples,
// and best-effort filesystem capacity. The kind identifies the implemented
// backend as local; media type detection remains best-effort and separate.
func (s *LocalFilesystemBackend) PoolMetrics() PoolSnapshot {
	return s.poolMetrics(IngestSnapshot{})
}

type poolMetricsWithIngest interface {
	poolMetrics(IngestSnapshot) PoolSnapshot
}

func (s *LocalFilesystemBackend) poolMetrics(ingest IngestSnapshot) PoolSnapshot {
	return s.poolMetricsAt(ingest, time.Now())
}

func (s *LocalFilesystemBackend) poolMetricsAt(ingest IngestSnapshot, now time.Time) PoolSnapshot {
	throughput, errorsTotal, samples := s.telemetry.sample(now, ingest)
	health := "healthy"
	if s.telemetry.degraded() {
		health = "degraded"
	}
	result := PoolSnapshot{ID: PoolIDLocalPrimary, DisplayName: "기본 보관 저장소", Kind: "local", Role: "primary", Health: health, Throughput: throughput, Samples: samples}
	result.EstimatedCeiling = estimateCeiling(samples)
	var fs syscall.Statfs_t
	if err := syscall.Statfs(s.root, &fs); err != nil {
		result.Health = "unavailable"
		result.ErrorsTotal = errorsTotal + 1
		return result
	}
	if fs.Bsize <= 0 {
		result.Health = "unavailable"
		result.ErrorsTotal = errorsTotal + 1
		return result
	}
	capacity, valid := capacityFromBlocks(uint64(fs.Blocks), uint64(fs.Bfree), uint64(fs.Bavail), uint64(fs.Bsize))
	if !valid {
		result.Health = "unavailable"
		result.ErrorsTotal = errorsTotal + 1
		return result
	}
	result.Capacity = capacity
	total := capacity.TotalBytes
	if total > 0 {
		result.Capacity.UsageRatio = math.Min(1, float64(capacity.UsedBytes)/float64(total))
	}
	result.ErrorsTotal = errorsTotal
	return result
}

// capacityFromBlocks validates the kernel's Statfs counters and checks every
// byte conversion before reporting capacity. Invalid values are unavailable,
// never wrapped into plausible-looking large numbers.
func capacityFromBlocks(blocks, free, available, blockSize uint64) (PoolCapacity, bool) {
	if blockSize == 0 || blocks < free || available > free {
		return PoolCapacity{}, false
	}
	total, ok := checkedByteProduct(blocks, blockSize)
	if !ok {
		return PoolCapacity{}, false
	}
	used, ok := checkedByteProduct(blocks-free, blockSize)
	if !ok {
		return PoolCapacity{}, false
	}
	availableBytes, ok := checkedByteProduct(available, blockSize)
	if !ok {
		return PoolCapacity{}, false
	}
	return PoolCapacity{TotalBytes: total, UsedBytes: used, AvailableBytes: availableBytes}, true
}

func (s *LocalFilesystemBackend) samplePool(ingest IngestSnapshot, now time.Time) {
	s.telemetry.sample(now, ingest)
}

func estimateCeiling(samples []PoolSample) EstimatedCeiling {
	reads, writes := make([]uint64, 0, len(samples)), make([]uint64, 0, len(samples))
	var activeObservation time.Duration
	for _, sample := range samples {
		if sample.ReadBytesPerSecond > 0 {
			reads = append(reads, sample.ReadBytesPerSecond)
		}
		if sample.WriteBytesPerSecond > 0 {
			writes = append(writes, sample.WriteBytesPerSecond)
		}
		if sample.ReadBytesPerSecond > 0 || sample.WriteBytesPerSecond > 0 {
			if sample.observedFor > 0 {
				remaining := minCeilingObservation - activeObservation
				if sample.observedFor >= remaining {
					activeObservation = minCeilingObservation
				} else {
					activeObservation += sample.observedFor
				}
			}
		}
	}
	// Require five minutes of non-idle intervals so one short spike cannot be
	// presented as a pool's observed ceiling. Sum actual elapsed intervals,
	// rather than assuming every configured sample has the same cadence.
	if activeObservation < minCeilingObservation {
		return EstimatedCeiling{Source: "unknown"}
	}
	result := EstimatedCeiling{Source: "observed"}
	if len(reads) >= minDirectionSamples {
		result.ReadBytesPerSecond = percentile95(reads)
	}
	if len(writes) >= minDirectionSamples {
		result.WriteBytesPerSecond = percentile95(writes)
	}
	if result.ReadBytesPerSecond == 0 && result.WriteBytesPerSecond == 0 {
		return EstimatedCeiling{Source: "unknown"}
	}
	return result
}

func percentile95(values []uint64) uint64 {
	if len(values) == 0 {
		return 0
	}
	values = append([]uint64(nil), values...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := int(math.Ceil(float64(len(values))*.95)) - 1
	if index < 0 {
		index = 0
	}
	return values[index]
}
