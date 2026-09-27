package adapterhost

import (
	"context"
	"errors"
	"time"
)

var (
	ErrAdapterHostClosed      = errors.New("adapter host is closed")
	ErrAdapterUnavailable     = errors.New("adapter is unavailable")
	ErrAdapterDisabled        = errors.New("adapter is disabled")
	ErrAdapterBusy            = errors.New("adapter operation is in progress")
	ErrAdapterRestartRejected = errors.New("adapter restart was rejected")
)

// SetEnabled changes whether new operations may use an adapter. Disabling is
// serialized with adapter IPC, cancels only that adapter's pending workflows,
// and gracefully stops its process. Enabling starts a fresh process and runs
// the same descriptor/fingerprint handshake used by automatic recovery.
func (h *Host) SetEnabled(id string, enabled bool) error {
	e, err := h.controlEntry(id)
	if err != nil {
		return err
	}
	if !e.opMu.TryLock() {
		return ErrAdapterBusy
	}
	defer e.opMu.Unlock()
	if h.isClosed() {
		return ErrAdapterHostClosed
	}

	e.stateMu.Lock()
	if enabled && e.restartRejected {
		e.status.State = "rejected"
		e.status.Error = "adapter restart failed validation"
		e.stateMu.Unlock()
		return ErrAdapterRestartRejected
	}
	if enabled && !e.disabled {
		e.stateMu.Unlock()
		return nil
	}
	if !enabled && e.disabled {
		e.stateMu.Unlock()
		return nil
	}
	e.restarting = true
	if enabled {
		e.status.State = "restarting"
	} else {
		e.disabled = true
		e.status.State = "disabled"
		e.status.Error = ""
	}
	p := e.process
	e.process = nil
	e.stateMu.Unlock()

	// Set the state gate before taking h.mu. Calls which passed entryFor just
	// before this transition are checked again under opMu/ensureProcess.
	h.cancelAdapterWorkflows(id)
	if p != nil {
		p.shutdown()
	}

	if !enabled {
		e.stateMu.Lock()
		e.restarting = false
		e.stateMu.Unlock()
		return nil
	}
	e.stateMu.Lock()
	e.disabled = false
	e.restarting = false
	e.nextRestart = time.Time{}
	e.status.State = "unavailable"
	e.status.Error = ""
	e.stateMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := h.ensureProcess(ctx, e); err != nil {
		// Keep runtime and persisted disabled state aligned when the caller
		// cannot complete the same validated startup handshake.
		e.stateMu.Lock()
		failed := e.process
		e.process = nil
		e.disabled = true
		e.restarting = false
		if e.restartRejected {
			e.status.State = "rejected"
			e.status.Error = "adapter restart failed validation"
		} else {
			e.status.State = "disabled"
			e.status.Error = "adapter could not be enabled"
		}
		e.stateMu.Unlock()
		if failed != nil {
			failed.shutdown()
		}
		return err
	}
	return nil
}

// Restart replaces one adapter process in place. Its current descriptor is
// retained and the replacement must match the original fingerprint. Recording
// workers already using resolved media sources do not depend on this process.
func (h *Host) Restart(ctx context.Context, id string) (Adapter, error) {
	e, err := h.controlEntry(id)
	if err != nil {
		return Adapter{}, err
	}
	if !e.opMu.TryLock() {
		return Adapter{}, ErrAdapterBusy
	}
	defer e.opMu.Unlock()
	if h.isClosed() {
		return Adapter{}, ErrAdapterHostClosed
	}
	e.stateMu.Lock()
	if e.disabled {
		e.stateMu.Unlock()
		return Adapter{}, ErrAdapterDisabled
	}
	if e.restartRejected {
		e.stateMu.Unlock()
		return Adapter{}, ErrAdapterRestartRejected
	}
	e.restarting = true
	e.status.State = "restarting"
	p := e.process
	e.process = nil
	e.stateMu.Unlock()

	h.cancelAdapterWorkflows(id)
	if p != nil {
		p.shutdown()
	}

	e.stateMu.Lock()
	e.restarting = false
	e.nextRestart = time.Time{}
	e.status.State = "unavailable"
	e.status.Error = ""
	e.stateMu.Unlock()

	if _, err := h.ensureProcess(ctx, e); err != nil {
		adapter, _ := h.Get(id)
		return adapter, err
	}
	return h.Get(id)
}

func (h *Host) controlEntry(id string) (*entry, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return nil, ErrAdapterHostClosed
	}
	e, ok := h.entries[id]
	if !ok || e.descriptor == nil {
		return nil, ErrAdapterUnavailable
	}
	return e, nil
}

func (h *Host) isClosed() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.closed
}

// cancelAdapterWorkflows removes only sessions owned by id, including their
// interaction tracker state and cancellation contexts.
func (h *Host) cancelAdapterWorkflows(id string) {
	h.mu.Lock()
	for workflowID, session := range h.workflows {
		if session.adapterID == id {
			h.removeWorkflowLocked(workflowID, session, "canceled")
		}
	}
	h.mu.Unlock()
}

// workflowGenerationCurrentLocked is called while h.mu is held immediately
// before publishing a challenge. It closes the race where a resolve response
// arrives just before a manual restart and the stale workflow is inserted only
// after restart cleanup completed.
func (h *Host) workflowGenerationCurrentLocked(session workflowSession) bool {
	if session.generation == 0 || h.entries == nil {
		return true // compatibility for isolated unit fixtures
	}
	e, ok := h.entries[session.adapterID]
	if !ok || e.descriptor == nil {
		return false
	}
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	return !e.disabled && !e.restarting && e.status.Generation == session.generation
}
