package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
)

// MutationGate admits mutating HTTP requests only while a Control generation
// owns the active epoch. It also drains requests admitted before fencing.
// Reads remain available to readiness checks and in-flight proxy transitions.
type MutationGate struct {
	mu       sync.Mutex
	active   bool
	inFlight int
	changed  chan struct{}
}

func NewMutationGate() *MutationGate {
	return &MutationGate{changed: make(chan struct{})}
}

func (g *MutationGate) Wrap(next http.Handler) http.Handler {
	if g == nil || next == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isMutatingMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		if !g.admit() {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "control generation is not accepting changes"})
			return
		}
		defer g.release()
		next.ServeHTTP(w, r)
	})
}

func isMutatingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func (g *MutationGate) admit() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.active {
		return false
	}
	g.inFlight++
	return true
}

func (g *MutationGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inFlight--
	if g.inFlight == 0 {
		g.signalLocked()
	}
}

// Activate opens mutation admission. It is used only after the Runtime Host
// has transferred the active control epoch.
func (g *MutationGate) Activate() {
	g.mu.Lock()
	g.active = true
	g.signalLocked()
	g.mu.Unlock()
}

// Fence immediately rejects new mutations. Already admitted requests are
// allowed to finish and may be joined with WaitForDrain.
func (g *MutationGate) Fence() {
	g.mu.Lock()
	g.active = false
	g.signalLocked()
	g.mu.Unlock()
}

func (g *MutationGate) WaitForDrain(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		g.mu.Lock()
		if g.inFlight == 0 {
			g.mu.Unlock()
			return nil
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// WaitAdmission lets durable Control-owned background mutations pause at an
// epoch fence and resume after rollback. Callers must release the returned
// function after the mutation is complete.
func (g *MutationGate) WaitAdmission(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		g.mu.Lock()
		if g.active {
			g.inFlight++
			g.mu.Unlock()
			return g.release, nil
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (g *MutationGate) signalLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}
