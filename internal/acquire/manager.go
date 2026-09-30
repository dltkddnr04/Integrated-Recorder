// Package acquire owns recording lifecycles and the shared HLS polling and
// original-byte acquisition path.
package acquire

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
	"github.com/dltkddnr04/integrated-recorder/internal/domain"
	"github.com/dltkddnr04/integrated-recorder/internal/network"
	"github.com/dltkddnr04/integrated-recorder/internal/storage"
)

type Resolver interface {
	Resolve(context.Context, string, json.RawMessage, *adapterproto.ResourceRef) (adapterproto.MediaSource, error)
}

// Refresher is an optional adapter operation. It is deliberately separate
// from Resolver so existing test and embedding implementations remain valid.
type Refresher interface {
	Refresh(context.Context, string, *adapterproto.ResourceRef, adapterproto.MediaSource) (adapterproto.MediaSource, error)
}

// RefreshPreparer separates validation from adapter-owned state mutation so
// Core can apply its network policy before a refresh transaction is committed.
type RefreshPreparer interface {
	PrepareRefresh(context.Context, string, *adapterproto.ResourceRef, adapterproto.MediaSource) (adapterproto.MediaSource, func() error, error)
}

// MetadataPreparer is optional so protocol-v1 adapters without metadata
// support keep working without a polling loop. The returned state commit is
// applied only after Core accepts the observation for the current media
// generation and durably stores any canonical revision.
type MetadataPreparer interface {
	PrepareMetadata(context.Context, string, *adapterproto.ResourceRef, adapterproto.MediaSource) (adapterproto.MetadataResult, func() error, bool, error)
}

type SourceValidator func(context.Context, string) error

type entry struct {
	// persistMu serializes durable root-document writes and archive deletion.
	// Callers always acquire it before mu, and never hold mu across storage I/O.
	persistMu       sync.Mutex
	mu              sync.Mutex
	recording       *domain.Recording
	deleted         bool
	cancel          context.CancelFunc
	done            chan struct{}
	media           adapterproto.MediaSource
	mediaGeneration uint64
	refreshGate     chan struct{}
	scheduler       *segmentScheduler
	adapterID       string
	resource        *adapterproto.ResourceRef
	terminalErr     error
}

type Manager struct {
	store    *storage.Store
	ingest   *storage.IngestService
	client   *http.Client
	resolver Resolver
	validate SourceValidator
	mu       sync.RWMutex
	entries  map[string]*entry
	// freshGeneration leaves existing archive documents read-only and does
	// not take ownership of them. It is used for a candidate Engine generation
	// that must not recover/interrupt work owned by an older Engine.
	freshGeneration bool
	closed          bool
	starts          sync.WaitGroup
	startsWait      sync.Once
	startsDone      chan struct{}

	// fetchBoundaryHook is a deterministic test seam for scheduler-owned
	// generation checks. It is configured before recording goroutines start.
	fetchBoundaryHook func(uri string)
	// storageWriteHook is a deterministic test seam used to block asynchronous
	// canonical writes without coupling HTTP body reads to filesystem latency.
	storageWriteHook func()
	// storageSnapshotWriteHook is a deterministic test seam for manifest
	// snapshot persistence; production leaves it nil.
	storageSnapshotWriteHook func()
	// storageMetadataWriteHook blocks queued root metadata writes in deterministic
	// storage-delay tests. Production leaves it nil.
	storageMetadataWriteHook func()
	// storageWriteFailureHook injects deterministic persistence failures for
	// retry/no-redownload tests. Production leaves it nil.
	storageWriteFailureHook func() error
	// Metadata polling is fixed policy in production; the interval seam keeps
	// lifecycle tests deterministic without waiting for the production cadence.
	metadataPollInterval time.Duration
	// metadataClock is a deterministic test seam. Production leaves it nil.
	metadataClock func() time.Time
}

// OwnedRecordingState is a bounded, non-payload runtime snapshot. It avoids
// cloning full canonical segment histories for process inventory checks.
type OwnedRecordingState struct {
	ID        string
	State     domain.RecordingState
	StartedAt time.Time
}

// StartupMode makes archive ownership behavior explicit when constructing a
// recorder manager. RecoverExisting preserves the established single-process
// startup recovery semantics. FreshGeneration reads no existing archives into
// the worker registry and performs no startup recovery writes.
type StartupMode uint8

const (
	RecoverExisting StartupMode = iota
	FreshGeneration
)

var errManagerClosed = errors.New("recording manager is closed")

// ErrActiveRecording is returned when a management operation attempts to
// delete a recording whose acquisition worker is still active.
var ErrActiveRecording = errors.New("active recording cannot be deleted")

// ErrListLimit is returned when a bounded management snapshot would exceed
// the caller's maximum recording count.
var ErrListLimit = errors.New("recording list exceeds management limit")

func NewManager(store *storage.Store, client *http.Client, resolver Resolver, validate SourceValidator) (*Manager, error) {
	return NewManagerWithMode(store, client, resolver, validate, RecoverExisting)
}

// NewManagerWithMode constructs a manager using an explicit archive recovery
// policy. Fresh generations may observe prior archives through read-only
// Get/List projections but own only recordings they start themselves.
func NewManagerWithMode(store *storage.Store, client *http.Client, resolver Resolver, validate SourceValidator, mode StartupMode) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("storage is required")
	}
	if mode != RecoverExisting && mode != FreshGeneration {
		return nil, fmt.Errorf("invalid recording manager startup mode")
	}
	if client == nil {
		client = network.NewPublicHTTPClient(25 * time.Second)
	}
	if validate == nil {
		validate = network.ValidatePublicURL
	}
	var loaded []*domain.Recording
	var err error
	if mode == RecoverExisting {
		loaded, err = store.LoadAll()
		if err != nil {
			return nil, err
		}
	}
	ingest, err := store.IngestService()
	if err != nil {
		return nil, err
	}
	if resolver == nil {
		resolver = unavailableResolver{}
	}
	m := &Manager{store: store, ingest: ingest, client: client, resolver: resolver, validate: validate, entries: map[string]*entry{}, freshGeneration: mode == FreshGeneration, startsDone: make(chan struct{})}
	for _, recording := range loaded {
		m.entries[recording.ID] = &entry{recording: recording, done: closedChannel()}
	}
	return m, nil
}

func cloneMediaSource(media adapterproto.MediaSource) adapterproto.MediaSource {
	data, err := json.Marshal(media)
	if err != nil {
		return media
	}
	var copy adapterproto.MediaSource
	if err = json.Unmarshal(data, &copy); err != nil {
		return media
	}
	return copy
}

func cloneResourceRef(resource *adapterproto.ResourceRef) *adapterproto.ResourceRef {
	if resource == nil {
		return nil
	}
	return &adapterproto.ResourceRef{Type: resource.Type, ID: resource.ID, Parent: cloneResourceRef(resource.Parent)}
}

func archiveResource(resource *adapterproto.ResourceRef) *domain.ResourceReference {
	if resource == nil {
		return nil
	}
	return &domain.ResourceReference{Type: resource.Type, ID: resource.ID, Parent: archiveResource(resource.Parent)}
}

func archiveProvenance(provenance *adapterproto.AdapterProvenance) *domain.AdapterProvenance {
	if provenance == nil {
		return nil
	}
	return &domain.AdapterProvenance{ID: provenance.ID, Version: provenance.Version, ProtocolVersion: provenance.ProtocolVersion, Fingerprint: provenance.Fingerprint}
}

type unavailableResolver struct{}

func (unavailableResolver) Resolve(context.Context, string, json.RawMessage, *adapterproto.ResourceRef) (adapterproto.MediaSource, error) {
	return adapterproto.MediaSource{}, fmt.Errorf("no adapter resolver is configured")
}

func (m *Manager) Start(ctx context.Context, adapterID string, input json.RawMessage, resource *adapterproto.ResourceRef, title string) (*domain.Recording, error) {
	if err := m.beginStart(); err != nil {
		return nil, err
	}
	defer m.starts.Done()
	if strings.TrimSpace(adapterID) == "" || strings.ContainsAny(adapterID, "/\\") {
		return nil, fmt.Errorf("adapter id is invalid")
	}
	if err := adapterproto.ValidateObject(input); err != nil {
		return nil, err
	}
	if err := adapterproto.ValidateResourceRef(resource); err != nil {
		return nil, fmt.Errorf("invalid resource reference")
	}
	media, err := m.resolver.Resolve(ctx, adapterID, input, resource)
	if err != nil {
		return nil, fmt.Errorf("adapter resolution failed")
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	return m.startResolved(ctx, id, adapterID, media, resource, title, nil)
}

// StartResolved creates a recording from a media source already resolved by
// an adapter workflow. The shared HLS acquisition path is identical to Start.
func (m *Manager) StartResolved(ctx context.Context, adapterID string, media adapterproto.MediaSource, resource *adapterproto.ResourceRef, title string, provenance *adapterproto.AdapterProvenance) (*domain.Recording, error) {
	if err := m.beginStart(); err != nil {
		return nil, err
	}
	defer m.starts.Done()
	id, err := newID()
	if err != nil {
		return nil, err
	}
	return m.startResolved(ctx, id, adapterID, media, resource, title, provenance)
}

// StartResolvedWithID creates a resolved recording with a caller-allocated,
// validated recording ID. This allows a management projection to persist an
// exact cross-reference before canonical creation without adding management
// data to the archive. The acquisition path is otherwise identical to
// StartResolved.
func (m *Manager) StartResolvedWithID(ctx context.Context, id, adapterID string, media adapterproto.MediaSource, resource *adapterproto.ResourceRef, title string, provenance *adapterproto.AdapterProvenance) (*domain.Recording, error) {
	if !validRecordingID(id) {
		return nil, fmt.Errorf("recording id is invalid")
	}
	if err := m.beginStart(); err != nil {
		return nil, err
	}
	defer m.starts.Done()
	return m.startResolved(ctx, id, adapterID, media, resource, title, provenance)
}

func (m *Manager) beginStart() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errManagerClosed
	}
	m.starts.Add(1)
	return nil
}

func (m *Manager) startResolved(ctx context.Context, id, adapterID string, media adapterproto.MediaSource, resource *adapterproto.ResourceRef, title string, provenance *adapterproto.AdapterProvenance) (*domain.Recording, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(adapterID) == "" || strings.ContainsAny(adapterID, "/\\") {
		return nil, fmt.Errorf("adapter id is invalid")
	}
	if err := adapterproto.ValidateResourceRef(resource); err != nil {
		return nil, fmt.Errorf("invalid resource reference")
	}
	var err error
	if err = adapterproto.ValidateMediaSource(media, []string{"hls"}); err != nil {
		return nil, errors.New("adapter returned invalid media source")
	}
	if err = m.validate(ctx, media.ManifestURL); err != nil {
		return nil, fmt.Errorf("invalid resolved media URL")
	}
	now := time.Now().UTC()
	classification := media.SourceURIClassification()
	recording := &domain.Recording{FormatVersion: 1, ID: id, Title: title, AdapterID: adapterID, Adapter: archiveProvenance(provenance), Resource: archiveResource(resource), SourceURIClassification: classification, State: domain.StateRecording, CreatedAt: now, StartedAt: now, Tracks: map[string]*domain.Track{
		"main": {ID: "main", SourcePlaylistURL: media.ManifestURL, NextArchiveOrdinal: 1, Segments: []domain.Segment{}, InitSegments: []domain.Segment{}},
	}}
	if err = m.store.CreateRecording(recording); err != nil {
		return nil, errors.New("recording storage could not be initialized")
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	e := &entry{recording: recording, cancel: cancel, done: make(chan struct{}), media: cloneMediaSource(media), refreshGate: make(chan struct{}, 1), adapterID: adapterID, resource: cloneResourceRef(resource)}
	m.mu.Lock()
	m.entries[id] = e
	m.mu.Unlock()
	go m.run(workerCtx, e, cloneMediaSource(media))
	return clone(recording), nil
}

func (m *Manager) Stop(id string) (*domain.Recording, error) {
	return m.StopContext(context.Background(), id)
}

// StopContext requests cancellation immediately and then waits for the worker
// only until ctx ends. A caller timeout does not undo the stop request.
func (m *Manager) StopContext(ctx context.Context, id string) (*domain.Recording, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	e, ok := m.entry(id)
	if !ok {
		return nil, storage.ErrNotFound
	}
	e.mu.Lock()
	cancel := e.cancel
	done := e.done
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	recording, err := m.Get(id)
	e.mu.Lock()
	terminalErr := e.terminalErr
	e.mu.Unlock()
	if terminalErr != nil {
		return recording, terminalErr
	}
	return recording, err
}

// Delete removes one inactive recording from the manager registry and its
// archive. persistMu serializes deletion against root metadata writes while mu
// is held only for state checks/publication. Active acquisitions must be
// stopped explicitly first.
func (m *Manager) Delete(id string) error {
	return m.delete(id, false)
}

// DeleteTerminalArchive lets a current Engine remove a terminal canonical
// archive after the Runtime Host has confirmed that no Engine generation has
// an active worker for it. This keeps Control Plane out of the archive write
// path when the original owner generation has already exited.
func (m *Manager) DeleteTerminalArchive(id string) error {
	return m.delete(id, true)
}

func (m *Manager) delete(id string, allowUnownedTerminal bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		if m.freshGeneration && !allowUnownedTerminal {
			// A fresh Engine can read archives from prior generations, but it must
			// never delete data it does not own.
			return storage.ErrNotFound
		}
		if allowUnownedTerminal {
			recording, err := m.store.LoadRecordingReadOnly(id)
			if err != nil {
				return err
			}
			switch recording.State {
			case domain.StateStopped, domain.StateCompleted, domain.StateInterrupted:
			default:
				return ErrActiveRecording
			}
		}
		// Store deletion is idempotent and also handles a tombstone left by an
		// interrupted previous delete. The caller must be an Engine.
		return m.store.DeleteRecordingData(id)
	}
	e.persistMu.Lock()
	defer e.persistMu.Unlock()
	e.mu.Lock()
	if e.deleted {
		e.mu.Unlock()
		return nil
	}
	if e.recording == nil || e.recording.State == domain.StateRecording {
		e.mu.Unlock()
		return ErrActiveRecording
	}
	e.deleted = true
	e.mu.Unlock()
	if err := m.store.DeleteRecordingData(id); err != nil {
		e.mu.Lock()
		e.deleted = false
		e.mu.Unlock()
		return err
	}
	delete(m.entries, id)
	return nil
}

// Close stops admission of new work, cancels active workers and waits for
// their durable terminal state. The caller supplies the overall shutdown
// deadline; a timed-out call may be repeated to continue waiting.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()

	m.startsWait.Do(func() {
		go func() {
			m.starts.Wait()
			close(m.startsDone)
		}()
	})
	select {
	case <-m.startsDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	m.mu.RLock()
	entries := make([]*entry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	m.mu.RUnlock()
	// Cancel every active worker before waiting for any one of them. A worker
	// may take time to finish a request or durably publish its terminal state;
	// waiting inline here could otherwise leave later recordings running when
	// the shutdown deadline expires.
	type workerWait struct {
		e    *entry
		done <-chan struct{}
	}
	waits := make([]workerWait, 0, len(entries))
	for _, e := range entries {
		e.mu.Lock()
		cancel, done := e.cancel, e.done
		active := e.recording.State == domain.StateRecording
		e.mu.Unlock()
		if active && cancel != nil {
			cancel()
		}
		waits = append(waits, workerWait{e: e, done: done})
	}
	// Start closing ingest admission immediately after cancelling acquisitions.
	// IngestService.Close starts its bounded drain coordinator before honoring
	// this caller's deadline; that coordinator also wakes Submit calls which
	// have completed their bodies but have not yet acquired a queue slot.
	if err := m.ingest.Close(ctx); err != nil {
		return err
	}
	for _, worker := range waits {
		select {
		case <-worker.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var terminalErrors []error
	for _, worker := range waits {
		worker.e.mu.Lock()
		if worker.e.terminalErr != nil {
			terminalErrors = append(terminalErrors, worker.e.terminalErr)
		}
		worker.e.mu.Unlock()
	}
	return errors.Join(terminalErrors...)
}

func (m *Manager) Get(id string) (*domain.Recording, error) {
	e, ok := m.entry(id)
	if !ok {
		if m.freshGeneration {
			return m.store.LoadRecordingReadOnly(id)
		}
		return nil, storage.ErrNotFound
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	copy := clone(e.recording)
	if copy == nil {
		return nil, errors.New("recording state could not be copied")
	}
	return copy, nil
}

func (m *Manager) List() []*domain.Recording {
	m.mu.RLock()
	entries := make([]*entry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	m.mu.RUnlock()
	result := make([]*domain.Recording, 0, len(entries))
	for _, e := range entries {
		e.mu.Lock()
		if recording := clone(e.recording); recording != nil {
			result = append(result, recording)
		}
		e.mu.Unlock()
	}
	if m.freshGeneration {
		if archived, err := m.store.LoadAllReadOnly(); err == nil {
			byID := make(map[string]*domain.Recording, len(archived)+len(result))
			for _, recording := range archived {
				byID[recording.ID] = recording
			}
			for _, recording := range result {
				byID[recording.ID] = recording
			}
			result = result[:0]
			for _, recording := range byID {
				result = append(result, recording)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result
}

// ListForManagement returns a bounded, read-only snapshot for management
// queries. It rejects oversized collections before cloning any recording,
// releases the manager lock before acquiring entry locks, and returns no
// partial snapshot when the caller's context is canceled.
func (m *Manager) ListForManagement(ctx context.Context, max int) ([]*domain.Recording, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.RLock()
	if len(m.entries) > max {
		m.mu.RUnlock()
		return nil, ErrListLimit
	}
	entries := make([]*entry, 0, len(m.entries))
	for _, e := range m.entries {
		if err := ctx.Err(); err != nil {
			m.mu.RUnlock()
			return nil, err
		}
		entries = append(entries, e)
	}
	m.mu.RUnlock()

	result := make([]*domain.Recording, 0, len(entries))
	lockRetry := time.NewTicker(time.Millisecond)
	defer lockRetry.Stop()
	for _, e := range entries {
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if e.mu.TryLock() {
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-lockRetry.C:
			}
		}
		if err := ctx.Err(); err != nil {
			e.mu.Unlock()
			return nil, err
		}
		recording := clone(e.recording)
		e.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if recording == nil {
			return nil, errors.New("recording snapshot could not be copied")
		}
		result = append(result, recording)
	}
	if m.freshGeneration {
		archived, err := m.store.LoadAllReadOnlyLimit(max)
		if err != nil {
			if errors.Is(err, storage.ErrReadOnlyListLimit) {
				return nil, ErrListLimit
			}
			return nil, err
		}
		byID := make(map[string]*domain.Recording, len(archived)+len(result))
		for _, recording := range archived {
			byID[recording.ID] = recording
		}
		for _, recording := range result {
			byID[recording.ID] = recording
		}
		if max >= 0 && len(byID) > max {
			return nil, ErrListLimit
		}
		result = make([]*domain.Recording, 0, len(byID))
		for _, recording := range byID {
			result = append(result, recording)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Keep List's newest-first ordering and equivalent timestamp tie behavior.
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// OwnedStates returns only lightweight lifecycle state for this manager's
// worker registry. A candidate manager never includes read-only archive
// snapshots from prior generations.
func (m *Manager) OwnedStates(max int) ([]OwnedRecordingState, error) {
	if max < 0 {
		return nil, ErrListLimit
	}
	m.mu.RLock()
	if max > 0 && len(m.entries) > max {
		m.mu.RUnlock()
		return nil, ErrListLimit
	}
	entries := make([]*entry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	m.mu.RUnlock()
	result := make([]OwnedRecordingState, 0, len(entries))
	for _, e := range entries {
		e.mu.Lock()
		if e.recording != nil {
			result = append(result, OwnedRecordingState{ID: e.recording.ID, State: e.recording.State, StartedAt: e.recording.StartedAt})
		}
		e.mu.Unlock()
	}
	if max > 0 && len(result) > max {
		return nil, ErrListLimit
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartedAt.After(result[j].StartedAt) })
	return result, nil
}

func (m *Manager) Store() *storage.Store { return m.store }

func (m *Manager) entry(id string) (*entry, bool) {
	m.mu.RLock()
	e, ok := m.entries[id]
	m.mu.RUnlock()
	return e, ok
}

func (m *Manager) update(e *entry, fn func(*domain.Recording) error) error {
	e.persistMu.Lock()
	defer e.persistMu.Unlock()
	e.mu.Lock()
	if e.deleted {
		e.mu.Unlock()
		return errors.New("recording was deleted")
	}
	next := clone(e.recording)
	e.mu.Unlock()
	if next == nil {
		e.mu.Lock()
		if e.terminalErr == nil {
			e.terminalErr = errors.New("recording state persistence failed")
		}
		e.mu.Unlock()
		return errors.New("recording state could not be copied")
	}
	if err := fn(next); err != nil {
		return err
	}
	if err := m.store.SaveRecording(next); err != nil {
		return errors.New("recording metadata persistence failed")
	}
	e.mu.Lock()
	e.recording = next
	e.mu.Unlock()
	return nil
}

func (m *Manager) run(ctx context.Context, e *entry, media adapterproto.MediaSource) {
	m.runWorker(ctx, e, media)
}

func (m *Manager) fail(e *entry, err error) {
	safe := safeFailureDescription(err)
	if updateErr := m.update(e, func(r *domain.Recording) error {
		if errors.Is(err, errStorageCommit) {
			// A source body was already fetched; failure to persist it is not a
			// source gap. Clear pending observations instead of misclassifying
			// backend failure as missing media.
			for _, track := range r.Tracks {
				if track == nil {
					continue
				}
				track.PendingSequences = nil
				track.PendingSegments = nil
			}
		} else {
			m.finalizePending(r, "recording ended with uncaptured media")
		}
		r.State = domain.StateInterrupted
		r.LastError = safe
		now := time.Now().UTC()
		r.StoppedAt = &now
		return nil
	}); updateErr != nil {
		m.setTerminalError(e, errors.New("recording terminal state persistence failed"))
		return
	}
	m.setTerminalError(e, errors.New(safe))
}

func (m *Manager) setTerminalError(e *entry, err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	if e.terminalErr == nil {
		e.terminalErr = err
	}
	e.mu.Unlock()
}

func safeFailureDescription(err error) string {
	if errors.Is(err, errStorageCommit) {
		return "recording storage commit failed"
	}
	var fetchErr *FetchError
	if errors.As(err, &fetchErr) {
		return fetchErr.Error()
	}
	if errors.Is(err, context.Canceled) {
		return "recording stopped"
	}
	return "recording acquisition failed"
}

// clone ensures API callers cannot mutate state protected by the manager.
func clone(r *domain.Recording) *domain.Recording {
	if r == nil {
		return nil
	}
	data, err := json.Marshal(r)
	if err != nil {
		return nil
	}
	var copy domain.Recording
	if err = json.Unmarshal(data, &copy); err != nil {
		return nil
	}
	return &copy
}
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func validRecordingID(id string) bool {
	if len(id) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 16 && id == strings.ToLower(id)
}
func closedChannel() chan struct{} { ch := make(chan struct{}); close(ch); return ch }

// The parser functions are variables to keep the acquisition loop easy to
// exercise through local HTTP tests without introducing another abstraction.
var (
	hlsParseMaster = parseMaster
	hlsParseMedia  = parseMedia
	selectVariant  = chooseVariant
)
