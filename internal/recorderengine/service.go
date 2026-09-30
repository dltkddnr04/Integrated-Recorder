// Package recorderengine owns the acquisition manager and adapter runtime for
// one engine generation. Its IPC surface is intentionally narrower than the
// management API.
package recorderengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/acquire"
	"github.com/dltkddnr04/integrated-recorder/internal/adapterhost"
	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
	"github.com/dltkddnr04/integrated-recorder/internal/domain"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimeipc"
	"github.com/dltkddnr04/integrated-recorder/internal/storage"
)

const (
	OperationReady          = "ready"
	OperationHeartbeat      = "heartbeat"
	OperationStartResolved  = "start_resolved"
	OperationBeginDrain     = "begin_drain"
	OperationActiveCount    = "active_recordings"
	OperationGet            = "get"
	OperationList           = "list"
	OperationStop           = "stop"
	OperationDelete         = "delete"
	OperationDeleteTerminal = "delete_terminal_archive"
	OperationInventory      = "inventory"
	DefaultListLimit        = 2000
	MaximumListLimit        = 10000
)

type StartResolvedRequest struct {
	RecordingID string                          `json:"recording_id,omitempty"`
	AdapterID   string                          `json:"adapter_id"`
	Media       adapterproto.MediaSource        `json:"media"`
	Resource    *adapterproto.ResourceRef       `json:"resource,omitempty"`
	Title       string                          `json:"title,omitempty"`
	Provenance  *adapterproto.AdapterProvenance `json:"provenance,omitempty"`
}

type RecordingIDRequest struct {
	RecordingID string `json:"recording_id"`
}

type GenerationRequest struct {
	GenerationID string `json:"generation_id"`
}

type ListRequest struct {
	Limit int `json:"limit,omitempty"`
}

type ReadyResult struct {
	Ready                bool      `json:"ready"`
	GenerationID         string    `json:"generation_id"`
	InstanceID           string    `json:"instance_id"`
	StartedAt            time.Time `json:"started_at"`
	ProtocolVersion      int       `json:"protocol_version"`
	ActiveRecordingCount int       `json:"active_recording_count"`
}

type HeartbeatResult struct {
	GenerationID string    `json:"generation_id"`
	InstanceID   string    `json:"instance_id"`
	At           time.Time `json:"at"`
}

type ActiveRecording struct {
	RecordingID string                `json:"recording_id"`
	State       domain.RecordingState `json:"state"`
	StartedAt   time.Time             `json:"started_at"`
}

type InventoryResult struct {
	GenerationID string            `json:"generation_id"`
	InstanceID   string            `json:"instance_id"`
	Ready        bool              `json:"ready"`
	Active       []ActiveRecording `json:"active_recordings"`
}

type DrainResult struct {
	GenerationID string `json:"generation_id"`
	InstanceID   string `json:"instance_id"`
	Draining     bool   `json:"draining"`
}

type ActiveRecordingCountResult struct {
	GenerationID string `json:"generation_id"`
	InstanceID   string `json:"instance_id"`
	Count        int    `json:"count"`
}

type Engine struct {
	manager          *acquire.Manager
	adapters         *adapterhost.Host
	generation       string
	instance         string
	startedAt        time.Time
	mu               sync.RWMutex
	admissionMu      sync.RWMutex
	draining         bool
	ready            bool
	closed           bool
	adapterCloseOnce sync.Once
}

func New(manager *acquire.Manager, adapters *adapterhost.Host, generationID, instanceID string) (*Engine, error) {
	if manager == nil || adapters == nil {
		return nil, errors.New("recorder manager and adapter runtime are required")
	}
	if generationID == "" || instanceID == "" {
		return nil, errors.New("engine generation and instance identities are required")
	}
	e := &Engine{manager: manager, adapters: adapters, generation: generationID, instance: instanceID, startedAt: time.Now().UTC(), ready: true}
	return e, nil
}

// RuntimeInstanceID is returned in the transport envelope so a Control Plane
// can pin subsequent calls to the same long-lived Engine process.
func (e *Engine) RuntimeInstanceID() string { return e.instance }

func (e *Engine) GenerationID() string { return e.generation }

func (e *Engine) Handle(ctx context.Context, operation string, payload json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.RLock()
	closed, ready := e.closed, e.ready
	e.mu.RUnlock()
	if closed {
		return nil, publicError("engine_unavailable", "recorder engine is shutting down")
	}
	switch operation {
	case OperationReady:
		rows, err := e.manager.OwnedStates(MaximumListLimit)
		if err != nil {
			return nil, publicError("inventory_too_large", "engine inventory exceeds the supported limit")
		}
		active := 0
		for _, row := range rows {
			if row.State == domain.StateRecording {
				active++
			}
		}
		return ReadyResult{Ready: ready, GenerationID: e.generation, InstanceID: e.instance, StartedAt: e.startedAt, ProtocolVersion: runtimeipc.ProtocolVersion, ActiveRecordingCount: active}, nil
	case OperationHeartbeat:
		return HeartbeatResult{GenerationID: e.generation, InstanceID: e.instance, At: time.Now().UTC()}, nil
	case OperationBeginDrain:
		var request GenerationRequest
		if err := decodePayload(payload, &request); err != nil || request.GenerationID == "" {
			return nil, publicError("invalid_request", "engine generation identity is invalid")
		}
		if request.GenerationID != e.generation {
			return nil, publicError("generation_mismatch", "request targets another engine generation")
		}
		// The write lock waits for any start already admitted under admissionMu
		// to finish, then fences every subsequent start atomically. Existing
		// recording workers use the Manager directly and are unaffected.
		e.admissionMu.Lock()
		e.draining = true
		e.admissionMu.Unlock()
		return DrainResult{GenerationID: e.generation, InstanceID: e.instance, Draining: true}, nil
	case OperationActiveCount:
		var request GenerationRequest
		if err := decodePayload(payload, &request); err != nil || request.GenerationID == "" {
			return nil, publicError("invalid_request", "engine generation identity is invalid")
		}
		if request.GenerationID != e.generation {
			return nil, publicError("generation_mismatch", "request targets another engine generation")
		}
		count, err := e.activeRecordingCount(ctx)
		if err != nil {
			return nil, publicError("inventory_too_large", "engine inventory exceeds the supported limit")
		}
		return ActiveRecordingCountResult{GenerationID: e.generation, InstanceID: e.instance, Count: count}, nil
	case OperationStartResolved:
		var request StartResolvedRequest
		if err := decodePayload(payload, &request); err != nil {
			return nil, publicError("invalid_request", "resolved recording request is invalid")
		}
		e.admissionMu.RLock()
		defer e.admissionMu.RUnlock()
		if e.draining {
			return nil, publicError("engine_draining", "recorder engine is draining and cannot start recordings")
		}
		if request.RecordingID == "" {
			result, err := e.manager.StartResolved(ctx, request.AdapterID, request.Media, request.Resource, request.Title, request.Provenance)
			if err != nil {
				return nil, publicError("start_failed", "recording could not be started")
			}
			return result, nil
		}
		result, err := e.manager.StartResolvedWithID(ctx, request.RecordingID, request.AdapterID, request.Media, request.Resource, request.Title, request.Provenance)
		if err != nil {
			return nil, publicError("start_failed", "recording could not be started")
		}
		return result, nil
	case OperationGet:
		var request RecordingIDRequest
		if err := decodePayload(payload, &request); err != nil || request.RecordingID == "" {
			return nil, publicError("invalid_request", "recording identity is invalid")
		}
		result, err := e.manager.Get(request.RecordingID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil, publicError("not_found", "recording was not found")
			}
			return nil, publicError("read_failed", "recording could not be read")
		}
		return result, nil
	case OperationList:
		var request ListRequest
		if err := decodePayload(payload, &request); err != nil {
			return nil, publicError("invalid_request", "recording list request is invalid")
		}
		limit := request.Limit
		if limit == 0 {
			limit = DefaultListLimit
		}
		if limit < 1 || limit > MaximumListLimit {
			return nil, publicError("invalid_request", "recording list limit is outside the supported range")
		}
		result, err := e.manager.ListForManagement(ctx, limit)
		if errors.Is(err, acquire.ErrListLimit) {
			return nil, publicError("list_too_large", "recording list exceeds the requested limit")
		}
		if err != nil {
			return nil, publicError("read_failed", "recording list could not be read")
		}
		return result, nil
	case OperationStop:
		var request RecordingIDRequest
		if err := decodePayload(payload, &request); err != nil || request.RecordingID == "" {
			return nil, publicError("invalid_request", "recording identity is invalid")
		}
		result, err := e.manager.StopContext(ctx, request.RecordingID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil, publicError("not_owned", "recording is not owned by this engine")
			}
			return nil, publicError("stop_failed", "recording could not be stopped")
		}
		return result, nil
	case OperationDelete:
		var request RecordingIDRequest
		if err := decodePayload(payload, &request); err != nil || request.RecordingID == "" {
			return nil, publicError("invalid_request", "recording identity is invalid")
		}
		if err := e.manager.Delete(request.RecordingID); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil, publicError("not_owned", "recording is not owned by this engine")
			}
			if errors.Is(err, acquire.ErrActiveRecording) {
				return nil, publicError("active_recording", "active recording cannot be deleted")
			}
			return nil, publicError("delete_failed", "recording could not be deleted")
		}
		return struct {
			Deleted bool `json:"deleted"`
		}{Deleted: true}, nil
	case OperationDeleteTerminal:
		var request RecordingIDRequest
		if err := decodePayload(payload, &request); err != nil || request.RecordingID == "" {
			return nil, publicError("invalid_request", "recording identity is invalid")
		}
		if err := e.manager.DeleteTerminalArchive(request.RecordingID); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil, publicError("not_found", "recording was not found")
			}
			if errors.Is(err, acquire.ErrActiveRecording) {
				return nil, publicError("active_recording", "active recording cannot be deleted")
			}
			return nil, publicError("delete_failed", "recording could not be deleted")
		}
		return struct {
			Deleted bool `json:"deleted"`
		}{Deleted: true}, nil
	case OperationInventory:
		rows, err := e.manager.OwnedStates(MaximumListLimit)
		if err != nil {
			return nil, publicError("inventory_too_large", "engine inventory exceeds the supported limit")
		}
		active := make([]ActiveRecording, 0, len(rows))
		for _, row := range rows {
			if row.State == domain.StateRecording {
				active = append(active, ActiveRecording{RecordingID: row.ID, State: row.State, StartedAt: row.StartedAt})
			}
		}
		return InventoryResult{GenerationID: e.generation, InstanceID: e.instance, Ready: ready, Active: active}, nil
	default:
		return nil, publicError("unsupported_operation", "runtime operation is not supported")
	}
}

func (e *Engine) activeRecordingCount(ctx context.Context) (int, error) {
	rows, err := e.manager.OwnedStates(MaximumListLimit)
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	count := 0
	for _, row := range rows {
		if row.State == domain.StateRecording {
			count++
		}
	}
	return count, nil
}

// Close is called only by the engine process's own shutdown path. IPC client
// disconnects never call this method.
func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	e.closed = true
	e.ready = false
	e.mu.Unlock()
	closeErr := e.manager.Close(ctx)
	// Manager.Close explicitly supports retrying after a caller deadline. Keep
	// adapter processes available until all acquisition workers have drained.
	if ctx == nil || ctx.Err() == nil {
		e.adapterCloseOnce.Do(e.adapters.Close)
	}
	return closeErr
}

type operationError struct {
	code    string
	message string
}

func (e operationError) Error() string                    { return e.message }
func (e operationError) PublicIPCError() (string, string) { return e.code, e.message }

func publicError(code, message string) error { return operationError{code: code, message: message} }

func decodePayload(data json.RawMessage, dst any) error {
	if len(data) == 0 || !json.Valid(data) {
		return errors.New("missing or malformed payload")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing payload data")
	}
	return nil
}
