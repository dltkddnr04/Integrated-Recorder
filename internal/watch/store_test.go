package watch

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStoreSeparatesSecretsAndPersistsPrivateDefinitionAndRuntime(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	def := Definition{ID: id, AdapterID: "fixture", Input: json.RawMessage(`{"source_url":"https://example.test"}`), Enabled: true, PreviewMode: "segment", CheckIntervalSeconds: 10, CreatedAt: now, UpdatedAt: now}
	runtime := Runtime{State: StateOffline, LastErrorCode: "safe_code"}
	secret := "do-not-persist-in-definition"
	if err := store.Create(def, map[string]string{"password": secret}, runtime); err != nil {
		t.Fatal(err)
	}

	definitionBytes, err := os.ReadFile(store.definitionPath(id))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(definitionBytes), secret) || strings.Contains(string(definitionBytes), "password\"") {
		t.Fatalf("definition contains a secret: %s", definitionBytes)
	}
	for _, path := range []string{store.root, store.definitionRoot, store.runtimeRoot, store.secretRoot, store.eventRoot, store.relationRoot} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("directory %s mode = %o, want 700", path, info.Mode().Perm())
		}
	}
	for _, path := range []string{store.definitionPath(id), store.runtimePath(id), store.secretPath(id), store.eventPath(id)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("file %s mode = %o, want 600", path, info.Mode().Perm())
		}
	}

	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	gotDef, gotRuntime, secrets, err := reopened.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if gotDef.ID != id || gotRuntime.State != StateOffline || secrets["password"] != secret {
		t.Fatalf("reopened watch def=%#v runtime=%#v secrets=%#v", gotDef, gotRuntime, secrets)
	}
}

func TestWatchViewsNeverReturnSecretValuesAndOmitListInput(t *testing.T) {
	adapter := &fakeAdapterRuntime{}
	manager := newFakeRecordingManager()
	service, view := newTestService(t, adapter, manager, Options{})
	secret := "private-watch-secret"
	if err := service.store.Update(view.Definition, map[string]string{"password": secret}, true); err != nil {
		t.Fatal(err)
	}
	view, err := service.Get(view.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("watch view leaked input secret: %s", encoded)
	}
	items := service.List()
	if len(items) != 1 || items[0].Input != nil {
		t.Fatalf("watch list should omit input, got %#v", items)
	}
	encoded, err = json.Marshal(items[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"input":null`) {
		t.Fatalf("watch list emits input:null rather than omitting it: %s", encoded)
	}
}

func TestDueIDsAvoidSecretFileReadsAndInputClones(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("b", 32)
	now := time.Now().UTC()
	def := Definition{ID: id, AdapterID: "fixture", Input: json.RawMessage(`{"large":"input"}`), Enabled: true, PreviewMode: "disabled", CheckIntervalSeconds: 10, CreatedAt: now, UpdatedAt: now}
	if err := store.Create(def, map[string]string{"password": "x"}, Runtime{State: StateOffline}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.secretPath(id), []byte("invalid secret json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := store.DueIDs(now.Add(time.Hour))
	if len(got) != 1 || got[0] != id {
		t.Fatalf("due IDs = %#v", got)
	}
	if snapshots := store.Snapshots(); len(snapshots) != 1 || string(snapshots[0].Definition.Input) != string(def.Input) {
		t.Fatalf("metadata snapshot = %#v", snapshots)
	}
}

func TestWatchSecretAggregateBoundAndPathValidation(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.Get("../escape"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid ID error = %v", err)
	}
	id := strings.Repeat("c", 32)
	now := time.Now().UTC()
	def := Definition{ID: id, AdapterID: "fixture", Input: json.RawMessage(`{}`), Enabled: true, PreviewMode: "disabled", CheckIntervalSeconds: 10, CreatedAt: now, UpdatedAt: now}
	if err := store.Create(def, map[string]string{"password": strings.Repeat("x", MaxSecretBytes+1)}, Runtime{State: StateChecking}); err == nil {
		t.Fatal("oversized input secret accepted")
	}
}

func TestDeleteAttemptsAllCleanupAfterUnsafeRuntimeSidecar(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("d", 32)
	recordingID := strings.Repeat("e", 32)
	now := time.Now().UTC()
	definition := Definition{ID: id, AdapterID: "fixture", Input: json.RawMessage(`{}`), Enabled: true, PreviewMode: "disabled", CheckIntervalSeconds: DefaultIntervalSeconds, CreatedAt: now, UpdatedAt: now}
	secret := "watch-secret-to-remove"
	if err := store.Create(definition, map[string]string{"password": secret}, Runtime{State: StateOffline}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddEvent(id, Event{Type: "watch_enabled", At: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRelation(RecordingRelation{RecordingID: recordingID, WatchID: id, PartIndex: 1, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	// Replace the runtime sidecar with an unsafe directory. Delete must retain
	// the regular-file/symlink protections while continuing cleanup of secrets,
	// events, and recording relations.
	if err := os.Remove(store.runtimePath(id)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(store.runtimePath(id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(id); err == nil {
		t.Fatal("Delete succeeded despite unsafe runtime sidecar")
	}
	if _, ok := store.document.Definitions[id]; ok {
		t.Fatal("logical deletion left Watch definition in memory")
	}
	if _, ok := store.document.Runtimes[id]; ok {
		t.Fatal("logical deletion left runtime index in memory")
	}
	if _, ok := store.document.Events[id]; ok {
		t.Fatal("logical deletion left event index in memory")
	}
	if _, ok := store.secretSet[id]; ok {
		t.Fatal("logical deletion left secret configuration index in memory")
	}
	if _, _, _, err := store.Get(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after logical delete error = %v, want not found", err)
	}
	for name, path := range map[string]string{
		"event":    store.eventPath(id),
		"secret":   store.secretPath(id),
		"relation": store.relationPath(recordingID),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s cleanup file still exists: err=%v", name, err)
		}
	}
	if _, err := os.Lstat(store.runtimePath(id)); err != nil {
		t.Errorf("unsafe runtime directory should not have been followed or removed: %v", err)
	}
	if store.secretBytes != 0 {
		t.Errorf("secret byte index = %d, want 0", store.secretBytes)
	}
}
