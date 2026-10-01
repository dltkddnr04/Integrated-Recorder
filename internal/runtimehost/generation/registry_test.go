package generation

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegistryLifecyclePersistenceAndRollbackPinning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "state", "generations.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Snapshot(); got.SchemaVersion != SchemaVersion || len(got.Generations) != 0 || len(got.Leases) != 0 {
		t.Fatalf("unexpected initial snapshot: %+v", got)
	}

	stageReady(t, r, generationOne)
	if err := r.Activate(generationOne); err != nil {
		t.Fatal(err)
	}
	if err := r.FinalizeActivation(generationOne); err != nil {
		t.Fatal(err)
	}
	leaseOne := Lease{RecordingID: recordingOne, EngineGeneration: generationOne, WorkerInstance: workerOne, StartedAt: time.Now().UTC()}
	if err := r.PinRecording(leaseOne); err != nil {
		t.Fatal(err)
	}

	stageReady(t, r, generationTwo)
	if err := r.Activate(generationTwo); err != nil {
		t.Fatal(err)
	}
	if err := r.FinalizeActivation(generationTwo); err != nil {
		t.Fatal(err)
	}
	state := r.Snapshot()
	if state.ActiveGenerationID != generationTwo || state.PreviousGenerationID != generationOne || state.Generations[generationOne].State != StateDraining {
		t.Fatalf("activation did not drain old generation: %+v", state)
	}
	if got := state.Leases[recordingOne].EngineGeneration; got != generationOne {
		t.Fatalf("activation moved existing lease to %s", got)
	}

	leaseTwo := Lease{RecordingID: recordingTwo, EngineGeneration: generationTwo, WorkerInstance: workerTwo, StartedAt: time.Now().UTC()}
	if err := r.PinRecording(leaseTwo); err != nil {
		t.Fatal(err)
	}
	if err := r.Rollback(); err != nil {
		t.Fatal(err)
	}
	state = r.Snapshot()
	if state.ActiveGenerationID != generationOne || state.PreviousGenerationID != generationTwo || state.Generations[generationTwo].State != StateDraining {
		t.Fatalf("rollback did not restore default generation: %+v", state)
	}
	if state.Leases[recordingOne].EngineGeneration != generationOne || state.Leases[recordingTwo].EngineGeneration != generationTwo {
		t.Fatalf("rollback moved existing leases: %+v", state.Leases)
	}
	if err := r.PinRecording(Lease{RecordingID: recordingThree, EngineGeneration: generationTwo, WorkerInstance: workerThree, StartedAt: time.Now().UTC()}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("new lease on non-active generation: got %v", err)
	}

	if err := r.ReleaseRecording(recordingTwo, generationOne); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("mismatched release: got %v", err)
	}
	if err := r.Retire(generationTwo); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("retired a generation with a live lease: got %v", err)
	}
	if err := r.ReleaseRecording(recordingTwo, generationTwo); err != nil {
		t.Fatal(err)
	}
	if err := r.Retire(generationTwo); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("retired the previous rollback generation: got %v", err)
	}
	stageReady(t, r, generationThree)
	if err := r.Activate(generationThree); err != nil {
		t.Fatal(err)
	}
	if err := r.FinalizeActivation(generationThree); err != nil {
		t.Fatal(err)
	}
	if err := r.Retire(generationTwo); err != nil {
		t.Fatal(err)
	}
	if got := r.Snapshot().PreviousGenerationID; got != generationOne {
		t.Fatalf("activation lost the current rollback target %q", got)
	}
	if err := r.RemoveRetired(generationTwo); err != nil {
		t.Fatalf("remove safely retired generation: %v", err)
	}

	reloaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	state = reloaded.Snapshot()
	if state.ActiveGenerationID != generationThree {
		t.Fatalf("reload lost lifecycle state: %+v", state)
	}
	if _, exists := state.Generations[generationTwo]; exists {
		t.Fatal("reload retained a removed retired generation")
	}
	if _, ok := state.Leases[recordingOne]; !ok {
		t.Fatal("reload lost active recording lease")
	}
}

func TestPreviousGenerationCannotBeRetiredOrRemoved(t *testing.T) {
	r := openActivated(t, generationOne)
	stageReady(t, r, generationTwo)
	if err := r.Activate(generationTwo); err != nil {
		t.Fatal(err)
	}
	if err := r.FinalizeActivation(generationTwo); err != nil {
		t.Fatal(err)
	}
	if err := r.Retire(generationOne); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("previous rollback target retired: %v", err)
	}
	if err := r.RemoveRetired(generationOne); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("previous rollback target removed: %v", err)
	}
}

func TestDormantPreviousGenerationPersistsAndCanBeReactivated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generations.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	stageReady(t, r, generationOne)
	if err := r.Activate(generationOne); err != nil {
		t.Fatal(err)
	}
	if err := r.FinalizeActivation(generationOne); err != nil {
		t.Fatal(err)
	}
	stageReady(t, r, generationTwo)
	if err := r.Activate(generationTwo); err != nil {
		t.Fatal(err)
	}
	if err := r.FinalizeActivation(generationTwo); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkEngineDormant(generationOne); err != nil {
		t.Fatalf("MarkEngineDormant(): %v", err)
	}
	if err := r.Retire(generationOne); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("dormant rollback target was retired: %v", err)
	}

	reloaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := reloaded.Snapshot()
	previous := snapshot.Generations[generationOne]
	if snapshot.PreviousGenerationID != generationOne || previous.State != StateDraining || !previous.EngineDormant {
		t.Fatalf("dormant rollback target was not preserved across reload: %+v", snapshot)
	}
	if err := reloaded.Rollback(); err != nil {
		t.Fatalf("Rollback(): %v", err)
	}
	snapshot = reloaded.Snapshot()
	if snapshot.ActiveGenerationID != generationOne || snapshot.Generations[generationOne].EngineDormant {
		t.Fatalf("rollback did not reactivate the dormant Engine generation: %+v", snapshot)
	}
}

func TestDormantGenerationRequiresNoRecordingLease(t *testing.T) {
	r := openActivated(t, generationOne)
	stageReady(t, r, generationTwo)
	if err := r.Activate(generationTwo); err != nil {
		t.Fatal(err)
	}
	if err := r.FinalizeActivation(generationTwo); err != nil {
		t.Fatal(err)
	}
	lease := Lease{RecordingID: recordingOne, EngineGeneration: generationOne, WorkerInstance: workerOne, StartedAt: time.Now().UTC()}
	// PinRecording only accepts the active generation, so a confirmed Engine
	// inventory is the intended source of a lease on a draining Engine.
	if err := r.ReconcileInventory(EngineInventory{Confirmed: true, EngineGeneration: generationOne, WorkerInstance: workerOne, ObservedAt: time.Now().UTC(), Recordings: []InventoryRecording{{RecordingID: recordingOne, StartedAt: lease.StartedAt}}}); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkEngineDormant(generationOne); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Engine with an active lease became dormant: %v", err)
	}
}

func TestAbortActivationRestoresPriorActiveAndRollbackTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generations.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{generationOne, generationTwo} {
		stageReady(t, r, id)
		if err := r.Activate(id); err != nil {
			t.Fatal(err)
		}
		if err := r.FinalizeActivation(id); err != nil {
			t.Fatal(err)
		}
	}
	stageReady(t, r, generationThree)
	if err := r.Activate(generationThree); err != nil {
		t.Fatal(err)
	}
	state := r.Snapshot()
	if state.ActivationPreviousGenerationID != generationOne {
		t.Fatalf("pre-activation rollback target = %q, want %q", state.ActivationPreviousGenerationID, generationOne)
	}
	if err := r.AbortActivation(generationThree, generationTwo); err != nil {
		t.Fatal(err)
	}
	state = r.Snapshot()
	if state.ActiveGenerationID != generationTwo || state.PreviousGenerationID != generationOne || state.ActivationPreviousGenerationID != "" {
		t.Fatalf("aborted activation did not restore generation pointers: %+v", state)
	}
	if state.Generations[generationThree].State != StateFailed {
		t.Fatalf("failed candidate state = %q, want failed", state.Generations[generationThree].State)
	}
	reloaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Snapshot(); got.ActiveGenerationID != generationTwo || got.PreviousGenerationID != generationOne {
		t.Fatalf("aborted activation did not survive reload: %+v", got)
	}
}

func TestRegistryRejectsInvalidTransitionsAndIdentifiers(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "registry.json"))
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Stage(testGeneration("../escape", StateStaging)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("unsafe identifier accepted: %v", err)
	}
	if err := r.MarkReady(generationOne); !errors.Is(err, ErrGenerationNotFound) {
		t.Fatalf("unknown generation was accepted: %v", err)
	}
	if err := r.Activate(generationOne); !errors.Is(err, ErrGenerationNotFound) {
		t.Fatalf("unknown generation activated: %v", err)
	}
	if err := r.Stage(testGeneration(generationOne, StateReady)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("generation skipped staging state: %v", err)
	}
	if err := r.Stage(testGeneration(generationOne, StateStaging)); err != nil {
		t.Fatal(err)
	}
	if err := r.Activate(generationOne); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("unready generation activated: %v", err)
	}
	if err := r.MarkReady(generationOne); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("candidate skipped verification: %v", err)
	}
	if err := r.Stage(testGeneration(generationTwo, StateStaging)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second staged candidate accepted: %v", err)
	}
}

func TestRegistryLeaseValidationAndDuplicateRejection(t *testing.T) {
	r := openActivated(t, generationOne)
	lease := Lease{RecordingID: recordingOne, EngineGeneration: generationOne, WorkerInstance: workerOne, StartedAt: time.Now().UTC()}
	if err := r.PinRecording(lease); err != nil {
		t.Fatal(err)
	}
	if err := r.PinRecording(lease); !errors.Is(err, ErrLeaseExists) {
		t.Fatalf("duplicate lease accepted: %v", err)
	}
	if err := r.ReleaseRecording(recordingOne, generationTwo); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("wrong generation released lease: %v", err)
	}
	if err := r.ReleaseRecording(recordingOne, generationOne); err != nil {
		t.Fatal(err)
	}
	if err := r.ReleaseRecording(recordingOne, generationOne); !errors.Is(err, ErrLeaseNotFound) {
		t.Fatalf("missing lease release result: %v", err)
	}
	if err := r.PinRecording(Lease{RecordingID: recordingTwo, EngineGeneration: generationOne, WorkerInstance: "bad/id", StartedAt: time.Now()}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("unsafe worker identity accepted: %v", err)
	}
}

func TestRegistryPersistenceFailureDoesNotPublishMemory(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	before := r.Snapshot()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	r.path = filepath.Join(blocker, "registry.json")
	if err := r.Stage(testGeneration(generationOne, StateStaging)); err == nil {
		t.Fatal("expected persistence failure")
	}
	if got := r.Snapshot(); len(got.Generations) != len(before.Generations) || got.StagedGenerationID != before.StagedGenerationID {
		t.Fatalf("failed persistence published memory state: before=%+v after=%+v", before, got)
	}
}

func TestRegistryStrictLoadAndFilePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime", "state")
	path := filepath.Join(dir, "generations.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Snapshot().SchemaVersion != SchemaVersion {
		t.Fatal("missing schema version")
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("registry directory permissions: info=%v err=%v", info, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("registry file permissions: info=%v err=%v", info, err)
	}

	if err := os.WriteFile(path, []byte(`{"schema_version":1,"schema_version":1,"generations":{},"leases":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("duplicate JSON field accepted: %v", err)
	}
}

func TestRegistryRejectsSymlinkStateFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "outside.json")
	if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "registry.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := Open(link); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("symlink state file accepted: %v", err)
	}
}

func TestSnapshotMapsAreIndependent(t *testing.T) {
	r := openActivated(t, generationOne)
	snapshot := r.Snapshot()
	snapshot.Generations[generationOne] = Generation{}
	snapshot.Leases[recordingOne] = Lease{}
	if got := r.Snapshot().Generations[generationOne].ID; got != generationOne {
		t.Fatalf("caller mutated registry generation: %q", got)
	}
}

func TestAdapterSetIdentityPersistsAndOldEmptyIdentityLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generations.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	withSet := testGeneration(generationOne, StateStaging)
	withSet.AdapterSetID = strings.Repeat("a", 64)
	if err := r.Stage(withSet); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Snapshot().Generations[generationOne].AdapterSetID; got != withSet.AdapterSetID {
		t.Fatalf("adapter set identity did not survive persistence: %q", got)
	}

	legacy := testGeneration(generationTwo, StateStaging)
	if err := r.Fail(generationOne); err != nil {
		t.Fatal(err)
	}
	if err := r.Stage(legacy); err != nil {
		t.Fatal(err)
	}
	legacyReload, err := Open(path)
	if err != nil {
		t.Fatalf("old generation without adapter_set_id failed to load: %v", err)
	}
	if got := legacyReload.Snapshot().Generations[generationTwo].AdapterSetID; got != "" {
		t.Fatalf("legacy empty adapter set identity changed to %q", got)
	}
}

func TestAdapterSetIdentityMustBeCanonicalSHA256(t *testing.T) {
	for _, value := range []string{"not-a-digest", strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("a", 65)} {
		item := testGeneration(generationOne, StateStaging)
		item.AdapterSetID = value
		if err := validateGeneration(item); err == nil {
			t.Errorf("noncanonical adapter set identity %q was accepted", value)
		}
	}
	legacy := testGeneration(generationOne, StateStaging)
	if err := validateGeneration(legacy); err != nil {
		t.Fatalf("legacy generation with omitted adapter set was rejected: %v", err)
	}
}

func TestReconcileInventoryCreatesAndRefreshesLeases(t *testing.T) {
	r := openActivated(t, generationOne)
	now := time.Now().UTC()
	started := now.Add(-time.Minute)
	inventory := EngineInventory{
		Confirmed: true, EngineGeneration: generationOne, WorkerInstance: workerOne, ObservedAt: now,
		Recordings: []InventoryRecording{{RecordingID: recordingOne, StartedAt: started}},
	}
	if err := r.ReconcileInventory(inventory); err != nil {
		t.Fatal(err)
	}
	lease, ok := r.Snapshot().Leases[recordingOne]
	if !ok || lease.RecordingID != recordingOne || lease.EngineGeneration != generationOne || lease.WorkerInstance != workerOne || !lease.StartedAt.Equal(started) {
		t.Fatalf("inventory lease was not projected exactly: %+v", lease)
	}

	// A confirmed empty inventory from this Engine releases only its own lease.
	if err := r.ReconcileInventory(EngineInventory{
		Confirmed: true, EngineGeneration: generationOne, WorkerInstance: workerOne, ObservedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Snapshot().Leases[recordingOne]; ok {
		t.Fatal("terminal recording remained leased after confirmed omission")
	}
}

func TestReconcileDrainingInventoryPreservesOtherGenerationLeases(t *testing.T) {
	r := openActivated(t, generationOne)
	now := time.Now().UTC()
	if err := r.ReconcileInventory(EngineInventory{
		Confirmed: true, EngineGeneration: generationOne, WorkerInstance: workerOne, ObservedAt: now,
		Recordings: []InventoryRecording{{RecordingID: recordingOne, StartedAt: now.Add(-time.Minute)}},
	}); err != nil {
		t.Fatal(err)
	}
	stageReady(t, r, generationTwo)
	if err := r.Activate(generationTwo); err != nil {
		t.Fatal(err)
	}
	if err := r.ReconcileInventories([]EngineInventory{
		{Confirmed: true, EngineGeneration: generationOne, WorkerInstance: workerOne, ObservedAt: now.Add(time.Second), Recordings: []InventoryRecording{{RecordingID: recordingOne, StartedAt: now.Add(-time.Minute)}}},
		{Confirmed: true, EngineGeneration: generationTwo, WorkerInstance: workerTwo, ObservedAt: now.Add(time.Second), Recordings: []InventoryRecording{{RecordingID: recordingTwo, StartedAt: now}}},
	}); err != nil {
		t.Fatal(err)
	}
	leases := r.Snapshot().Leases
	if len(leases) != 2 || leases[recordingOne].EngineGeneration != generationOne || leases[recordingTwo].EngineGeneration != generationTwo {
		t.Fatalf("active/draining leases were not preserved: %+v", leases)
	}
	if err := r.ReconcileInventory(EngineInventory{Confirmed: true, EngineGeneration: generationTwo, WorkerInstance: workerTwo, ObservedAt: now.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	leases = r.Snapshot().Leases
	if len(leases) != 1 || leases[recordingOne].EngineGeneration != generationOne {
		t.Fatalf("one Engine inventory released another generation's lease: %+v", leases)
	}
}

func TestReconcileInventoryRejectsMalformedDuplicateAndCrossGenerationLeases(t *testing.T) {
	r := openActivated(t, generationOne)
	now := time.Now().UTC()
	valid := EngineInventory{Confirmed: true, EngineGeneration: generationOne, WorkerInstance: workerOne, ObservedAt: now}
	malformed := valid
	malformed.Confirmed = false
	if err := r.ReconcileInventory(malformed); !errors.Is(err, ErrInvalidInventory) {
		t.Fatalf("unconfirmed inventory accepted: %v", err)
	}
	malformed = valid
	malformed.Recordings = []InventoryRecording{{RecordingID: recordingOne, StartedAt: now}, {RecordingID: recordingOne, StartedAt: now}}
	if err := r.ReconcileInventory(malformed); !errors.Is(err, ErrInvalidInventory) {
		t.Fatalf("duplicate recording accepted: %v", err)
	}
	if len(r.Snapshot().Leases) != 0 {
		t.Fatal("malformed inventory mutated leases")
	}

	if err := r.ReconcileInventory(EngineInventory{
		Confirmed: true, EngineGeneration: generationOne, WorkerInstance: workerOne, ObservedAt: now,
		Recordings: []InventoryRecording{{RecordingID: recordingOne, StartedAt: now.Add(-time.Second)}},
	}); err != nil {
		t.Fatal(err)
	}
	stageReady(t, r, generationTwo)
	if err := r.Activate(generationTwo); err != nil {
		t.Fatal(err)
	}
	conflict := EngineInventory{Confirmed: true, EngineGeneration: generationTwo, WorkerInstance: workerTwo, ObservedAt: now,
		Recordings: []InventoryRecording{{RecordingID: recordingOne, StartedAt: now}}}
	if err := r.ReconcileInventory(conflict); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("cross-generation lease moved: %v", err)
	}
	if got := r.Snapshot().Leases[recordingOne].EngineGeneration; got != generationOne {
		t.Fatalf("conflicting inventory moved lease to %s", got)
	}
}

func TestReconcileInventoriesPersistenceFailureDoesNotPublish(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	stageReady(t, r, generationOne)
	if err := r.Activate(generationOne); err != nil {
		t.Fatal(err)
	}
	before := r.Snapshot()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	r.path = filepath.Join(blocker, "registry.json")
	now := time.Now().UTC()
	err = r.ReconcileInventory(EngineInventory{Confirmed: true, EngineGeneration: generationOne, WorkerInstance: workerOne, ObservedAt: now,
		Recordings: []InventoryRecording{{RecordingID: recordingOne, StartedAt: now.Add(-time.Second)}}})
	if err == nil {
		t.Fatal("expected registry persistence failure")
	}
	if got := r.Snapshot(); len(got.Leases) != len(before.Leases) || got.ActiveGenerationID != before.ActiveGenerationID {
		t.Fatalf("failed persistence published inventory: before=%+v after=%+v", before, got)
	}
}

func openActivated(t *testing.T, id string) *Registry {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	stageReady(t, r, id)
	if err := r.Activate(id); err != nil {
		t.Fatal(err)
	}
	return r
}

func stageReady(t *testing.T, r *Registry, id string) {
	t.Helper()
	if err := r.Stage(testGeneration(id, StateStaging)); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkVerified(id); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkReady(id); err != nil {
		t.Fatal(err)
	}
}

func testGeneration(id string, state State) Generation {
	return Generation{
		ID: id, Version: "1.2.3", Commit: "0123456789abcdef", InstalledAt: time.Now().UTC(), State: state,
		ControlProtocol: 1, EngineProtocol: 1,
		ArchiveReadCompatibility: CompatibilityRange{Minimum: 1, Maximum: 3}, ArchiveWriteEpoch: 2,
	}
}

const (
	generationOne   = "11111111111111111111111111111111"
	generationTwo   = "22222222222222222222222222222222"
	generationThree = "33333333333333333333333333333333"
	recordingOne    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	recordingTwo    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	recordingThree  = "cccccccccccccccccccccccccccccccc"
	workerOne       = "dddddddddddddddddddddddddddddddd"
	workerTwo       = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	workerThree     = "ffffffffffffffffffffffffffffffff"
)
