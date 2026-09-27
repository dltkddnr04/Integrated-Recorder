package acquire

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
	"github.com/dltkddnr04/integrated-recorder/internal/domain"
	"github.com/dltkddnr04/integrated-recorder/internal/hls"
)

const (
	minSegmentWorkers = 1
	maxSegmentWorkers = 4
	maxSegmentTasks   = 128
)

type segmentTaskKey struct {
	epoch    uint64
	sequence uint64
}

type segmentTaskState uint8

const (
	segmentTaskQueued segmentTaskState = iota
	segmentTaskInFlight
	segmentTaskRefreshing
	segmentTaskRetryWait
	segmentTaskAwaitManifest
	segmentTaskGapCommitting
)

type segmentTask struct {
	key                segmentTaskKey
	source             hls.MediaSegment
	ordinal            uint64
	available          bool
	observed           bool
	observedGeneration uint64
	requiredGeneration uint64
	awaitingManifest   bool
	attempt            int
	refreshCycles      int
	state              segmentTaskState
	timerToken         uint64
	retryCancel        context.CancelFunc
}

type initFlight struct {
	done chan struct{}
	id   string
	err  error
}

type epochMarker struct {
	ordinal             uint64
	sourceDiscontinuity bool
}

// segmentScheduler belongs to one recording worker. Discovery is serialized by
// its caller; only bounded fetch execution and retry timing are concurrent.
type segmentScheduler struct {
	manager *Manager
	e       *entry
	ctx     context.Context
	cancel  context.CancelFunc

	mu                       sync.Mutex
	commitMu                 sync.Mutex
	changed                  chan struct{}
	tasks                    map[segmentTaskKey]*segmentTask
	ready                    []*segmentTask
	workers                  int
	busy                     int
	closed                   bool
	fatal                    error
	ending                   bool
	endingGeneration         uint64
	latestManifestGeneration uint64
	manifestGenerationSet    bool
	directMode               bool
	wg                       sync.WaitGroup
	init                     map[string]*initFlight
	next                     uint64
	epochMarkers             map[uint64]epochMarker
	manifestWake             chan struct{}
}

func newSegmentScheduler(parent context.Context, manager *Manager, e *entry) (*segmentScheduler, error) {
	next, err := nextArchiveOrdinal(e)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	s := &segmentScheduler{
		manager: manager, e: e, ctx: ctx, cancel: cancel,
		changed: make(chan struct{}), tasks: make(map[segmentTaskKey]*segmentTask),
		init: make(map[string]*initFlight), next: next, epochMarkers: make(map[uint64]epochMarker), manifestWake: make(chan struct{}, 1),
	}
	s.mu.Lock()
	for worker := 0; worker < minSegmentWorkers; worker++ {
		s.spawnLocked(false)
	}
	s.mu.Unlock()
	e.mu.Lock()
	e.scheduler = s
	e.mu.Unlock()
	return s, nil
}

func (s *segmentScheduler) signalLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *segmentScheduler) spawnLocked(burst bool) {
	if s.closed || s.workers >= maxSegmentWorkers {
		return
	}
	s.workers++
	s.wg.Add(1)
	go s.worker(burst)
}

func (s *segmentScheduler) growIfSaturatedLocked() {
	if len(s.ready) > 0 && s.busy == s.workers && s.workers < maxSegmentWorkers {
		s.spawnLocked(true)
	}
}

func (s *segmentScheduler) discover(ctx context.Context, epoch uint64, playlist hls.MediaPlaylist) error {
	_, generation := currentMediaVersion(s.e)
	_, err := s.discoverAtGeneration(ctx, epoch, playlist, generation)
	return err
}

func (s *segmentScheduler) discoverAtGeneration(ctx context.Context, epoch uint64, playlist hls.MediaPlaylist, generation uint64) (bool, error) {
	for index, source := range playlist.Segments {
		if !s.manifestGenerationCurrent(generation) {
			return false, nil
		}
		if source.Gap || s.manager.hasSequence(s.e, epoch, source.Sequence) || gapCoversEpoch(s.recordingSnapshot(), "main", epoch, source.Sequence) {
			continue
		}
		key := segmentTaskKey{epoch: epoch, sequence: source.Sequence}
		for {
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				return false, errors.New("segment scheduler is closed")
			}
			if !s.manifestGenerationCurrentLocked(generation) {
				s.mu.Unlock()
				return false, nil
			}
			if existing := s.tasks[key]; existing != nil {
				if existing.state == segmentTaskGapCommitting {
					s.mu.Unlock()
					break
				}
				existing.source = source // preserve latest signed fetch URI for a later retry
				existing.available = true
				existing.observed = true
				existing.observedGeneration = generation
				if existing.awaitingManifest && generation >= existing.requiredGeneration {
					existing.awaitingManifest = false
					existing.state = segmentTaskQueued
					s.ready = append(s.ready, existing)
					s.growIfSaturatedLocked()
				} else if existing.state == segmentTaskRetryWait {
					// A re-observed segment remains a single task; its timer stays
					// authoritative and no second fetch is admitted.
				}
				s.signalLocked()
				s.mu.Unlock()
				break
			}
			// Capture commits root metadata before removing the task map entry.
			// Recheck under the same lock used for task identity allocation to
			// prevent a poll that passed the optimistic check from re-enqueueing
			// an already captured segment. A gap is similarly protected by the
			// gap-committing state until its metadata write succeeds.
			if s.manager.hasSequence(s.e, epoch, source.Sequence) || gapCoversEpoch(s.recordingSnapshot(), "main", epoch, source.Sequence) {
				s.mu.Unlock()
				break
			}
			if len(s.tasks) >= maxSegmentTasks {
				changed := s.changed
				s.mu.Unlock()
				select {
				case <-ctx.Done():
					if err := s.manager.markRemainingPending(s.e, epoch, playlist.Segments[index:], ctx.Err()); err != nil {
						return false, err
					}
					return false, ctx.Err()
				case <-s.ctx.Done():
					if err := s.manager.markRemainingPending(s.e, epoch, playlist.Segments[index:], s.ctx.Err()); err != nil {
						return false, err
					}
					return false, s.ctx.Err()
				case <-changed:
				}
				continue
			}
			ordinal := s.next
			if ordinal == 0 || ordinal == ^uint64(0) {
				s.mu.Unlock()
				return false, errors.New("archive ordinal overflow")
			}
			s.next++
			task := &segmentTask{key: key, source: source, ordinal: ordinal, available: true, observed: true, observedGeneration: generation, state: segmentTaskQueued}
			s.tasks[key] = task
			s.ready = append(s.ready, task)
			s.growIfSaturatedLocked()
			s.signalLocked()
			s.mu.Unlock()
			break
		}
	}
	return s.manifestGenerationCurrent(generation), nil
}

func (s *segmentScheduler) recordingSnapshot() *domain.Recording {
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	return clone(s.e.recording)
}

// manifestGenerationCurrent rejects bodies fetched from an older media source.
// The entry check is repeated while holding the scheduler lock at mutation
// sites; no lock is held across queue backpressure or network work.
func (s *segmentScheduler) manifestGenerationCurrent(generation uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manifestGenerationCurrentLocked(generation)
}

func (s *segmentScheduler) manifestGenerationCurrentLocked(generation uint64) bool {
	s.e.mu.Lock()
	current := s.e.mediaGeneration
	s.e.mu.Unlock()
	if current != generation || (s.manifestGenerationSet && generation < s.latestManifestGeneration) {
		return false
	}
	if !s.manifestGenerationSet || generation > s.latestManifestGeneration {
		s.latestManifestGeneration = generation
		s.manifestGenerationSet = true
		if s.ending && generation > s.endingGeneration {
			s.ending = false
			s.signalLocked()
		}
	}
	return true
}

// noteMediaGeneration runs immediately after a refreshed source is committed.
// It reopens a playlist that ended on the prior source generation.
func (s *segmentScheduler) noteMediaGeneration(generation uint64) {
	s.mu.Lock()
	reopened := false
	if !s.manifestGenerationSet || generation > s.latestManifestGeneration {
		s.latestManifestGeneration = generation
		s.manifestGenerationSet = true
		if s.ending && generation > s.endingGeneration {
			s.ending = false
			reopened = true
			s.signalLocked()
		}
	}
	s.mu.Unlock()
	if reopened {
		s.wakeManifestPoll()
	}
}

func (s *segmentScheduler) observe(epoch uint64, playlist hls.MediaPlaylist) error {
	_, generation := currentMediaVersion(s.e)
	return s.observeAtGeneration(epoch, playlist, generation)
}

func (s *segmentScheduler) observeAtGeneration(epoch uint64, playlist hls.MediaPlaylist, generation uint64) error {
	present := make(map[uint64]bool, len(playlist.Segments))
	gaps := make(map[uint64]bool)
	var max uint64
	for i, source := range playlist.Segments {
		present[source.Sequence] = true
		if source.Gap {
			gaps[source.Sequence] = true
		}
		if i == 0 || source.Sequence > max {
			max = source.Sequence
		}
	}
	var expired []*segmentTask
	s.mu.Lock()
	if !s.manifestGenerationCurrentLocked(generation) {
		s.mu.Unlock()
		return nil
	}
	ending := s.ending
	for key, task := range s.tasks {
		if key.epoch > epoch {
			continue
		}
		available := key.epoch == epoch && present[key.sequence] && !gaps[key.sequence]
		if !available && (key.epoch < epoch || max > key.sequence || (key.epoch == epoch && gaps[key.sequence])) {
			task.available = false
			if task.state == segmentTaskRetryWait || task.state == segmentTaskQueued || task.state == segmentTaskAwaitManifest {
				task.timerToken++
				task.state = segmentTaskGapCommitting
				task.awaitingManifest = false
				expired = append(expired, task)
			}
		} else if available {
			task.available = true
		}
	}
	for _, task := range expired {
		if task.retryCancel != nil {
			task.retryCancel()
			task.retryCancel = nil
		}
	}
	if len(expired) > 0 {
		s.ready = s.ready[:0]
		for _, task := range s.tasks {
			if task.state == segmentTaskQueued {
				s.ready = append(s.ready, task)
			}
		}
		sort.Slice(s.ready, func(i, j int) bool { return s.ready[i].ordinal < s.ready[j].ordinal })
		s.signalLocked()
	}
	s.mu.Unlock()
	for _, task := range expired {
		reason := "media left the live window after retries"
		if ending {
			reason = "stream ended before media could be captured"
		}
		if err := s.persistGap(task, reason); err != nil {
			return err
		}
	}
	return nil
}

func (s *segmentScheduler) protectedSequences(epoch uint64) map[uint64]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	protected := make(map[uint64]bool)
	for key := range s.tasks {
		if key.epoch == epoch {
			protected[key.sequence] = true
		}
	}
	return protected
}

func (s *segmentScheduler) protectedByEpoch() map[uint64]map[uint64]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	protected := make(map[uint64]map[uint64]bool)
	for key := range s.tasks {
		if protected[key.epoch] == nil {
			protected[key.epoch] = make(map[uint64]bool)
		}
		protected[key.epoch][key.sequence] = true
	}
	return protected
}

func activeScheduler(e *entry) *segmentScheduler {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.scheduler
}

func (s *segmentScheduler) endList(generation uint64) bool {
	s.mu.Lock()
	if !s.manifestGenerationCurrentLocked(generation) {
		s.mu.Unlock()
		return false
	}
	s.ending = true
	s.endingGeneration = generation
	// A task waiting for a refreshed playlist cannot be retried after ENDLIST.
	// It is terminally pending; the drain then lets the recording finalize it.
	var expired []*segmentTask
	for _, task := range s.tasks {
		if task.state == segmentTaskAwaitManifest {
			if task.available && task.observed && task.observedGeneration >= task.requiredGeneration {
				task.awaitingManifest = false
				task.state = segmentTaskQueued
				s.ready = append(s.ready, task)
				s.growIfSaturatedLocked()
				continue
			}
			task.available = false
			task.timerToken++
			task.awaitingManifest = false
			task.state = segmentTaskGapCommitting
			expired = append(expired, task)
		}
	}
	for _, task := range expired {
		if task.retryCancel != nil {
			task.retryCancel()
			task.retryCancel = nil
		}
	}
	s.signalLocked()
	s.mu.Unlock()
	for _, task := range expired {
		if err := s.persistGap(task, "stream ended before refreshed media could be captured"); err != nil {
			s.failFatal(err)
			return true
		}
	}
	return true
}

func (s *segmentScheduler) wakeManifestPoll() {
	select {
	case s.manifestWake <- struct{}{}:
	default:
	}
}

func (s *segmentScheduler) worker(burst bool) {
	defer s.wg.Done()
	for {
		s.mu.Lock()
		for len(s.ready) == 0 && !s.closed && s.ctx.Err() == nil {
			if burst {
				s.workers--
				s.signalLocked()
				s.mu.Unlock()
				return
			}
			changed := s.changed
			s.mu.Unlock()
			select {
			case <-s.ctx.Done():
			case <-changed:
			}
			s.mu.Lock()
		}
		if s.closed || s.ctx.Err() != nil {
			s.workers--
			s.signalLocked()
			s.mu.Unlock()
			return
		}
		task := s.ready[0]
		s.ready[0] = nil
		s.ready = s.ready[1:]
		if task.state != segmentTaskQueued || s.tasks[task.key] != task {
			s.mu.Unlock()
			continue
		}
		task.state = segmentTaskInFlight
		s.busy++
		s.growIfSaturatedLocked()
		s.signalLocked()
		s.mu.Unlock()

		err := s.acquire(task)

		s.mu.Lock()
		s.busy--
		if s.tasks[task.key] != task {
			s.signalLocked()
			s.mu.Unlock()
			continue
		}
		if err == nil {
			delete(s.tasks, task.key)
			s.signalLocked()
			s.mu.Unlock()
			continue
		}
		if s.ctx.Err() != nil || errors.Is(err, context.Canceled) {
			s.signalLocked()
			s.mu.Unlock()
			continue
		}
		var trigger *segmentRefreshTrigger
		if errors.As(err, &trigger) {
			// acquire() performs the coordinated refresh and returns the trigger
			// only when refresh is not possible or has been exhausted.
			s.mu.Unlock()
			s.failFatal(fmt.Errorf("media source refresh failed"))
			return
		}
		if errors.Is(err, errAwaitManifest) {
			s.mu.Unlock()
			s.awaitManifest(task)
			continue
		}
		var fetchErr *FetchError
		if !errors.As(err, &fetchErr) {
			s.mu.Unlock()
			s.failFatal(err)
			return
		}
		if !task.available {
			task.state = segmentTaskGapCommitting
			s.signalLocked()
			s.mu.Unlock()
			if gapErr := s.persistGap(task, "media left the live window after retries"); gapErr != nil {
				s.failFatal(gapErr)
			}
			continue
		}
		if persistErr := s.manager.markPending(s.e, task.key.epoch, task.key.sequence, err); persistErr != nil {
			s.mu.Unlock()
			s.failFatal(persistErr)
			return
		}
		task.attempt++
		if s.directMode {
			delete(s.tasks, task.key)
			s.signalLocked()
			s.mu.Unlock()
			continue
		}
		if s.ending && task.attempt >= segmentAttempts {
			task.state = segmentTaskGapCommitting
			s.signalLocked()
			s.mu.Unlock()
			if gapErr := s.persistGap(task, "stream ended after segment retries"); gapErr != nil {
				s.failFatal(gapErr)
				return
			}
			continue
		}
		task.state = segmentTaskRetryWait
		task.timerToken++
		token := task.timerToken
		delay := retryDelay(task.attempt - 1)
		timerCtx, timerCancel := context.WithCancel(s.ctx)
		task.retryCancel = timerCancel
		s.wg.Add(1)
		s.signalLocked()
		s.mu.Unlock()
		go s.retryTimer(timerCtx, timerCancel, task, token, delay)
	}
}

// awaitManifest atomically transitions a refreshed task to waiting for the
// next playlist, unless ENDLIST has already closed admission. In that case
// the task is terminally recorded as a gap instead of leaving drain blocked.
func (s *segmentScheduler) awaitManifest(task *segmentTask) {
	s.mu.Lock()
	if s.tasks[task.key] != task {
		s.mu.Unlock()
		return
	}
	// Refresh commits the source before it can update scheduler state. Read the
	// entry generation here as well so ENDLIST from the old source cannot win
	// that narrow interval and turn a recoverable task into a gap.
	_, currentGeneration := currentMediaVersion(s.e)
	if currentGeneration > s.latestManifestGeneration {
		s.latestManifestGeneration = currentGeneration
		s.manifestGenerationSet = true
		if s.ending && currentGeneration > s.endingGeneration {
			s.ending = false
		}
	}
	if task.available && task.observed && task.observedGeneration >= task.requiredGeneration {
		task.awaitingManifest = false
		task.state = segmentTaskQueued
		s.ready = append(s.ready, task)
		s.growIfSaturatedLocked()
		s.signalLocked()
		s.mu.Unlock()
		return
	}
	if !s.ending {
		task.state = segmentTaskAwaitManifest
		task.awaitingManifest = true
		s.signalLocked()
		s.mu.Unlock()
		s.wakeManifestPoll()
		return
	}
	task.state = segmentTaskGapCommitting
	s.signalLocked()
	s.mu.Unlock()
	if err := s.persistGap(task, "stream ended before refreshed media could be captured"); err != nil {
		s.failFatal(err)
	}
}

func (s *segmentScheduler) persistGap(task *segmentTask, reason string) error {
	if err := s.manager.markGap(s.e, task.key.epoch, task.key.sequence, reason); err != nil {
		return err
	}
	s.mu.Lock()
	if s.tasks[task.key] == task {
		delete(s.tasks, task.key)
		s.signalLocked()
	}
	s.mu.Unlock()
	return nil
}

func retryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 7 {
		attempt = 7
	}
	return time.Duration(150+attempt*250) * time.Millisecond
}

func (s *segmentScheduler) retryTimer(ctx context.Context, cancel context.CancelFunc, task *segmentTask, token uint64, delay time.Duration) {
	defer s.wg.Done()
	defer cancel()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil || s.tasks[task.key] != task || task.timerToken != token || !task.available {
		return
	}
	task.retryCancel = nil
	task.state = segmentTaskQueued
	s.ready = append(s.ready, task)
	s.growIfSaturatedLocked()
	s.signalLocked()
}

func (s *segmentScheduler) acquire(task *segmentTask) error {
	s.mu.Lock()
	s.e.mu.Lock()
	generation := s.e.mediaGeneration
	if !task.observed || task.observedGeneration != generation || task.observedGeneration < task.requiredGeneration {
		if generation > task.requiredGeneration {
			task.requiredGeneration = generation
		}
		task.available = false
		task.observed = false
		s.signalLocked()
		s.e.mu.Unlock()
		s.mu.Unlock()
		return errAwaitManifest
	}
	media := cloneMediaSource(s.e.media)
	source := task.source
	if source.Init != nil {
		initCopy := *source.Init
		if initCopy.ByteRange != nil {
			rangeCopy := *initCopy.ByteRange
			initCopy.ByteRange = &rangeCopy
		}
		source.Init = &initCopy
	}
	epoch, ordinal := task.key.epoch, task.ordinal
	firstInEpoch := false // synthetic epoch markers are assigned at metadata commit order
	refreshCycles := task.refreshCycles
	s.e.mu.Unlock()
	s.mu.Unlock()
	initID := ""
	if source.Init != nil {
		var err error
		initID, err = s.acquireInit(source, epoch, media, generation)
		if err != nil {
			if errors.Is(err, errStaleMediaGeneration) {
				return s.awaitCurrentGeneration(task)
			}
			return s.handleFetchFailure(task, media, generation, refreshCycles, err)
		}
	}
	segment, err := s.manager.acquireMediaOnce(s.ctx, s.e, source, epoch, ordinal, initID, firstInEpoch, media, generation)
	if err != nil {
		if errors.Is(err, errStaleMediaGeneration) {
			return s.awaitCurrentGeneration(task)
		}
		return s.handleFetchFailure(task, media, generation, refreshCycles, err)
	}
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	sourceDiscontinuity := source.Discontinuity
	root := s.recordingSnapshot()
	var previousUpdate *domain.Segment
	var plannedMarker *epochMarker
	if segment.SourceEpoch > 0 {
		marker, exists := s.epochMarkers[segment.SourceEpoch]
		if !exists {
			track := root.Tracks["main"]
			if track != nil {
				for i := range track.Segments {
					candidate := track.Segments[i]
					if candidate.SourceEpoch == segment.SourceEpoch && (marker.ordinal == 0 || candidate.ArchiveOrdinal < marker.ordinal) {
						marker = epochMarker{ordinal: candidate.ArchiveOrdinal, sourceDiscontinuity: candidate.Discontinuity}
						exists = true
					}
				}
			}
		}
		if !exists || segment.ArchiveOrdinal < marker.ordinal {
			if exists {
				track := root.Tracks["main"]
				for i := range track.Segments {
					candidate := track.Segments[i]
					if candidate.SourceEpoch == segment.SourceEpoch && candidate.ArchiveOrdinal == marker.ordinal {
						candidate.Discontinuity = marker.sourceDiscontinuity
						previousUpdate = &candidate
						break
					}
				}
			}
			segment.Discontinuity = true
			planned := epochMarker{ordinal: segment.ArchiveOrdinal, sourceDiscontinuity: sourceDiscontinuity}
			plannedMarker = &planned
		} else if plannedMarker == nil {
			planned := marker
			plannedMarker = &planned
		}
	}
	// Sidecars, including a moved synthetic epoch marker, are durable before
	// the root metadata references their final projection.
	if err = s.manager.store.SaveSidecar(recordingID(s.e), segment.StoragePath, segment); err != nil {
		return err
	}
	if previousUpdate != nil {
		if err = s.manager.store.SaveSidecar(recordingID(s.e), previousUpdate.StoragePath, *previousUpdate); err != nil {
			return err
		}
	}
	if err = s.manager.update(s.e, func(r *domain.Recording) error {
		t := r.Tracks["main"]
		for _, existing := range t.Segments {
			if existing.SourceEpoch == segment.SourceEpoch && existing.Sequence == segment.Sequence {
				return nil
			}
		}
		if previousUpdate != nil {
			for i := range t.Segments {
				if t.Segments[i].ID == previousUpdate.ID {
					t.Segments[i].Discontinuity = previousUpdate.Discontinuity
					break
				}
			}
		}
		t.Segments = append(t.Segments, segment)
		sort.Slice(t.Segments, func(i, j int) bool {
			if t.Segments[i].ArchiveOrdinal != t.Segments[j].ArchiveOrdinal {
				return t.Segments[i].ArchiveOrdinal < t.Segments[j].ArchiveOrdinal
			}
			if t.Segments[i].SourceEpoch != t.Segments[j].SourceEpoch {
				return t.Segments[i].SourceEpoch < t.Segments[j].SourceEpoch
			}
			return t.Segments[i].Sequence < t.Segments[j].Sequence
		})
		if segment.ArchiveOrdinal >= t.NextArchiveOrdinal {
			if segment.ArchiveOrdinal == ^uint64(0) {
				return errors.New("archive ordinal overflow")
			}
			t.NextArchiveOrdinal = segment.ArchiveOrdinal + 1
		}
		t.PendingSegments = removePending(t.PendingSegments, segment.SourceEpoch, segment.Sequence)
		if segment.SourceEpoch == 0 {
			t.PendingSequences = removeSequence(t.PendingSequences, segment.Sequence)
		}
		r.LastError = ""
		return nil
	}); err != nil {
		return err
	}
	if plannedMarker != nil && segment.SourceEpoch > 0 {
		s.epochMarkers[segment.SourceEpoch] = *plannedMarker
	}
	return nil
}

var errAwaitManifest = errors.New("segment retry awaits refreshed manifest")

// errStaleMediaGeneration indicates that a scheduler-owned source URI no
// longer belongs to the recording's current media source generation.
var errStaleMediaGeneration = errors.New("media source generation changed before fetch")

// awaitCurrentGeneration records the generation that invalidated a fetch and
// keeps any concurrently observed manifest from that generation or newer.
// Lock order matches discovery and admission: scheduler, then entry.
func (s *segmentScheduler) awaitCurrentGeneration(task *segmentTask) error {
	s.mu.Lock()
	if s.tasks[task.key] == task {
		s.e.mu.Lock()
		generation := s.e.mediaGeneration
		if generation > task.requiredGeneration {
			task.requiredGeneration = generation
		}
		if !task.available || !task.observed || task.observedGeneration < generation {
			task.available = false
			task.observed = false
		}
		s.e.mu.Unlock()
		s.signalLocked()
	}
	s.mu.Unlock()
	return errAwaitManifest
}

func (s *segmentScheduler) handleFetchFailure(task *segmentTask, media adapterproto.MediaSource, generation uint64, refreshCycles int, err error) error {
	if !s.manager.shouldRefresh(media, err) {
		return err
	}
	if refreshCycles >= maxRefreshCycles {
		return makeSegmentRefreshTrigger(task.key.epoch, task.key.sequence, err)
	}
	if !s.markRefreshing(task) {
		return errAwaitManifest
	}
	_, nextGeneration, refreshErr := s.manager.refreshMediaAtGeneration(s.ctx, s.e, media, generation)
	if refreshErr != nil {
		return makeSegmentRefreshTrigger(task.key.epoch, task.key.sequence, err)
	}
	s.mu.Lock()
	if s.tasks[task.key] == task {
		if nextGeneration > task.requiredGeneration {
			task.requiredGeneration = nextGeneration
		}
		if nextGeneration > generation {
			task.refreshCycles++
		}
	}
	s.mu.Unlock()
	// Resolve a current segment URI from the refreshed playlist before retrying;
	// awaitManifest wakes the poller only after the task is no longer in its
	// protected Refreshing state.
	return errAwaitManifest
}

func (s *segmentScheduler) markRefreshing(task *segmentTask) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.tasks[task.key] != task {
		return false
	}
	task.state = segmentTaskRefreshing
	task.awaitingManifest = false
	s.signalLocked()
	return true
}

func (s *segmentScheduler) acquireInit(segment hls.MediaSegment, epoch uint64, media adapterproto.MediaSource, generation uint64) (string, error) {
	source := *segment.Init
	key := initIdentity(source, epoch, segment.DiscontinuitySequence)
	s.mu.Lock()
	if flight := s.init[key]; flight != nil {
		done := flight.done
		s.mu.Unlock()
		select {
		case <-s.ctx.Done():
			return "", s.ctx.Err()
		case <-done:
			return flight.id, flight.err
		}
	}
	flight := &initFlight{done: make(chan struct{})}
	s.init[key] = flight
	s.mu.Unlock()

	id, err := s.manager.acquireInitOnce(s.ctx, s.e, source, epoch, segment.DiscontinuitySequence, media, generation)
	s.mu.Lock()
	flight.id, flight.err = id, err
	delete(s.init, key)
	close(flight.done)
	s.mu.Unlock()
	return id, err
}

func initIdentity(source hls.Map, epoch, discontinuity uint64) string {
	key := fmt.Sprintf("%s|epoch=%d|discontinuity=%d", source.URI, epoch, discontinuity)
	if source.ByteRange != nil {
		key += fmt.Sprintf("|%d@%d", source.ByteRange.Length, source.ByteRange.Offset)
	}
	return key
}

func (s *segmentScheduler) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fatal
}

func (s *segmentScheduler) failFatal(err error) {
	s.mu.Lock()
	if s.fatal == nil {
		s.fatal = err
	}
	s.closed = true
	s.signalLocked()
	s.mu.Unlock()
	// Preserve the manager's normal interrupted-state/error path.
	s.e.mu.Lock()
	cancel := s.e.cancel
	s.e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.cancel()
}

func (s *segmentScheduler) drain(ctx context.Context) error {
	for {
		s.mu.Lock()
		if s.fatal != nil {
			err := s.fatal
			s.mu.Unlock()
			return err
		}
		if len(s.tasks) == 0 {
			s.mu.Unlock()
			return nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.ctx.Done():
			if err := s.failure(); err != nil {
				return err
			}
			return s.ctx.Err()
		case <-changed:
		}
	}
}

// drainEndList drains only while ENDLIST still belongs to this media source
// generation. A concurrent refresh reopens admission and lets the poll loop
// fetch a manifest from the new source instead of finalizing the recording.
func (s *segmentScheduler) drainEndList(ctx context.Context, generation uint64) (bool, error) {
	for {
		s.mu.Lock()
		if s.fatal != nil {
			err := s.fatal
			s.mu.Unlock()
			return false, err
		}
		if !s.ending || s.endingGeneration != generation || !s.manifestGenerationCurrentLocked(generation) {
			s.mu.Unlock()
			return false, nil
		}
		if len(s.tasks) == 0 {
			s.mu.Unlock()
			return true, nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-s.ctx.Done():
			if err := s.failure(); err != nil {
				return false, err
			}
			return false, s.ctx.Err()
		case <-changed:
		}
	}
}

func (s *segmentScheduler) close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.signalLocked()
	}
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
	var pending []segmentTaskKey
	s.mu.Lock()
	for key := range s.tasks {
		pending = append(pending, key)
	}
	s.tasks = make(map[segmentTaskKey]*segmentTask)
	s.ready = nil
	s.signalLocked()
	s.mu.Unlock()
	for _, key := range pending {
		if s.manager.hasSequence(s.e, key.epoch, key.sequence) {
			continue
		}
		if err := s.manager.markPending(s.e, key.epoch, key.sequence, context.Canceled); err != nil {
			s.failFatal(err)
			return err
		}
	}
	s.e.mu.Lock()
	if s.e.scheduler == s {
		s.e.scheduler = nil
	}
	s.e.mu.Unlock()
	return s.failure()
}
