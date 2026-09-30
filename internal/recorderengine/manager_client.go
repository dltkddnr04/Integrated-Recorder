package recorderengine

import (
	"context"
	"errors"
	"fmt"

	"github.com/dltkddnr04/integrated-recorder/internal/acquire"
	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
	"github.com/dltkddnr04/integrated-recorder/internal/domain"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimeipc"
	"github.com/dltkddnr04/integrated-recorder/internal/storage"
)

// ManagerClient adapts one generation-pinned Engine IPC client to the
// recordingManager method set consumed by the Control Plane. It has no local
// storage fallback: all lifecycle mutations are sent to the selected Engine.
type ManagerClient struct {
	client *runtimeipc.Client
}

var ErrEngineDraining = errors.New("recorder engine is draining")

func NewManagerClient(client *runtimeipc.Client) (*ManagerClient, error) {
	if client == nil {
		return nil, errors.New("recorder engine IPC client is required")
	}
	return &ManagerClient{client: client}, nil
}

func (m *ManagerClient) StartResolved(ctx context.Context, adapterID string, media adapterproto.MediaSource, resource *adapterproto.ResourceRef, title string, provenance *adapterproto.AdapterProvenance) (*domain.Recording, error) {
	return m.start(ctx, StartResolvedRequest{AdapterID: adapterID, Media: media, Resource: resource, Title: title, Provenance: provenance})
}

func (m *ManagerClient) StartResolvedWithID(ctx context.Context, id, adapterID string, media adapterproto.MediaSource, resource *adapterproto.ResourceRef, title string, provenance *adapterproto.AdapterProvenance) (*domain.Recording, error) {
	return m.start(ctx, StartResolvedRequest{RecordingID: id, AdapterID: adapterID, Media: media, Resource: resource, Title: title, Provenance: provenance})
}

func (m *ManagerClient) start(ctx context.Context, request StartResolvedRequest) (*domain.Recording, error) {
	var recording domain.Recording
	if err := m.client.Call(ctx, OperationStartResolved, request, &recording); err != nil {
		return nil, normalizeEngineError(err)
	}
	return &recording, nil
}

// BeginDrain fences new recording admission on the specified generation. The
// engine waits for any already-admitted start call to finish before replying.
// Existing recording workers are not affected.
func (m *ManagerClient) BeginDrain(ctx context.Context, generationID string) error {
	if generationID == "" {
		return errors.New("recorder engine generation identity is required")
	}
	var result DrainResult
	if err := m.client.Call(ctx, OperationBeginDrain, GenerationRequest{GenerationID: generationID}, &result); err != nil {
		return normalizeEngineError(err)
	}
	if result.GenerationID != generationID || result.InstanceID == "" || !result.Draining {
		return errors.New("recorder engine drain response identity is invalid")
	}
	return nil
}

// ActiveRecordings returns a bounded count of this Engine's owned active
// recordings, excluding read-only archive snapshots from other generations.
func (m *ManagerClient) ActiveRecordings(ctx context.Context, generationID string) (int, error) {
	if generationID == "" {
		return 0, errors.New("recorder engine generation identity is required")
	}
	var result ActiveRecordingCountResult
	if err := m.client.Call(ctx, OperationActiveCount, GenerationRequest{GenerationID: generationID}, &result); err != nil {
		return 0, normalizeEngineError(err)
	}
	if result.GenerationID != generationID || result.InstanceID == "" || result.Count < 0 || result.Count > MaximumListLimit {
		return 0, errors.New("recorder engine active recording count response is invalid")
	}
	return result.Count, nil
}

func (m *ManagerClient) Get(id string) (*domain.Recording, error) {
	return m.GetContext(context.Background(), id)
}

func (m *ManagerClient) GetContext(ctx context.Context, id string) (*domain.Recording, error) {
	var recording domain.Recording
	if err := m.client.Call(ctx, OperationGet, RecordingIDRequest{RecordingID: id}, &recording); err != nil {
		return nil, normalizeEngineError(err)
	}
	return &recording, nil
}

// Inventory returns the active recordings owned by this exact Engine process.
// The transport envelope pins the generation; the result is checked as well
// so callers do not accidentally associate another Engine's inventory.
func (m *ManagerClient) Inventory(ctx context.Context) (InventoryResult, error) {
	var inventory InventoryResult
	if err := m.client.Call(ctx, OperationInventory, nil, &inventory); err != nil {
		return InventoryResult{}, normalizeEngineError(err)
	}
	if inventory.GenerationID == "" || inventory.InstanceID == "" {
		return InventoryResult{}, errors.New("recorder engine inventory identity is invalid")
	}
	for _, item := range inventory.Active {
		if item.RecordingID == "" || item.State != domain.StateRecording {
			return InventoryResult{}, errors.New("recorder engine inventory entry is invalid")
		}
	}
	return inventory, nil
}

func (m *ManagerClient) List() []*domain.Recording {
	rows, err := m.ListForManagement(context.Background(), DefaultListLimit)
	if err != nil {
		return nil
	}
	return rows
}

func (m *ManagerClient) ListForManagement(ctx context.Context, limit int) ([]*domain.Recording, error) {
	if limit < 1 || limit > MaximumListLimit {
		return nil, acquire.ErrListLimit
	}
	var recordings []*domain.Recording
	if err := m.client.Call(ctx, OperationList, ListRequest{Limit: limit}, &recordings); err != nil {
		return nil, normalizeEngineError(err)
	}
	if recordings == nil {
		return []*domain.Recording{}, nil
	}
	return recordings, nil
}

func (m *ManagerClient) Stop(id string) (*domain.Recording, error) {
	return m.StopContext(context.Background(), id)
}

func (m *ManagerClient) StopContext(ctx context.Context, id string) (*domain.Recording, error) {
	var recording domain.Recording
	if err := m.client.Call(ctx, OperationStop, RecordingIDRequest{RecordingID: id}, &recording); err != nil {
		return nil, normalizeEngineError(err)
	}
	return &recording, nil
}

func (m *ManagerClient) Delete(id string) error {
	return m.DeleteContext(context.Background(), id)
}

func (m *ManagerClient) DeleteContext(ctx context.Context, id string) error {
	var result struct {
		Deleted bool `json:"deleted"`
	}
	if err := m.client.Call(ctx, OperationDelete, RecordingIDRequest{RecordingID: id}, &result); err != nil {
		return normalizeEngineError(err)
	}
	if !result.Deleted {
		return errors.New("recorder engine did not confirm recording deletion")
	}
	return nil
}

// DeleteTerminalArchive asks this Engine to delete a terminal archive after
// the Host-side ManagerRouter has checked every generation's live inventory.
func (m *ManagerClient) DeleteTerminalArchive(ctx context.Context, id string) error {
	var result struct {
		Deleted bool `json:"deleted"`
	}
	if err := m.client.Call(ctx, OperationDeleteTerminal, RecordingIDRequest{RecordingID: id}, &result); err != nil {
		return normalizeEngineError(err)
	}
	if !result.Deleted {
		return errors.New("recorder engine did not confirm terminal archive deletion")
	}
	return nil
}

func normalizeEngineError(err error) error {
	var remote *runtimeipc.RemoteError
	if !errors.As(err, &remote) {
		return errors.New("recorder engine IPC operation failed")
	}
	switch remote.Code {
	case "generation_mismatch":
		return errors.New("recorder engine generation identity mismatch")
	case "engine_draining":
		return ErrEngineDraining
	case "not_found", "not_owned":
		return storage.ErrNotFound
	case "active_recording":
		return acquire.ErrActiveRecording
	case "list_too_large", "inventory_too_large":
		return acquire.ErrListLimit
	case "start_failed":
		return errors.New("recording could not be started")
	case "stop_failed":
		return errors.New("recording could not be stopped")
	case "delete_failed":
		return errors.New("recording could not be deleted")
	default:
		if remote.Message != "" {
			return fmt.Errorf("recorder engine: %s", remote.Message)
		}
		return errors.New("recorder engine operation failed")
	}
}

var _ interface {
	StartResolved(context.Context, string, adapterproto.MediaSource, *adapterproto.ResourceRef, string, *adapterproto.AdapterProvenance) (*domain.Recording, error)
	StartResolvedWithID(context.Context, string, string, adapterproto.MediaSource, *adapterproto.ResourceRef, string, *adapterproto.AdapterProvenance) (*domain.Recording, error)
	Get(string) (*domain.Recording, error)
	List() []*domain.Recording
	ListForManagement(context.Context, int) ([]*domain.Recording, error)
	Stop(string) (*domain.Recording, error)
	Delete(string) error
} = (*ManagerClient)(nil)
