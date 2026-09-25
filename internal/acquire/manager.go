// Package acquire owns recording lifecycles and the shared HLS polling and
// original-byte acquisition path.
package acquire

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/domain"
	"github.com/dltkddnr04/integrated-recorder/internal/network"
	"github.com/dltkddnr04/integrated-recorder/internal/platform/owncast"
	"github.com/dltkddnr04/integrated-recorder/internal/storage"
)

type Resolver interface {
	Resolve(instanceBase string) (string, error)
}
type SourceValidator func(context.Context, string) error

type entry struct {
	mu        sync.Mutex
	recording *domain.Recording
	cancel    context.CancelFunc
	done      chan struct{}
}

type Manager struct {
	store    *storage.Store
	client   *http.Client
	resolver Resolver
	validate SourceValidator
	mu       sync.RWMutex
	entries  map[string]*entry
}

func NewManager(store *storage.Store, client *http.Client, resolver Resolver, validate SourceValidator) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("storage is required")
	}
	if client == nil {
		client = network.NewPublicHTTPClient(25 * time.Second)
	}
	if resolver == nil {
		resolver = owncast.Resolver{}
	}
	if validate == nil {
		validate = network.ValidatePublicURL
	}
	loaded, err := store.LoadAll()
	if err != nil {
		return nil, err
	}
	m := &Manager{store: store, client: client, resolver: resolver, validate: validate, entries: map[string]*entry{}}
	for _, recording := range loaded {
		m.entries[recording.ID] = &entry{recording: recording, done: closedChannel()}
	}
	return m, nil
}

func (m *Manager) Start(ctx context.Context, sourceURL, title string) (*domain.Recording, error) {
	if err := m.validate(ctx, sourceURL); err != nil {
		return nil, fmt.Errorf("invalid source URL: %w", err)
	}
	manifestURL, err := m.resolver.Resolve(sourceURL)
	if err != nil {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	recording := &domain.Recording{FormatVersion: 1, ID: id, Title: title, SourceURL: sourceURL, State: domain.StateRecording, CreatedAt: now, StartedAt: now, Tracks: map[string]*domain.Track{
		"main": {ID: "main", SourcePlaylistURL: manifestURL, Segments: []domain.Segment{}, InitSegments: []domain.Segment{}},
	}}
	if err = m.store.NewRecordingDir(id); err != nil {
		return nil, err
	}
	if err = m.store.SaveRecording(recording); err != nil {
		return nil, err
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	e := &entry{recording: recording, cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	m.entries[id] = e
	m.mu.Unlock()
	go m.run(workerCtx, e, manifestURL)
	return m.Get(id)
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
	return m.Get(id)
}

func (m *Manager) Get(id string) (*domain.Recording, error) {
	e, ok := m.entry(id)
	if !ok {
		return nil, storage.ErrNotFound
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return clone(e.recording), nil
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
		result = append(result, clone(e.recording))
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
	if err := fn(e.recording); err != nil {
		return err
	}
	return m.store.SaveRecording(e.recording)
}

func (m *Manager) run(ctx context.Context, e *entry, initialURL string) {
	defer close(e.done)
	defer func() { e.mu.Lock(); e.cancel = nil; e.mu.Unlock() }()
	defer func() {
		if ctx.Err() != nil {
			_ = m.update(e, func(r *domain.Recording) error {
				if r.State == domain.StateRecording {
					m.finalizePending(r, "recording stopped before pending segments could be captured")
					r.State = domain.StateStopped
					now := time.Now().UTC()
					r.StoppedAt = &now
				}
				return nil
			})
		}
	}()
	selectedURL := initialURL
	first := true
	for {
		if ctx.Err() != nil {
			return
		}
		if first {
			body, err := fetchManifest(ctx, m.client, selectedURL)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				m.fail(e, err)
				return
			}
			if err = m.saveSnapshot(e, "main", selectedURL, body); err != nil {
				m.fail(e, err)
				return
			}
			if hasMasterTag(body) {
				master, parseErr := hlsParseMaster(body, selectedURL)
				if parseErr != nil {
					m.fail(e, parseErr)
					return
				}
				variant, selectErr := selectVariant(master)
				if selectErr != nil {
					m.fail(e, selectErr)
					return
				}
				selectedURL = variant.URI
				if err = m.update(e, func(r *domain.Recording) error {
					t := r.Tracks["main"]
					t.SourcePlaylistURL = selectedURL
					t.Bandwidth = variant.Bandwidth
					return nil
				}); err != nil {
					m.fail(e, err)
					return
				}
				body, err = fetchManifest(ctx, m.client, selectedURL)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					m.fail(e, err)
					return
				}
				if err = m.saveSnapshot(e, "main", selectedURL, body); err != nil {
					m.fail(e, err)
					return
				}
				playlist, parseErr := hlsParseMedia(body, selectedURL)
				if parseErr != nil {
					m.fail(e, parseErr)
					return
				}
				done, processErr := m.process(e, ctx, playlist)
				if processErr != nil {
					if ctx.Err() != nil {
						return
					}
					m.fail(e, processErr)
					return
				}
				if done {
					return
				}
				first = false
				if err = wait(ctx, pollDelay(playlist.TargetDuration)); err != nil {
					return
				}
				continue
			}
			playlist, parseErr := hlsParseMedia(body, selectedURL)
			if parseErr != nil {
				m.fail(e, parseErr)
				return
			}
			done, processErr := m.process(e, ctx, playlist)
			if processErr != nil {
				if ctx.Err() != nil {
					return
				}
				m.fail(e, processErr)
				return
			}
			if done {
				return
			}
			first = false
			if err = wait(ctx, pollDelay(playlist.TargetDuration)); err != nil {
				return
			}
			continue
		}
		body, err := fetchManifest(ctx, m.client, selectedURL)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			m.fail(e, err)
			return
		}
		if err = m.saveSnapshot(e, "main", selectedURL, body); err != nil {
			m.fail(e, err)
			return
		}
		playlist, err := hlsParseMedia(body, selectedURL)
		if err != nil {
			m.fail(e, err)
			return
		}
		done, err := m.process(e, ctx, playlist)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			m.fail(e, err)
			return
		}
		if done {
			return
		}
		if err = wait(ctx, pollDelay(playlist.TargetDuration)); err != nil {
			return
		}
	}
}

func (m *Manager) fail(e *entry, err error) {
	_ = m.update(e, func(r *domain.Recording) error {
		m.finalizePending(r, "recording ended with uncaptured segments")
		r.State = domain.StateInterrupted
		r.LastError = err.Error()
		now := time.Now().UTC()
		r.StoppedAt = &now
		return nil
	})
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
	data, _ := json.Marshal(r)
	var copy domain.Recording
	_ = json.Unmarshal(data, &copy)
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
