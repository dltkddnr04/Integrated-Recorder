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

type SourceValidator func(context.Context, string) error

type entry struct {
	mu          sync.Mutex
	recording   *domain.Recording
	cancel      context.CancelFunc
	done        chan struct{}
	media       adapterproto.MediaSource
	adapterID   string
	resource    *adapterproto.ResourceRef
	terminalErr error
}

type Manager struct {
	store      *storage.Store
	client     *http.Client
	resolver   Resolver
	validate   SourceValidator
	mu         sync.RWMutex
	entries    map[string]*entry
	closed     bool
	starts     sync.WaitGroup
	startsWait sync.Once
	startsDone chan struct{}
}

var errManagerClosed = errors.New("recording manager is closed")

func NewManager(store *storage.Store, client *http.Client, resolver Resolver, validate SourceValidator) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("storage is required")
	}
	if client == nil {
		client = network.NewPublicHTTPClient(25 * time.Second)
	}
	if validate == nil {
		validate = network.ValidatePublicURL
	}
	loaded, err := store.LoadAll()
	if err != nil {
		return nil, err
	}
	if resolver == nil {
		resolver = unavailableResolver{}
	}
	m := &Manager{store: store, client: client, resolver: resolver, validate: validate, entries: map[string]*entry{}, startsDone: make(chan struct{})}
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
	return m.startResolved(ctx, adapterID, media, resource, title, nil)
}

// StartResolved creates a recording from a media source already resolved by
// an adapter workflow. The shared HLS acquisition path is identical to Start.
func (m *Manager) StartResolved(ctx context.Context, adapterID string, media adapterproto.MediaSource, resource *adapterproto.ResourceRef, title string, provenance *adapterproto.AdapterProvenance) (*domain.Recording, error) {
	if err := m.beginStart(); err != nil {
		return nil, err
	}
	defer m.starts.Done()
	return m.startResolved(ctx, adapterID, media, resource, title, provenance)
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

func (m *Manager) startResolved(ctx context.Context, adapterID string, media adapterproto.MediaSource, resource *adapterproto.ResourceRef, title string, provenance *adapterproto.AdapterProvenance) (*domain.Recording, error) {
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
	id, err := newID()
	if err != nil {
		return nil, err
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
	e := &entry{recording: recording, cancel: cancel, done: make(chan struct{}), media: cloneMediaSource(media), adapterID: adapterID, resource: cloneResourceRef(resource)}
	m.mu.Lock()
	m.entries[id] = e
	m.mu.Unlock()
	go m.run(workerCtx, e, cloneMediaSource(media))
	return clone(recording), nil
}

func (m *Manager) Stop(id string) (*domain.Recording, error) {
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
	<-done
	recording, err := m.Get(id)
	e.mu.Lock()
	terminalErr := e.terminalErr
	e.mu.Unlock()
	if terminalErr != nil {
		return recording, terminalErr
	}
	return recording, err
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
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result
}

func (m *Manager) Store() *storage.Store { return m.store }

func (m *Manager) entry(id string) (*entry, bool) {
	m.mu.RLock()
	e, ok := m.entries[id]
	m.mu.RUnlock()
	return e, ok
}

func (m *Manager) update(e *entry, fn func(*domain.Recording) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	next := clone(e.recording)
	if next == nil {
		if e.terminalErr == nil {
			e.terminalErr = errors.New("recording state persistence failed")
		}
		return errors.New("recording state could not be copied")
	}
	if err := fn(next); err != nil {
		return err
	}
	if err := m.store.SaveRecording(next); err != nil {
		if e.terminalErr == nil {
			e.terminalErr = errors.New("recording metadata persistence failed")
		}
		return errors.New("recording metadata persistence failed")
	}
	e.recording = next
	return nil
}

func (m *Manager) run(ctx context.Context, e *entry, media adapterproto.MediaSource) {
	m.runWorker(ctx, e, media)
}

func (m *Manager) fail(e *entry, err error) {
	safe := safeFailureDescription(err)
	if updateErr := m.update(e, func(r *domain.Recording) error {
		m.finalizePending(r, "recording ended with uncaptured media")
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
	var fetchErr *FetchError
	if errors.As(err, &fetchErr) {
		return fetchErr.Error()
	}
	if errors.Is(err, context.Canceled) {
		return "recording stopped"
	}
	return "recording acquisition failed"
}

func (m *Manager) saveSnapshot(e *entry, trackID, source string, data []byte) error {
	var id string
	e.mu.Lock()
	id = e.recording.ID
	e.mu.Unlock()
	snapshot, err := m.store.SaveSnapshot(id, trackID, source, data, time.Now().UTC())
	if err != nil {
		return err
	}
	return m.update(e, func(r *domain.Recording) error { r.Snapshots = append(r.Snapshots, snapshot); return nil })
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
func closedChannel() chan struct{} { ch := make(chan struct{}); close(ch); return ch }

// The parser functions are variables to keep the acquisition loop easy to
// exercise through local HTTP tests without introducing another abstraction.
var (
	hlsParseMaster = parseMaster
	hlsParseMedia  = parseMedia
	selectVariant  = chooseVariant
)
