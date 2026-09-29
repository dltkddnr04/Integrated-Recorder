package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	ingestAllocationChunk        int64 = 64 << 10
	DefaultMaxIngestPayloadBytes int64 = 512 << 20
	// MaxIngestObjects bounds completed objects awaiting persistence.
	MaxIngestObjects = 128
	// DefaultIngestGlobalBytes bounds reservations across the process. This is
	// 1 GiB: it can hold two maximum-sized (512 MiB) response bodies.
	DefaultIngestGlobalBytes int64 = 1 << 30
	// DefaultIngestPerRecordingBytes accounts for both the old and new backing
	// arrays while a maximum-sized payload grows from 256 MiB to 512 MiB.
	DefaultIngestPerRecordingBytes int64 = 768 << 20
	DefaultIngestWriters                 = 1
	DefaultPersistAttempts               = 5
	DefaultRetryInitialBackoff           = 100 * time.Millisecond
	DefaultRetryMaxBackoff               = 800 * time.Millisecond
	DefaultPoolSampleInterval            = 5 * time.Second
	DefaultPoolMetricsRetention          = 24 * time.Hour
	MaxIngestGlobalBytes           int64 = 2 << 30
	MaxIngestPerRecordingBytes     int64 = 1536 << 20
	MaxIngestPayloadBytes          int64 = 1 << 30
)

var (
	ErrIngestClosed       = errors.New("storage ingest service is closed")
	ErrIngestTooLarge     = errors.New("ingest payload exceeds size limit")
	ErrIngestReservation  = errors.New("ingest payload exceeded its reserved byte budget")
	ErrIngestSizeMismatch = ErrPayloadSizeMismatch
)

type IngestOptions struct {
	QueueObjects      int
	GlobalBytes       int64
	PerRecordingBytes int64
	MaxPayloadBytes   int64
	Writers           int
	PersistAttempts   int
	RetryBase         time.Duration
	RetryMaxBackoff   time.Duration
	SampleInterval    time.Duration
	MetricsRetention  time.Duration
}

func DefaultIngestOptions() IngestOptions {
	return IngestOptions{
		QueueObjects: MaxIngestObjects, GlobalBytes: DefaultIngestGlobalBytes,
		PerRecordingBytes: DefaultIngestPerRecordingBytes, MaxPayloadBytes: DefaultMaxIngestPayloadBytes,
		Writers: DefaultIngestWriters, PersistAttempts: DefaultPersistAttempts,
		RetryBase: DefaultRetryInitialBackoff, RetryMaxBackoff: DefaultRetryMaxBackoff,
		SampleInterval: DefaultPoolSampleInterval, MetricsRetention: DefaultPoolMetricsRetention,
	}
}

// ValidateIngestOptions applies public operational bounds. The byte ceilings
// are twice the current production defaults, permitting one step of capacity
// tuning while capping volatile payload memory at 2 GiB process-wide, 1.5 GiB
// per recording, and 1 GiB per source object. Per-recording/global budgets
// must also admit the largest transient old+new slice allocation used by
// ReadPayload.
func ValidateIngestOptions(options IngestOptions) error {
	if options.GlobalBytes <= 0 || options.GlobalBytes > MaxIngestGlobalBytes {
		return errors.New("global buffer limit must be greater than zero and at most 2 GiB")
	}
	if options.PerRecordingBytes <= 0 || options.PerRecordingBytes > MaxIngestPerRecordingBytes {
		return errors.New("per-recording buffer limit must be greater than zero and at most 1536 MiB")
	}
	if options.MaxPayloadBytes <= 0 || options.MaxPayloadBytes > MaxIngestPayloadBytes {
		return errors.New("maximum payload size must be greater than zero and at most 1 GiB")
	}
	if options.PerRecordingBytes > options.GlobalBytes {
		return errors.New("per-recording buffer limit cannot exceed the global buffer limit")
	}
	if options.MaxPayloadBytes > options.PerRecordingBytes {
		return errors.New("maximum payload size cannot exceed the per-recording buffer limit")
	}
	peak := maxReallocationPeak(options.MaxPayloadBytes)
	if options.GlobalBytes < peak || options.PerRecordingBytes < peak {
		return errors.New("global and per-recording buffer limits must each fit the maximum payload's temporary reallocation peak")
	}
	if options.QueueObjects < 1 || options.QueueObjects > MaxIngestObjects {
		return errors.New("pending storage queue capacity must be between 1 and 128 objects")
	}
	if options.Writers != 1 {
		return errors.New("writer concurrency must be 1 to preserve canonical commit ordering")
	}
	if options.PersistAttempts < 1 || options.PersistAttempts > 10 {
		return errors.New("storage retry attempts must be between 1 and 10")
	}
	if options.RetryBase < 10*time.Millisecond || options.RetryBase > 30*time.Second {
		return errors.New("initial storage retry backoff must be between 10 ms and 30 s")
	}
	if options.RetryMaxBackoff < options.RetryBase || options.RetryMaxBackoff > 5*time.Minute {
		return errors.New("maximum storage retry backoff must be at least the initial backoff and at most 5 minutes")
	}
	if options.SampleInterval < time.Second || options.SampleInterval > time.Hour {
		return errors.New("storage metrics sampling interval must be between 1 s and 1 hour")
	}
	if options.MetricsRetention < options.SampleInterval || options.MetricsRetention > 24*time.Hour {
		return errors.New("storage metrics retention must be at least the sampling interval and at most 24 hours")
	}
	return nil
}

type IngestSnapshot struct {
	BufferCapacityBytes       int64   `json:"buffer_capacity_bytes"`
	PerRecordingCapacityBytes int64   `json:"per_recording_capacity_bytes"`
	BufferUsedBytes           int64   `json:"buffer_used_bytes"`
	ReservedBytes             int64   `json:"reserved_bytes"`
	QueueObjects              int     `json:"queue_objects"`
	QueueBytes                int64   `json:"queue_bytes"`
	OldestPersistAgeSeconds   float64 `json:"oldest_persist_age_seconds"`
	ActiveWriters             int     `json:"active_writers"`
	WriterConcurrency         int     `json:"writer_concurrency"`
	StorageErrorsTotal        uint64  `json:"storage_errors_total"`
}

type IngestService struct {
	store   *Store
	options IngestOptions
	jobs    chan *ingestJob
	slots   chan struct{}

	mu                  sync.Mutex
	closed              bool
	changed             chan struct{}
	overflowing         bool
	reservedGlobal      int64
	reservedByRecording map[string]int64
	usedBytes           int64
	queuedBytes         int64
	queuedObjects       int
	oldestQueued        time.Time
	queueTimes          map[*ingestJob]time.Time
	activeWriters       int
	activeSubmits       int
	storageErrors       uint64
	failedRecordings    map[string]bool
	// reallocationHook is a deterministic test seam for observing transient
	// old+new backing-array reservations. Production leaves it nil.
	reallocationHook func(oldCapacity, newCapacity int64)

	workers     sync.WaitGroup
	closeOnce   sync.Once
	closeDone   chan struct{}
	samplerStop chan struct{}
	samplerDone chan struct{}
}

type storageErrorRecorder interface{ recordStorageError() }

type ingestJob struct {
	recordingID string
	payload     *IngestPayload
	persist     func([]byte) (PayloadResult, error)
	complete    func(PayloadResult, error)
	queuedAt    time.Time
}

func (j *ingestJob) payloadBytes() int64 {
	if j == nil || j.payload == nil {
		return 0
	}
	return int64(len(j.payload.data))
}

// IngestPayload owns a bounded volatile response body. It is not canonical
// data; the archive becomes aware of bytes only after the persist callback
// finishes all durable metadata ordering.
type IngestPayload struct {
	service     *IngestService
	recordingID string
	data        []byte
	result      PayloadResult
	reserved    int64
	used        int64
	once        sync.Once
}

func (p *IngestPayload) Result() PayloadResult { return p.result }
func (p *IngestPayload) Bytes() []byte         { return p.data }

// Release returns all accounting exactly once. Submit transfers ownership to
// the service; callers release only when Submit fails.
func (p *IngestPayload) Release() {
	if p == nil || p.service == nil {
		return
	}
	p.once.Do(func() { p.service.releasePayload(p) })
}

func NewIngestService(store *Store, options IngestOptions) (*IngestService, error) {
	if store == nil {
		return nil, errors.New("archive store is required")
	}
	defaults := DefaultIngestOptions()
	if options.QueueObjects <= 0 {
		options.QueueObjects = defaults.QueueObjects
	}
	if options.GlobalBytes <= 0 {
		options.GlobalBytes = defaults.GlobalBytes
	}
	if options.PerRecordingBytes <= 0 {
		options.PerRecordingBytes = defaults.PerRecordingBytes
	}
	if options.MaxPayloadBytes <= 0 {
		options.MaxPayloadBytes = defaults.MaxPayloadBytes
	}
	if options.Writers <= 0 {
		options.Writers = defaults.Writers
	}
	if options.PersistAttempts <= 0 {
		options.PersistAttempts = defaults.PersistAttempts
	}
	if options.RetryBase <= 0 {
		options.RetryBase = defaults.RetryBase
	}
	if options.RetryMaxBackoff <= 0 {
		options.RetryMaxBackoff = defaults.RetryMaxBackoff
	}
	if options.SampleInterval <= 0 {
		options.SampleInterval = defaults.SampleInterval
	}
	if options.MetricsRetention <= 0 {
		options.MetricsRetention = defaults.MetricsRetention
	}
	if err := ValidateIngestOptions(options); err != nil || options.Writers > options.QueueObjects {
		return nil, errors.New("invalid bounded ingest configuration")
	}
	if backend, ok := store.StorageBackend.(*LocalFilesystemBackend); ok {
		backend.telemetry.configure(options.SampleInterval, options.MetricsRetention)
	}
	s := &IngestService{store: store, options: options, jobs: make(chan *ingestJob, options.QueueObjects), slots: make(chan struct{}, options.QueueObjects), changed: make(chan struct{}), reservedByRecording: map[string]int64{}, queueTimes: map[*ingestJob]time.Time{}, failedRecordings: map[string]bool{}, closeDone: make(chan struct{}), samplerStop: make(chan struct{}), samplerDone: make(chan struct{})}
	for i := 0; i < options.Writers; i++ {
		s.workers.Add(1)
		go s.writer()
	}
	go s.sampleLoop()
	return s, nil
}

// ReadPayload reserves an advertised size as an allocation hint and otherwise
// reserves incrementally as the backing array grows. Content-Length is only
// an admission hint; max and exact-size checks remain authoritative.
func (s *IngestService) ReadPayload(ctx context.Context, recordingID string, src io.Reader, max, expectedSize, reservationHint int64) (*IngestPayload, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if src == nil || max <= 0 || expectedSize > max || expectedSize < -1 {
		return nil, errors.New("invalid ingest payload request")
	}
	if max > s.options.MaxPayloadBytes {
		return nil, ErrIngestTooLarge
	}
	reserve := reservationHint
	if expectedSize >= 0 {
		reserve = expectedSize
	}
	if reserve > max {
		return nil, ErrIngestTooLarge
	}
	// Unknown-length bodies reserve as their backing array grows. A declared
	// size is only a hint and is converted to the same geometric capacity used
	// by the allocation path.
	reserve = allocationCapacity(reserve, max)
	if err := s.reserve(ctx, recordingID, reserve); err != nil {
		return nil, err
	}
	payload := &IngestPayload{service: s, recordingID: recordingID, reserved: reserve}
	hash := sha256.New()
	buf := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			payload.Release()
			return nil, err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if int64(len(payload.data))+int64(n) > max {
				payload.Release()
				return nil, ErrIngestTooLarge
			}
			if expectedSize >= 0 && int64(len(payload.data))+int64(n) > expectedSize {
				payload.Release()
				return nil, ErrIngestSizeMismatch
			}
			required := int64(len(payload.data)) + int64(n)
			allocation := allocationCapacity(required, max)
			oldCapacity := int64(cap(payload.data))
			if allocation > oldCapacity {
				// A growth temporarily retains both arrays. Account for the full
				// new allocation while the old reservation is still held. The
				// exclusive lease prevents partial buffers from deadlocking while
				// competing streams wait on the same remaining budget.
				additional := allocation
				if oldCapacity == 0 {
					additional = allocation - payload.reserved
					if additional < 0 {
						additional = 0
					}
				}
				needsLease := additional > 0 && payload.reserved > 0
				if needsLease {
					if err := s.acquireReallocationLease(); err != nil {
						payload.Release()
						return nil, err
					}
				}
				growthErr := func() error {
					if needsLease {
						defer s.releaseReallocationLease()
					}
					if err := s.reserve(ctx, recordingID, additional); err != nil {
						return err
					}
					payload.reserved += additional
					if s.reallocationHook != nil {
						s.reallocationHook(oldCapacity, allocation)
					}
					grown := make([]byte, len(payload.data), int(allocation))
					copy(grown, payload.data)
					payload.data = grown
					if oldCapacity > 0 {
						payload.reserved -= oldCapacity
						s.releaseReservation(recordingID, oldCapacity)
					}
					// A small or inaccurate Content-Length hint can reserve more
					// than the new array. Shrink after allocation succeeds.
					if excess := payload.reserved - allocation; excess > 0 {
						payload.reserved -= excess
						s.releaseReservation(recordingID, excess)
					}
					return nil
				}()
				if growthErr != nil {
					payload.Release()
					return nil, growthErr
				}
			}
			payload.data = append(payload.data, buf[:n]...)
			_, _ = hash.Write(buf[:n])
			payload.used += int64(n)
			s.addUsed(int64(n))
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			payload.Release()
			return nil, readErr
		}
		if n == 0 {
			payload.Release()
			return nil, io.ErrNoProgress
		}
	}
	if expectedSize >= 0 && int64(len(payload.data)) != expectedSize {
		payload.Release()
		return nil, fmt.Errorf("%w: got %d bytes; expected %d", ErrIngestSizeMismatch, len(payload.data), expectedSize)
	}
	allocated := int64(cap(payload.data))
	if allocated < payload.reserved {
		s.releaseReservation(recordingID, payload.reserved-allocated)
		payload.reserved = allocated
	}
	payload.result = PayloadResult{Size: int64(len(payload.data)), SHA256: hex.EncodeToString(hash.Sum(nil))}
	return payload, nil
}

func allocationCapacity(required, maximum int64) int64 {
	if required <= 0 {
		return 0
	}
	if required >= maximum {
		return maximum
	}
	capacity := min(ingestAllocationChunk, maximum)
	for capacity < required {
		next := capacity * 2
		if next <= capacity || next > maximum {
			return maximum
		}
		capacity = next
	}
	return capacity
}

// maxReallocationPeak is the largest old+new array pair on the geometric
// growth path. It lets option validation guarantee one maximum payload can
// always finish a reallocation without exceeding either configured budget.
func maxReallocationPeak(maximum int64) int64 {
	if maximum <= 0 {
		return 0
	}
	previous := allocationCapacity(maximum-1, maximum)
	if previous == maximum {
		// maximum-1 may round to maximum; find the preceding capacity.
		previous = 0
		capacity := min(ingestAllocationChunk, maximum)
		for capacity < maximum {
			next := capacity * 2
			if next <= capacity || next >= maximum {
				previous = capacity
				break
			}
			capacity = next
		}
	}
	if previous == 0 {
		return maximum
	}
	return previous + maximum
}

// acquireReallocationLease gives one growing reader exclusive access to the
// remaining byte budget. Contenders fail fast and release their partial
// buffers, while the holder's subsequent reserve remains context-cancellable.
func (s *IngestService) acquireReallocationLease() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrIngestClosed
	}
	if s.overflowing {
		return ErrIngestReservation
	}
	s.overflowing = true
	s.signalLocked()
	return nil
}

func (s *IngestService) releaseReallocationLease() {
	s.mu.Lock()
	s.overflowing = false
	s.signalLocked()
	s.mu.Unlock()
}

func (s *IngestService) reserve(ctx context.Context, recordingID string, bytes int64) error {
	if bytes == 0 {
		return nil
	}
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return ErrIngestClosed
		}
		recordingUsed := s.reservedByRecording[recordingID]
		if bytes <= s.options.GlobalBytes-s.reservedGlobal && bytes <= s.options.PerRecordingBytes-recordingUsed {
			s.reservedGlobal += bytes
			s.reservedByRecording[recordingID] = recordingUsed + bytes
			s.signalLocked()
			s.mu.Unlock()
			return nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (s *IngestService) addUsed(bytes int64) {
	s.mu.Lock()
	s.usedBytes += bytes
	s.mu.Unlock()
}

func (s *IngestService) releaseReservation(recordingID string, bytes int64) {
	if bytes <= 0 {
		return
	}
	s.mu.Lock()
	s.reservedGlobal -= bytes
	s.reservedByRecording[recordingID] -= bytes
	if s.reservedByRecording[recordingID] == 0 {
		delete(s.reservedByRecording, recordingID)
	}
	s.signalLocked()
	s.mu.Unlock()
}

func (s *IngestService) releasePayload(payload *IngestPayload) {
	s.mu.Lock()
	s.usedBytes -= payload.used
	s.reservedGlobal -= payload.reserved
	s.reservedByRecording[payload.recordingID] -= payload.reserved
	if s.reservedByRecording[payload.recordingID] <= 0 {
		delete(s.reservedByRecording, payload.recordingID)
	}
	s.signalLocked()
	s.mu.Unlock()
	payload.data = nil
}

// Submit transfers payload ownership after placing it in the bounded object
// queue. Persistence failures are retried over the same byte slice. A failed
// submission leaves ownership with the caller.
func (s *IngestService) Submit(ctx context.Context, payload *IngestPayload, persist func([]byte) (PayloadResult, error), complete func(PayloadResult, error)) error {
	if payload == nil || payload.service != s || persist == nil || complete == nil {
		return errors.New("invalid ingest job")
	}
	return s.submit(ctx, &ingestJob{recordingID: payload.recordingID, payload: payload, persist: persist, complete: complete})
}

// SubmitCommit queues a bounded, byte-free metadata operation behind durable
// payload jobs. It is used for manifest observations so slow root-document
// writes do not hold the HLS poller; the callback must persist the latest
// recording projection when it runs instead of a stale captured snapshot.
func (s *IngestService) SubmitCommit(ctx context.Context, recordingID string, commit func() error, complete func(error)) error {
	if recordingID == "" || commit == nil || complete == nil {
		return errors.New("invalid ingest commit")
	}
	return s.submit(ctx, &ingestJob{
		recordingID: recordingID,
		persist: func([]byte) (PayloadResult, error) {
			return PayloadResult{}, commit()
		},
		complete: func(_ PayloadResult, err error) { complete(err) },
	})
}

func (s *IngestService) submit(_ context.Context, job *ingestJob) error {
	// The body/metadata handed here is already complete. Acquisition context
	// cancellation (for example, a user pressing Stop) must not discard it
	// while a bounded queue is temporarily full. Submissions that began before
	// Close are allowed to wait for a slot and enter the drain; Close rejects
	// only submissions that begin after admission has stopped.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrIngestClosed
	}
	s.activeSubmits++
	s.signalLocked()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.activeSubmits--
		s.signalLocked()
		s.mu.Unlock()
	}()

	job.queuedAt = time.Now()
	for {
		s.mu.Lock()
		changed := s.changed
		s.mu.Unlock()
		select {
		case s.slots <- struct{}{}:
			goto slotAcquired
		case <-changed:
		}
	}

slotAcquired:
	s.mu.Lock()
	s.queuedObjects++
	s.queuedBytes += job.payloadBytes()
	s.queueTimes[job] = job.queuedAt
	s.refreshOldestQueuedLocked()
	s.signalLocked()
	s.mu.Unlock()
	// The close coordinator keeps jobs open until activeSubmits reaches zero.
	s.jobs <- job
	return nil
}

func (s *IngestService) writer() {
	defer s.workers.Done()
	for job := range s.jobs {
		s.mu.Lock()
		s.queuedObjects--
		s.queuedBytes -= job.payloadBytes()
		delete(s.queueTimes, job)
		s.refreshOldestQueuedLocked()
		s.activeWriters++
		s.signalLocked()
		s.mu.Unlock()
		<-s.slots
		s.mu.Lock()
		failed := s.failedRecordings[job.recordingID]
		s.mu.Unlock()
		var result PayloadResult
		var err error
		if failed {
			err = errors.New("canonical storage commit failed")
		} else {
			result, err = s.persistWithRetry(job)
		}
		if err != nil {
			s.mu.Lock()
			s.storageErrors++
			s.failedRecordings[job.recordingID] = true
			s.mu.Unlock()
			if recorder, ok := s.store.StorageBackend.(storageErrorRecorder); ok {
				recorder.recordStorageError()
			}
		}
		if job.payload != nil {
			job.payload.Release()
		}
		job.complete(result, err)
		s.mu.Lock()
		s.activeWriters--
		s.signalLocked()
		s.mu.Unlock()
	}
}

func (s *IngestService) persistWithRetry(job *ingestJob) (PayloadResult, error) {
	var result PayloadResult
	var err error
	var data []byte
	if job.payload != nil {
		data = job.payload.data
	}
	for attempt := 0; attempt < s.options.PersistAttempts; attempt++ {
		result, err = job.persist(data)
		if err == nil {
			return result, nil
		}
		// Store payload callbacks already measure successful writes. Callback
		// failures are counted here; avoid logging error contents or paths.
		if attempt+1 == s.options.PersistAttempts {
			break
		}
		delay := retryBackoff(s.options.RetryBase, s.options.RetryMaxBackoff, attempt)
		timer := time.NewTimer(delay)
		<-timer.C
	}
	return PayloadResult{}, errors.New("canonical storage commit failed after bounded retries")
}

func retryBackoff(initial, maximum time.Duration, retryIndex int) time.Duration {
	delay := initial
	for i := 0; i < retryIndex && delay < maximum; i++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func (s *IngestService) Snapshot() IngestSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	oldest := 0.0
	var oldestQueued time.Time
	for _, queuedAt := range s.queueTimes {
		if oldestQueued.IsZero() || queuedAt.Before(oldestQueued) {
			oldestQueued = queuedAt
		}
	}
	if !oldestQueued.IsZero() {
		oldest = time.Since(oldestQueued).Seconds()
	}
	return IngestSnapshot{BufferCapacityBytes: s.options.GlobalBytes, PerRecordingCapacityBytes: s.options.PerRecordingBytes, BufferUsedBytes: s.usedBytes, ReservedBytes: s.reservedGlobal, QueueObjects: s.queuedObjects, QueueBytes: s.queuedBytes, OldestPersistAgeSeconds: oldest, ActiveWriters: s.activeWriters, WriterConcurrency: s.options.Writers, StorageErrorsTotal: s.storageErrors}
}

// Options returns the immutable runtime options used when this service was
// constructed. Settings edits do not resize a running service.
func (s *IngestService) Options() IngestOptions {
	if s == nil {
		return DefaultIngestOptions()
	}
	return s.options
}

// PoolMetrics joins the local backend observations with volatile ingest state
// without exposing the backend's physical root path.
func (s *IngestService) PoolMetrics() PoolSnapshot {
	state := s.Snapshot()
	var pool PoolSnapshot
	if backend, ok := s.store.StorageBackend.(poolMetricsWithIngest); ok {
		pool = backend.poolMetrics(state)
	} else {
		pool = s.store.StorageBackend.PoolMetrics()
	}
	pool.Buffer = PoolBuffer{UsedBytes: state.BufferUsedBytes, CapacityBytes: state.BufferCapacityBytes, ReservedBytes: state.ReservedBytes, PerRecordingCapacityBytes: state.PerRecordingCapacityBytes}
	pool.Queue = PoolQueue{Objects: state.QueueObjects, Bytes: state.QueueBytes, OldestAgeSeconds: state.OldestPersistAgeSeconds}
	pool.Writers = PoolWriters{Active: state.ActiveWriters, Limit: state.WriterConcurrency}
	return pool
}

func (s *IngestService) sampleLoop() {
	defer close(s.samplerDone)
	ticker := time.NewTicker(s.options.SampleInterval)
	defer ticker.Stop()
	s.runSampler(ticker.C, s.samplerStop)
}

// runSampler is separated from ticker construction so tests can drive exact
// sample timestamps without waiting five seconds. Production owns this loop;
// API reads do not determine whether the history continues to advance.
func (s *IngestService) runSampler(ticks <-chan time.Time, stop <-chan struct{}) {
	for {
		select {
		case now := <-ticks:
			state := s.Snapshot()
			if backend, ok := s.store.StorageBackend.(interface {
				samplePool(IngestSnapshot, time.Time)
			}); ok {
				backend.samplePool(state, now)
			}
		case <-stop:
			return
		}
	}
}

func (s *IngestService) signalLocked() { close(s.changed); s.changed = make(chan struct{}) }

func (s *IngestService) refreshOldestQueuedLocked() {
	s.oldestQueued = time.Time{}
	for _, queuedAt := range s.queueTimes {
		if s.oldestQueued.IsZero() || queuedAt.Before(s.oldestQueued) {
			s.oldestQueued = queuedAt
		}
	}
}

// Close stops admission and drains every accepted object. Calls may be
// repeated after a deadline to continue waiting for a blocked local writer.
func (s *IngestService) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.signalLocked()
		s.mu.Unlock()
		close(s.samplerStop)
		go func() {
			s.waitForSubmits()
			close(s.jobs)
			s.workers.Wait()
			<-s.samplerDone
			close(s.closeDone)
		}()
	})
	select {
	case <-s.closeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *IngestService) waitForSubmits() {
	for {
		s.mu.Lock()
		if s.activeSubmits == 0 {
			s.mu.Unlock()
			return
		}
		changed := s.changed
		s.mu.Unlock()
		<-changed
	}
}

// Done is closed after the service has stopped admission, drained accepted
// jobs, and joined its writer and sampler goroutines. It can be observed by
// coordinators that must outlive a caller's Close deadline.
func (s *IngestService) Done() <-chan struct{} { return s.closeDone }
