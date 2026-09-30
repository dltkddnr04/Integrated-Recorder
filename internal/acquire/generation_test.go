package acquire_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/acquire"
	"github.com/dltkddnr04/integrated-recorder/internal/domain"
	"github.com/dltkddnr04/integrated-recorder/internal/storage"
)

func TestFreshManagerObservesActiveArchiveWithoutRecoveryOrOwnership(t *testing.T) {
	root := t.TempDir()
	store, err := storage.New(root)
	if err != nil {
		t.Fatal(err)
	}
	const id = "0123456789abcdef0123456789abcdef"
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.CreateRecording(&domain.Recording{FormatVersion: 1, ID: id, Title: "owned by older engine", State: domain.StateRecording, CreatedAt: now, StartedAt: now, Tracks: map[string]*domain.Track{"main": {ID: "main", PendingSequences: []uint64{9}}}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "recordings", id, "recording.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := acquire.NewManagerWithMode(store, nil, nil, nil, acquire.FreshGeneration)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	got, err := manager.Get(id)
	if err != nil || got.State != domain.StateRecording {
		t.Fatalf("fresh manager Get = %#v, %v", got, err)
	}
	rows, err := manager.ListForManagement(context.Background(), 10)
	if err != nil || len(rows) != 1 || rows[0].State != domain.StateRecording {
		t.Fatalf("fresh manager list = %#v, %v", rows, err)
	}
	if owned, err := manager.OwnedStates(10); err != nil || len(owned) != 0 {
		t.Fatalf("fresh manager claimed old engine entries: %#v", owned)
	}
	if err := manager.Delete(id); err == nil {
		t.Fatal("fresh manager deleted an archive it did not own")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("fresh manager startup or read changed pre-existing recording.json")
	}
}

func TestRecoveringManagerRetainsExistingRecoveryBehavior(t *testing.T) {
	root := t.TempDir()
	store, err := storage.New(root)
	if err != nil {
		t.Fatal(err)
	}
	const id = "1123456789abcdef0123456789abcdef"
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.CreateRecording(&domain.Recording{FormatVersion: 1, ID: id, State: domain.StateRecording, CreatedAt: now, StartedAt: now, Tracks: map[string]*domain.Track{"main": {ID: "main", PendingSequences: []uint64{9}}}}); err != nil {
		t.Fatal(err)
	}
	manager, err := acquire.NewManagerWithMode(store, nil, nil, nil, acquire.RecoverExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	got, err := manager.Get(id)
	if err != nil || got.State != domain.StateInterrupted || got.StoppedAt == nil || len(got.Gaps) != 1 {
		t.Fatalf("recovering manager did not retain restart recovery behavior: %#v, %v", got, err)
	}
}
