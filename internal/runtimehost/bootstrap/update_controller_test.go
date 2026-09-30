package bootstrap

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/buildinfo"
	"github.com/dltkddnr04/integrated-recorder/internal/controlplane"
	"github.com/dltkddnr04/integrated-recorder/internal/recorderengine"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/generation"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/httpapi"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/install"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/leases"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/release"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/resources"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/supervisor"
)

type controllerFixture struct {
	controller *updateController
	registry   *generation.Registry
	sup        *controllerSupervisor
	manifest   release.Manifest
	publicKey  ed25519.PublicKey
	privateKey ed25519.PrivateKey
	source     *controllerSource
	detacher   *controllerDetacher
	root       string
	initialID  string
}

func newControllerFixture(t *testing.T, badSignature bool) *controllerFixture {
	t.Helper()
	tempRoot := "/tmp"
	if runtime.GOOS == "darwin" {
		tempRoot = "/private/tmp"
	}
	root, err := os.MkdirTemp(tempRoot, "ir-update-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() {
				_ = os.Chmod(path, 0700)
			} else if entry.Type()&os.ModeSymlink == 0 {
				_ = os.Chmod(path, 0600)
			}
			return nil
		})
		_ = os.RemoveAll(root)
	})
	dataDir := filepath.Join(root, "data")
	if err := os.Mkdir(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "bundle")
	if err := os.Mkdir(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"control-plane", "recorder-engine"} {
		if err := os.WriteFile(filepath.Join(bundle, name), []byte("fixture"), 0500); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := generation.Open(filepath.Join(dataDir, "runtime", "state", "generations.json"))
	if err != nil {
		t.Fatal(err)
	}
	initialID := strings.Repeat("a", 32)
	initial := generation.Generation{ID: initialID, Version: "1.0.0", Commit: strings.Repeat("a", 40), InstalledAt: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC), State: generation.StateStaging, ControlProtocol: 1, EngineProtocol: 1, ArchiveReadCompatibility: generation.CompatibilityRange{Minimum: 1, Maximum: 1}, ArchiveWriteEpoch: 1}
	if err := registry.Stage(initial); err != nil {
		t.Fatal(err)
	}
	if err := registry.MarkVerified(initialID); err != nil {
		t.Fatal(err)
	}
	if err := registry.MarkReady(initialID); err != nil {
		t.Fatal(err)
	}
	if err := registry.Activate(initialID); err != nil {
		t.Fatal(err)
	}
	if err := registry.FinalizeActivation(initialID); err != nil {
		t.Fatal(err)
	}

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest, manifestBytes, signature, payloads := signedTestRelease(t, private)
	if badSignature {
		signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	}
	src := &controllerSource{manifest: manifestBytes, signature: signature, payloads: payloads}
	compatibility := compatibilityForTest()
	installer, err := install.NewInstaller(filepath.Join(dataDir, "runtime"), map[string]ed25519.PublicKey{"test-key": public}, compatibility)
	if err != nil {
		t.Fatal(err)
	}
	resourceCoordinator, err := resources.New(resources.Limits{GlobalBufferBytes: 100, PerRecordingBufferBytes: 100, QueueObjects: 4, WriterConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig()
	config.DataDir, config.BundleDir = dataDir, bundle
	config.ListenAddr = "127.0.0.1:8080"
	sup := &controllerSupervisor{activeControl: initialID, generations: map[string]supervisor.GenerationSnapshot{
		initialID: {ID: initialID, Engine: supervisor.ProcessSnapshot{State: supervisor.ProcessReady}, Control: supervisor.ProcessSnapshot{State: supervisor.ProcessReady}, ControlActive: true},
	}}
	readiness := &controllerReadiness{}
	lifecycle := &controllerLifecycle{}
	events := &controllerEventLog{}
	drain := &controllerDrain{events: events, active: make(map[string]int)}
	detacher := &controllerDetacher{events: events}
	sup.events = events
	initialSocket := filepath.Join(root, "engine.sock")
	initialToken := filepath.Join(root, "engine.token")
	resourceSocket := "/tmp/ir-resource.sock"
	resourceToken := filepath.Join(root, "resources.token")
	if err := os.WriteFile(initialToken, bytes.Repeat([]byte{'x'}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resourceToken, bytes.Repeat([]byte{'r'}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	identity := buildinfo.Info{Version: "1.0.0", Commit: strings.Repeat("a", 40), BuildTime: "2026-09-29T00:00:00Z", ReleaseChannel: "stable", RuntimeProtocolVersion: buildinfo.RuntimeProtocolVersion}
	controller, err := newUpdateController(updateControllerOptions{
		Config: config, HostBuild: identity, ApplicationBuild: identity, Registry: registry,
		Supervisor: sup, Readiness: readiness, Lifecycle: lifecycle, Drain: drain,
		EngineDetacher: detacher,
		Coordinator:    resourceCoordinator, Installer: installer,
		TrustedKeys: map[string]ed25519.PublicKey{"test-key": public}, Compatibility: compatibility,
		SourceFactory:   func() (install.Source, error) { return src, nil },
		ControlTarget:   testControlTarget,
		CatalogWriter:   writeTestCatalog,
		Engines:         []engineAttachment{{generationID: initialID, releaseDir: bundle, socketPath: initialSocket, tokenPath: initialToken, instanceID: strings.Repeat("b", 32)}},
		ActiveControlID: initialID, ResourceSocket: resourceSocket, ResourceTokenPath: resourceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &controllerFixture{controller: controller, registry: registry, sup: sup, manifest: manifest, publicKey: public, privateKey: private, source: src, detacher: detacher, root: dataDir, initialID: initialID}
}

func TestUpdateControllerDevelopmentBuildFailsClosed(t *testing.T) {
	root := t.TempDir()
	config := DefaultConfig()
	config.DataDir = filepath.Join(root, "data")
	config.BundleDir = filepath.Join(root, "bundle")
	if err := os.Mkdir(config.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(config.BundleDir, 0700); err != nil {
		t.Fatal(err)
	}
	registry, err := generation.Open(filepath.Join(config.DataDir, "runtime", "state", "generations.json"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := resources.New(resources.Limits{GlobalBufferBytes: 100, PerRecordingBufferBytes: 100, QueueObjects: 1, WriterConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := newUpdateController(updateControllerOptions{
		Config: config, HostBuild: buildinfo.Current(), ApplicationBuild: buildinfo.Current(), Registry: registry,
		Supervisor: &controllerSupervisor{}, Readiness: &controllerReadiness{}, Lifecycle: &controllerLifecycle{}, Drain: &controllerDrain{},
		Coordinator: coordinator,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := controller.Status(context.Background())
	if err != nil || status.UpdateUnavailableReason != "development_build" {
		t.Fatalf("Status() = (%+v, %v), want development_build", status, err)
	}
	if _, err := controller.Check(context.Background()); controllerErrorCode(err) != "update_unavailable" {
		t.Fatalf("Check() error = %v, want update_unavailable", err)
	}
}

func TestUpdateControllerRejectsBadSignatureWithoutChangingActiveGeneration(t *testing.T) {
	fixture := newControllerFixture(t, true)
	before := fixture.registry.Snapshot()
	if _, err := fixture.controller.Check(context.Background()); controllerErrorCode(err) != "verification_failed" {
		t.Fatalf("Check() error = %v, want verification_failed", err)
	}
	after := fixture.registry.Snapshot()
	if after.ActiveGenerationID != before.ActiveGenerationID || after.StagedGenerationID != "" || fixture.sup.startEngineCalls != 0 || fixture.sup.startControlCalls != 0 {
		t.Fatalf("invalid signature changed runtime state: before=%+v after=%+v starts=(%d,%d)", before, after, fixture.sup.startEngineCalls, fixture.sup.startControlCalls)
	}
}

func TestUpdateControllerRejectsCandidateIncompatibleWithRunningEngineEpoch(t *testing.T) {
	fixture := newControllerFixture(t, false)
	snapshot := fixture.registry.Snapshot()
	initial := snapshot.Generations[fixture.initialID]
	initial.ArchiveWriteEpoch = 2
	initial.ArchiveReadCompatibility.Maximum = 2
	snapshot.Generations[fixture.initialID] = initial
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(fixture.root, "runtime", "state", "generations.json")
	if err := os.WriteFile(registryPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := generation.Open(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.registry = registry
	fixture.controller.registry = registry
	if _, err := fixture.controller.Check(context.Background()); controllerErrorCode(err) != "release_incompatible" {
		t.Fatalf("Check() error = %v, want release_incompatible for active Engine write epoch", err)
	}
	if registry.Snapshot().ActiveGenerationID != fixture.initialID || registry.Snapshot().StagedGenerationID != "" {
		t.Fatalf("incompatible update changed generation registry: %+v", registry.Snapshot())
	}
}

func TestUpdateControllerReadinessFailureLeavesActiveGenerationUntouched(t *testing.T) {
	fixture := newControllerFixture(t, false)
	fixture.sup.failCandidateEngine = true
	if _, err := fixture.controller.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.controller.Stage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.controller.Activate(context.Background()); controllerErrorCode(err) != "candidate_not_ready" {
		t.Fatalf("Activate() error = %v, want candidate_not_ready", err)
	}
	snapshot := fixture.registry.Snapshot()
	if snapshot.ActiveGenerationID != fixture.initialID || snapshot.StagedGenerationID != "" || snapshot.Generations[snapshot.ActiveGenerationID].State != generation.StateActive {
		t.Fatalf("readiness failure changed active generation: %+v", snapshot)
	}
	fixture.sup.mu.Lock()
	controlStarts := fixture.sup.startControlCalls
	fixture.sup.mu.Unlock()
	if controlStarts != 0 || fixture.sup.Snapshot().ActiveControlGeneration != fixture.initialID {
		t.Fatalf("unready candidate was routed: Control starts=%d supervisor=%+v", controlStarts, fixture.sup.Snapshot())
	}
}

func TestUpdateControllerStageVerifiesAndDoesNotStartProcesses(t *testing.T) {
	fixture := newControllerFixture(t, false)
	fixture.source.notes = "<img src=x onerror=alert(1)> release summary"
	if _, err := fixture.controller.Check(context.Background()); err != nil {
		t.Fatalf("Check(): %v", err)
	}
	checked, err := fixture.controller.Status(context.Background())
	if err != nil || checked.AvailableRelease == nil || checked.AvailableRelease.NotesSummary != fixture.source.notes {
		t.Fatalf("available release notes summary = (%+v, %v)", checked.AvailableRelease, err)
	}
	status, err := fixture.controller.Stage(context.Background())
	if err != nil {
		t.Fatalf("Stage(): %v", err)
	}
	if status.StagedRelease == nil || status.StagedRelease.Version != fixture.manifest.ReleaseVersion || status.StagedRelease.NotesSummary != fixture.source.notes || status.VerificationState != "verified" {
		t.Fatalf("Stage() status = %+v", status)
	}
	if fixture.sup.startEngineCalls != 0 || fixture.sup.startControlCalls != 0 {
		t.Fatalf("Stage started executable(s): engine=%d control=%d", fixture.sup.startEngineCalls, fixture.sup.startControlCalls)
	}
	snapshot := fixture.registry.Snapshot()
	if snapshot.ActiveGenerationID != fixture.initialID || snapshot.StagedGenerationID == "" || snapshot.Generations[snapshot.StagedGenerationID].State != generation.StateVerified {
		t.Fatalf("Stage changed active state or failed to record verified candidate: %+v", snapshot)
	}
	installedID := install.ReleaseDirectoryID(fixture.manifest.ReleaseVersion, fixture.manifest.Commit)
	installed, err := install.InspectInstalledRelease(filepath.Join(fixture.root, "runtime"), installedID, map[string]ed25519.PublicKey{"test-key": fixture.publicKey}, compatibilityForTest())
	if err != nil || installed.Manifest.ReleaseVersion != fixture.manifest.ReleaseVersion {
		t.Fatalf("InspectInstalledRelease() = (%+v, %v)", installed, err)
	}
}

func TestUpdateControllerOperationGateSerializesChecks(t *testing.T) {
	fixture := newControllerFixture(t, false)
	entered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var sourceCalls int
	var sourceMu sync.Mutex
	fixture.controller.sourceFactory = func() (install.Source, error) {
		sourceMu.Lock()
		sourceCalls++
		call := sourceCalls
		sourceMu.Unlock()
		if call == 1 {
			close(entered)
			<-releaseFirst
		}
		return fixture.source, nil
	}
	firstDone := make(chan error, 1)
	go func() { _, err := fixture.controller.Check(context.Background()); firstDone <- err }()
	<-entered
	secondDone := make(chan error, 1)
	go func() { _, err := fixture.controller.Check(context.Background()); secondDone <- err }()
	select {
	case err := <-secondDone:
		t.Fatalf("second check returned before serialized first operation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Check(): %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second Check(): %v", err)
	}
	sourceMu.Lock()
	defer sourceMu.Unlock()
	if sourceCalls != 2 {
		t.Fatalf("source calls = %d, want serialized calls for both requests", sourceCalls)
	}
}

func TestUpdateControllerStatusDoesNotExposePrivatePathsOrDiagnostics(t *testing.T) {
	fixture := newControllerFixture(t, false)
	if _, err := fixture.controller.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := fixture.controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("Status.Validate(): %v", err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{fixture.root, "socketPath", "tokenPath", "release.json", "github.com", "private key", "signed-url"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("status leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestUpdateControllerActivateAndRollbackKeepEngineGenerationAttachments(t *testing.T) {
	fixture := newControllerFixture(t, false)
	lease := generation.Lease{RecordingID: strings.Repeat("1", 32), EngineGeneration: fixture.initialID, WorkerInstance: strings.Repeat("2", 32), StartedAt: time.Now().UTC()}
	if err := fixture.registry.PinRecording(lease); err != nil {
		t.Fatalf("PinRecording(): %v", err)
	}
	if _, err := fixture.controller.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.controller.Stage(context.Background()); err != nil {
		t.Fatal(err)
	}
	active, err := fixture.controller.Activate(context.Background())
	if err != nil {
		t.Fatalf("Activate(): %v", err)
	}
	if active.DefaultEngine == nil || active.DefaultEngine.Version != fixture.manifest.ReleaseVersion || len(fixture.controller.EngineAttachments()) != 2 {
		t.Fatalf("candidate activation did not retain both Engine attachments: status=%+v engines=%d", active, len(fixture.controller.EngineAttachments()))
	}
	fixture.sup.mu.Lock()
	var candidateSpec *supervisor.ProcessSpec
	for index := range fixture.sup.engineSpecs {
		if fixture.sup.engineSpecs[index].GenerationID == active.DefaultEngine.ID {
			copy := fixture.sup.engineSpecs[index]
			candidateSpec = &copy
			break
		}
	}
	fixture.sup.mu.Unlock()
	resourceOwner := ""
	if candidateSpec != nil {
		resourceOwner = envValue(candidateSpec.Env, "RUNTIME_RESOURCE_OWNER")
	}
	if candidateSpec == nil || envValue(candidateSpec.Env, "ENGINE_RECOVERY_MODE") != "fresh" || !strings.HasPrefix(resourceOwner, "e"+active.DefaultEngine.ID+"-") || !generationPattern.MatchString(strings.TrimPrefix(resourceOwner, "e"+active.DefaultEngine.ID+"-")) || envValue(candidateSpec.Env, "RUNTIME_RESOURCE_SOCKET_PATH") == "" || envValue(candidateSpec.Env, "RUNTIME_RESOURCE_TOKEN_FILE") == "" {
		t.Fatalf("candidate Engine did not use fresh recovery and shared Host resource coordination: %+v", candidateSpec)
	}
	fixture.sup.mu.Lock()
	var candidateControl *supervisor.ProcessSpec
	for index := range fixture.sup.controlSpecs {
		if fixture.sup.controlSpecs[index].GenerationID == active.DefaultEngine.ID {
			copy := fixture.sup.controlSpecs[index]
			candidateControl = &copy
			break
		}
	}
	fixture.sup.mu.Unlock()
	controlOwner := ""
	if candidateControl != nil {
		controlOwner = envValue(candidateControl.Env, "RUNTIME_RESOURCE_OWNER")
	}
	if candidateControl == nil || !strings.HasPrefix(controlOwner, "c"+active.DefaultEngine.ID+"-") || !generationPattern.MatchString(strings.TrimPrefix(controlOwner, "c"+active.DefaultEngine.ID+"-")) || envValue(candidateControl.Env, "RUNTIME_RESOURCE_SOCKET_PATH") == "" || envValue(candidateControl.Env, "RUNTIME_RESOURCE_TOKEN_FILE") == "" {
		t.Fatalf("candidate Control did not use fresh shared resource telemetry identity: %+v", candidateControl)
	}
	if len(fixture.registry.Snapshot().Leases) != 1 || active.DrainingGenerations[0].ActiveRecordings != 1 {
		t.Fatalf("existing Recording lease moved or disappeared across activation: %+v", active)
	}
	for _, attachment := range fixture.controller.EngineAttachments() {
		if attachment.generationID == fixture.initialID {
			continue
		}
		if attachment.generationID != active.DefaultEngine.ID {
			t.Fatalf("active Engine attachment mismatch: %+v", attachment)
		}
	}
	rolled, err := fixture.controller.Rollback(context.Background())
	if err != nil {
		t.Fatalf("Rollback(): %v", err)
	}
	if rolled.DefaultEngine == nil || rolled.DefaultEngine.ID != fixture.initialID || len(fixture.controller.EngineAttachments()) != 2 {
		t.Fatalf("rollback changed generation pins or attachments: status=%+v", rolled)
	}
	if leaseAfter, ok := fixture.registry.Snapshot().Leases[lease.RecordingID]; !ok || leaseAfter.EngineGeneration != fixture.initialID {
		t.Fatalf("rollback moved an existing Recording lease: %+v", leaseAfter)
	}
}

func TestUpdateControllerReconciliationStopsIdlePreviousEngineButRetainsRollbackRelease(t *testing.T) {
	fixture := newControllerFixture(t, false)
	defaultID := activateFixtureCandidate(t, fixture)
	fixture.controller.inventorySource = emptyInventorySource(nil, "")

	if err := fixture.controller.ReconcileLeases(context.Background()); err != nil {
		t.Fatalf("ReconcileLeases(): %v", err)
	}
	snapshot := fixture.registry.Snapshot()
	if snapshot.PreviousGenerationID != fixture.initialID {
		t.Fatalf("previous rollback target changed: %+v", snapshot)
	}
	if _, exists := snapshot.Generations[fixture.initialID]; !exists {
		t.Fatalf("previous rollback generation was removed: %+v", snapshot)
	}
	if _, attached := fixture.controller.engineAttachment(fixture.initialID); !attached {
		t.Fatal("previous rollback release attachment was removed")
	}
	if !snapshot.Generations[fixture.initialID].EngineDormant {
		t.Fatal("lease-free previous Engine was not durably marked dormant")
	}
	if !contains(fixture.sup.retireCalls, fixture.initialID) || !contains(fixture.detacher.callsEngineIDs(), fixture.initialID) {
		t.Fatalf("idle previous Engine was not detached and stopped: retire=%v detach=%v default=%s", fixture.sup.retireCalls, fixture.detacher.calls, defaultID)
	}
	if process, exists := supervisorGeneration(fixture.sup.Snapshot(), fixture.initialID); !exists || process.Engine.State != supervisor.ProcessExited {
		t.Fatalf("previous Engine process was not stopped: %+v", process)
	}
	if err := fixture.controller.ReconcileLeases(context.Background()); err != nil {
		t.Fatalf("subsequent lease reconciliation required the dormant Engine inventory: %v", err)
	}
}

func TestUpdateControllerKeepsPreviousEngineUntilItsLastLeaseEnds(t *testing.T) {
	fixture := newControllerFixture(t, false)
	lease := generation.Lease{RecordingID: strings.Repeat("1", 32), EngineGeneration: fixture.initialID, WorkerInstance: strings.Repeat("2", 32), StartedAt: time.Now().UTC()}
	if err := fixture.registry.PinRecording(lease); err != nil {
		t.Fatal(err)
	}
	defaultID := activateFixtureCandidate(t, fixture)
	fixture.controller.inventorySource = emptyInventorySource(map[string][]generation.InventoryRecording{
		fixture.initialID: {{RecordingID: lease.RecordingID, StartedAt: lease.StartedAt}},
	}, "")
	if err := fixture.controller.ReconcileLeases(context.Background()); err != nil {
		t.Fatalf("ReconcileLeases() with active previous-generation lease: %v", err)
	}
	snapshot := fixture.registry.Snapshot()
	if !generationHasLease(snapshot, fixture.initialID) || snapshot.Generations[fixture.initialID].EngineDormant {
		t.Fatalf("previous Engine with an active Recording was drained: %+v", snapshot)
	}
	if contains(fixture.sup.retireCalls, fixture.initialID) {
		t.Fatal("previous Engine was stopped while its Recording lease remained")
	}

	fixture.controller.inventorySource = emptyInventorySource(nil, "")
	if err := fixture.controller.ReconcileLeases(context.Background()); err != nil {
		t.Fatalf("ReconcileLeases() after the final lease ended: %v", err)
	}
	snapshot = fixture.registry.Snapshot()
	if generationHasLease(snapshot, fixture.initialID) || !snapshot.Generations[fixture.initialID].EngineDormant || snapshot.PreviousGenerationID != fixture.initialID {
		t.Fatalf("final lease release did not stop the Engine while preserving rollback metadata: %+v", snapshot)
	}
	if _, exists := snapshot.Generations[fixture.initialID]; !exists {
		t.Fatal("rollback generation metadata was removed")
	}
	if process, exists := supervisorGeneration(fixture.sup.Snapshot(), fixture.initialID); !exists || process.Engine.State != supervisor.ProcessExited {
		t.Fatalf("previous Engine did not exit after its final lease: %+v", process)
	}
	if snapshot.ActiveGenerationID != defaultID {
		t.Fatalf("default generation changed while draining the previous Engine: %+v", snapshot)
	}
}

func TestUpdateControllerLeaseReconciliationHoldsGenerationWithLease(t *testing.T) {
	fixture := newControllerFixture(t, false)
	lease := generation.Lease{RecordingID: strings.Repeat("1", 32), EngineGeneration: fixture.initialID, WorkerInstance: strings.Repeat("2", 32), StartedAt: time.Now().UTC()}
	if err := fixture.registry.PinRecording(lease); err != nil {
		t.Fatal(err)
	}
	activateFixtureCandidate(t, fixture)
	thirdID := strings.Repeat("3", 32)
	activateSyntheticGeneration(t, fixture, thirdID, "1.2.4", strings.Repeat("4", 40))
	fixture.controller.inventorySource = emptyInventorySource(map[string][]generation.InventoryRecording{
		fixture.initialID: {{RecordingID: lease.RecordingID, StartedAt: lease.StartedAt}},
	}, "")

	if err := fixture.controller.ReconcileLeases(context.Background()); err != nil {
		t.Fatalf("ReconcileLeases(): %v", err)
	}
	snapshot := fixture.registry.Snapshot()
	if _, exists := snapshot.Generations[fixture.initialID]; !exists || !generationHasLease(snapshot, fixture.initialID) {
		t.Fatalf("held lease generation was removed or unpinned: %+v", snapshot)
	}
	for _, retired := range fixture.sup.retireCalls {
		if retired == fixture.initialID {
			t.Fatal("Engine with a durable Recording lease was retired")
		}
	}
}

func TestUpdateControllerReconcileDetachesBeforeRetiringAndRemovesOldRelease(t *testing.T) {
	fixture := newControllerFixture(t, false)
	installedID := activateFixtureCandidate(t, fixture)
	activateSyntheticGeneration(t, fixture, strings.Repeat("3", 32), "1.2.4", strings.Repeat("4", 40))
	activeID := strings.Repeat("5", 32)
	activateSyntheticGeneration(t, fixture, activeID, "1.2.5", strings.Repeat("6", 40))
	fixture.controller.inventorySource = emptyInventorySource(nil, "")
	releaseDir := filepath.Join(fixture.root, "runtime", "releases", install.ReleaseDirectoryID(fixture.manifest.ReleaseVersion, fixture.manifest.Commit))
	if _, err := os.Lstat(releaseDir); err != nil {
		t.Fatalf("installed release missing before reconciliation: %v", err)
	}

	if err := fixture.controller.ReconcileLeases(context.Background()); err != nil {
		t.Fatalf("ReconcileLeases(): %v", err)
	}
	snapshot := fixture.registry.Snapshot()
	if _, exists := snapshot.Generations[installedID]; exists {
		t.Fatalf("retired generation metadata remains after successful cleanup: %+v", snapshot)
	}
	if _, err := os.Lstat(releaseDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("immutable release remains after retirement: %v", err)
	}
	events := fixture.detacher.events.snapshot()
	detachAt, retireAt := -1, -1
	for index, event := range events {
		if event == "detach:"+activeID+":"+installedID {
			detachAt = index
		}
		if event == "retire:"+installedID {
			retireAt = index
		}
	}
	if detachAt < 0 || retireAt < 0 || detachAt >= retireAt {
		t.Fatalf("Engine was not detached after the fresh drain proof and before retirement: events=%v", events)
	}
	if !contains(fixture.sup.retireCalls, fixture.initialID) || !contains(fixture.sup.retireCalls, installedID) {
		t.Fatalf("expected eligible old Engines to be retired: %v", fixture.sup.retireCalls)
	}
	if snapshot.PreviousGenerationID != strings.Repeat("3", 32) || snapshot.ActiveGenerationID != activeID {
		t.Fatalf("active or previous generation pointer changed during cleanup: %+v", snapshot)
	}
}

func TestUpdateControllerRetirementFailurePreservesReleaseAndRegistryMetadata(t *testing.T) {
	fixture := newControllerFixture(t, false)
	installedID := activateFixtureCandidate(t, fixture)
	activateSyntheticGeneration(t, fixture, strings.Repeat("3", 32), "1.2.4", strings.Repeat("4", 40))
	activeID := strings.Repeat("5", 32)
	activateSyntheticGeneration(t, fixture, activeID, "1.2.5", strings.Repeat("6", 40))
	fixture.controller.inventorySource = emptyInventorySource(nil, "")
	fixture.sup.retireErrors = map[string]error{installedID: errors.New("private supervisor failure")}
	releaseDir := filepath.Join(fixture.root, "runtime", "releases", install.ReleaseDirectoryID(fixture.manifest.ReleaseVersion, fixture.manifest.Commit))

	if err := fixture.controller.ReconcileLeases(context.Background()); err != nil {
		t.Fatalf("ReconcileLeases(): %v", err)
	}
	snapshot := fixture.registry.Snapshot()
	if generationInfo, exists := snapshot.Generations[installedID]; !exists || generationInfo.State != generation.StateDraining {
		t.Fatalf("failed Engine retirement changed registry state: %+v", snapshot)
	}
	if _, err := os.Lstat(releaseDir); err != nil {
		t.Fatalf("release metadata was removed after retirement failure: %v", err)
	}
	if !contains(fixture.detacher.callsEngineIDs(), installedID) {
		t.Fatalf("expected detachment attempt before failed retirement: %v", fixture.detacher.calls)
	}
}

func TestUpdateControllerLeaseInventoryFailurePreventsAllGarbageCollection(t *testing.T) {
	fixture := newControllerFixture(t, false)
	installedID := activateFixtureCandidate(t, fixture)
	activateSyntheticGeneration(t, fixture, strings.Repeat("3", 32), "1.2.4", strings.Repeat("4", 40))
	activeID := strings.Repeat("5", 32)
	activateSyntheticGeneration(t, fixture, activeID, "1.2.5", strings.Repeat("6", 40))
	fixture.controller.inventorySource = emptyInventorySource(nil, activeID)
	releaseDir := filepath.Join(fixture.root, "runtime", "releases", install.ReleaseDirectoryID(fixture.manifest.ReleaseVersion, fixture.manifest.Commit))

	if err := fixture.controller.ReconcileLeases(context.Background()); err == nil {
		t.Fatal("ReconcileLeases() succeeded despite an unconfirmed Engine inventory")
	}
	if len(fixture.sup.retireCalls) != 0 || len(fixture.detacher.calls) != 0 {
		t.Fatalf("inventory failure did not stop garbage collection: retire=%v detach=%v", fixture.sup.retireCalls, fixture.detacher.calls)
	}
	if _, exists := fixture.registry.Snapshot().Generations[installedID]; !exists {
		t.Fatal("generation metadata was removed despite an inventory failure")
	}
	if _, err := os.Lstat(releaseDir); err != nil {
		t.Fatalf("installed release was removed despite an inventory failure: %v", err)
	}
}

func activateFixtureCandidate(t *testing.T, fixture *controllerFixture) string {
	t.Helper()
	if _, err := fixture.controller.Check(context.Background()); err != nil {
		t.Fatalf("Check(): %v", err)
	}
	if _, err := fixture.controller.Stage(context.Background()); err != nil {
		t.Fatalf("Stage(): %v", err)
	}
	id := fixture.registry.Snapshot().StagedGenerationID
	if _, err := fixture.controller.Activate(context.Background()); err != nil {
		t.Fatalf("Activate(): %v", err)
	}
	return id
}

func activateSyntheticGeneration(t *testing.T, fixture *controllerFixture, id, version, commit string) {
	t.Helper()
	g := generation.Generation{
		ID: id, Version: version, Commit: commit, InstalledAt: time.Now().UTC(), State: generation.StateStaging,
		ControlProtocol: 1, EngineProtocol: 1,
		ArchiveReadCompatibility: generation.CompatibilityRange{Minimum: 1, Maximum: 1}, ArchiveWriteEpoch: 1,
	}
	for _, action := range []func() error{
		func() error { return fixture.registry.Stage(g) },
		func() error { return fixture.registry.MarkVerified(id) },
		func() error { return fixture.registry.MarkReady(id) },
		func() error { return fixture.registry.Activate(id) },
		func() error { return fixture.registry.FinalizeActivation(id) },
	} {
		if err := action(); err != nil {
			t.Fatalf("activate synthetic generation %s: %v", id, err)
		}
	}
	fixture.sup.mu.Lock()
	if previous := fixture.sup.generations[fixture.sup.activeControl]; previous.ID != "" {
		previous.ControlActive = false
		previous.Control.State = supervisor.ProcessExited
		fixture.sup.generations[previous.ID] = previous
	}
	fixture.sup.activeControl = id
	fixture.sup.generations[id] = supervisor.GenerationSnapshot{
		ID: id, Engine: supervisor.ProcessSnapshot{State: supervisor.ProcessReady},
		Control: supervisor.ProcessSnapshot{State: supervisor.ProcessReady}, ControlActive: true,
	}
	fixture.sup.mu.Unlock()
	fixture.controller.mu.Lock()
	fixture.controller.engines[id] = engineAttachment{
		generationID: id, releaseDir: fixture.controller.config.BundleDir,
		socketPath: filepath.Join(fixture.root, "runtime", "ipc", "e-"+id[:12]+".sock"),
		tokenPath:  filepath.Join(fixture.root, "runtime", "ipc", "e-"+id[:12]+".token"),
		instanceID: strings.Repeat("f", 32),
	}
	fixture.controller.mu.Unlock()
}

func emptyInventorySource(recordings map[string][]generation.InventoryRecording, failID string) leases.InventorySource {
	return leases.InventorySourceFunc(func(ctx context.Context, engine generation.Generation) (generation.EngineInventory, error) {
		if failID == engine.ID {
			return generation.EngineInventory{}, errors.New("unconfirmed test inventory")
		}
		if err := ctx.Err(); err != nil {
			return generation.EngineInventory{}, err
		}
		worker := strings.Repeat("f", 32)
		return generation.EngineInventory{
			Confirmed: true, EngineGeneration: engine.ID, WorkerInstance: worker,
			ObservedAt: time.Now().UTC(), Recordings: append([]generation.InventoryRecording(nil), recordings[engine.ID]...),
		}, nil
	})
}

func contains(values []string, item string) bool {
	for _, value := range values {
		if value == item {
			return true
		}
	}
	return false
}

func (d *controllerDetacher) callsEngineIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := make([]string, 0, len(d.calls))
	for _, call := range d.calls {
		ids = append(ids, call[1])
	}
	return ids
}

func envValue(environment []string, name string) string {
	prefix := name + "="
	for _, item := range environment {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func signedTestRelease(t *testing.T, private ed25519.PrivateKey) (release.Manifest, []byte, string, map[string][]byte) {
	t.Helper()
	payloads := map[string][]byte{
		"host.bin": []byte("host fixture executable"), "control.bin": []byte("control fixture executable"),
		"engine.bin": []byte("engine fixture executable"), "adapter.bin": []byte("adapter fixture executable"),
	}
	roles := []struct{ role, name string }{{release.RoleRuntimeHost, "host.bin"}, {release.RoleControlPlane, "control.bin"}, {release.RoleRecorderEngine, "engine.bin"}, {release.RoleAdapterRuntime, "adapter.bin"}}
	manifest := release.Manifest{
		ManifestSchemaVersion: release.ManifestSchemaVersion, ReleaseVersion: "1.1.0", Commit: strings.Repeat("c", 40),
		BuildTime: "2026-09-30T00:00:00Z", Channel: "stable", KeyID: "test-key",
		MinimumHostProtocol: 1, MaximumHostProtocol: 1, ControlProtocolVersion: 1, EngineProtocolVersion: 1,
		AdapterProtocolMinimum: 1, AdapterProtocolMaximum: 2, ArchiveReadMinimum: 1, ArchiveReadMaximum: 1,
		ArchiveWriteEpoch: 1, ManagementSchemaMinimum: 1, ManagementSchemaMaximum: 1,
		Platform: runtime.GOOS, Architecture: runtime.GOARCH,
	}
	for _, role := range roles {
		hash := sha256.Sum256(payloads[role.name])
		manifest.Artifacts = append(manifest.Artifacts, release.Artifact{Role: role.role, Filename: role.name, Size: int64(len(payloads[role.name])), SHA256: hex.EncodeToString(hash[:])})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(private, data))
	return manifest, data, signature, payloads
}

func compatibilityForTest() release.HostCompatibility {
	return release.HostCompatibility{
		Platform: runtime.GOOS, Architecture: runtime.GOARCH, RuntimeProtocolVersion: 1,
		ControlProtocolRange: release.ProtocolRange{Minimum: 1, Maximum: 1},
		EngineProtocolRange:  release.ProtocolRange{Minimum: 1, Maximum: 1},
		AdapterProtocolRange: release.ProtocolRange{Minimum: 1, Maximum: 2},
		ArchiveReadRange:     release.ProtocolRange{Minimum: 1, Maximum: 1}, ArchiveWriteEpoch: 1,
		ManagementSchemaVersion: 1,
	}
}

func testControlTarget() (string, *url.URL, error) {
	return "127.0.0.1:12345", &url.URL{Scheme: "http", Host: "127.0.0.1:12345"}, nil
}

func writeTestCatalog(path string, catalog controlplane.EngineCatalog) error {
	if catalog.Version != 1 || catalog.ActiveGenerationID == "" || len(catalog.Engines) == 0 || len(catalog.Engines) > 32 {
		return errors.New("test engine catalog is invalid")
	}
	activeFound := false
	for _, engine := range catalog.Engines {
		activeFound = activeFound || engine.GenerationID == catalog.ActiveGenerationID
	}
	if !activeFound {
		return errors.New("test engine catalog active generation is missing")
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		return err
	}
	return writeAtomicPrivate(path, data)
}

type controllerSource struct {
	manifest  []byte
	signature string
	payloads  map[string][]byte
	notes     string
}

func (s *controllerSource) ReleaseNotesSummary() string { return s.notes }

func (s *controllerSource) Manifest(ctx context.Context) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return append([]byte(nil), s.manifest...), s.signature, nil
}

func (s *controllerSource) OpenArtifact(ctx context.Context, filename string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	payload, ok := s.payloads[filename]
	if !ok {
		return nil, errors.New("missing test artifact")
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

type controllerSupervisor struct {
	mu                  sync.Mutex
	activeControl       string
	generations         map[string]supervisor.GenerationSnapshot
	startEngineCalls    int
	startControlCalls   int
	failCandidateEngine bool
	engineSpecs         []supervisor.ProcessSpec
	controlSpecs        []supervisor.ProcessSpec
	retireErrors        map[string]error
	retireCalls         []string
	events              *controllerEventLog
}

func (s *controllerSupervisor) Snapshot() supervisor.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := supervisor.Snapshot{ActiveControlGeneration: s.activeControl}
	for _, item := range s.generations {
		result.Generations = append(result.Generations, item)
	}
	return result
}

func (s *controllerSupervisor) StartEngine(_ context.Context, spec supervisor.ProcessSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startEngineCalls++
	if s.failCandidateEngine && spec.GenerationID != strings.Repeat("a", 32) {
		return supervisor.ErrCandidateNotReady
	}
	spec.Env = append([]string(nil), spec.Env...)
	s.engineSpecs = append(s.engineSpecs, spec)
	item := s.generations[spec.GenerationID]
	item.ID, item.Engine.State = spec.GenerationID, supervisor.ProcessReady
	s.generations[spec.GenerationID] = item
	return nil
}

func (s *controllerSupervisor) StartControl(_ context.Context, spec supervisor.ProcessSpec, _ *url.URL) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startControlCalls++
	spec.Env = append([]string(nil), spec.Env...)
	s.controlSpecs = append(s.controlSpecs, spec)
	item := s.generations[spec.GenerationID]
	item.ID, item.Control.State = spec.GenerationID, supervisor.ProcessReady
	s.generations[spec.GenerationID] = item
	return nil
}

func (s *controllerSupervisor) ActivateControlWith(_ context.Context, id string, beforeRoute, afterAbort func() error) error {
	s.mu.Lock()
	item, ok := s.generations[id]
	if !ok || item.Engine.State != supervisor.ProcessReady || item.Control.State != supervisor.ProcessReady {
		s.mu.Unlock()
		return supervisor.ErrCandidateNotReady
	}
	s.mu.Unlock()
	if beforeRoute != nil {
		if err := beforeRoute(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	old := s.activeControl
	s.activeControl = id
	item = s.generations[id]
	item.ControlActive = true
	s.generations[id] = item
	if old != "" && old != id {
		oldItem := s.generations[old]
		oldItem.ControlActive = false
		oldItem.Control.State = supervisor.ProcessExited
		s.generations[old] = oldItem
	}
	s.mu.Unlock()
	_ = afterAbort
	return nil
}

func (s *controllerSupervisor) StopControl(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.generations[id]
	if s.activeControl == id {
		return supervisor.ErrControlIsActive
	}
	item.Control.State = supervisor.ProcessExited
	s.generations[id] = item
	return nil
}

func (s *controllerSupervisor) RetireEngine(_ context.Context, id string, _ supervisor.EngineDrain) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retireCalls = append(s.retireCalls, id)
	if s.events != nil {
		s.events.add("retire:" + id)
	}
	if err := s.retireErrors[id]; err != nil {
		return err
	}
	item := s.generations[id]
	if s.activeControl == id {
		return supervisor.ErrEngineStillDefault
	}
	item.Engine.State = supervisor.ProcessExited
	s.generations[id] = item
	return nil
}

type controllerReadiness struct{}

func (*controllerReadiness) register(string, supervisor.Role, string, string) {}
func (*controllerReadiness) WaitReady(context.Context, supervisor.ProcessSpec, supervisor.Child) error {
	return nil
}
func (*controllerReadiness) engineIdentity(id string) (recorderengine.ReadyResult, error) {
	return recorderengine.ReadyResult{Ready: true, GenerationID: id, InstanceID: strings.Repeat("d", 32), ProtocolVersion: 1}, nil
}
func (*controllerReadiness) controlIdentity(id string) (controlplane.LifecycleSnapshot, error) {
	return controlplane.LifecycleSnapshot{Ready: true, Active: false, State: controlplane.LifecyclePassive, GenerationID: id, InstanceID: strings.Repeat("e", 32)}, nil
}

type controllerLifecycle struct{}

func (*controllerLifecycle) register(string, string, string, string) error        { return nil }
func (*controllerLifecycle) PrepareHandoff(context.Context, string, string) error { return nil }
func (*controllerLifecycle) PrepareActivation(context.Context, string) error      { return nil }
func (*controllerLifecycle) Activate(context.Context, string) error               { return nil }
func (*controllerLifecycle) Rollback(context.Context, string, string) error       { return nil }

func (*controllerDrain) register(string, string, string, string) error { return nil }
func (d *controllerDrain) BeginDrain(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.beginCalls = append(d.beginCalls, id)
	if d.events != nil {
		d.events.add("begin:" + id)
	}
	return d.beginErrors[id]
}
func (d *controllerDrain) ActiveRecordings(_ context.Context, id string) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.activeCalls = append(d.activeCalls, id)
	if d.events != nil {
		d.events.add("active:" + id)
	}
	return d.active[id], d.activeErrors[id]
}

type controllerDrain struct {
	mu           sync.Mutex
	active       map[string]int
	activeErrors map[string]error
	beginErrors  map[string]error
	beginCalls   []string
	activeCalls  []string
	events       *controllerEventLog
}

type controllerDetacher struct {
	mu     sync.Mutex
	calls  [][2]string
	errors map[string]error
	events *controllerEventLog
}

func (d *controllerDetacher) DetachEngine(_ context.Context, controlID, engineID string) error {
	d.mu.Lock()
	d.calls = append(d.calls, [2]string{controlID, engineID})
	err := d.errors[engineID]
	d.mu.Unlock()
	if d.events != nil {
		d.events.add("detach:" + controlID + ":" + engineID)
	}
	return err
}

type controllerEventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *controllerEventLog) add(event string) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *controllerEventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func controllerErrorCode(err error) string {
	var controllerErr *httpapi.ControllerError
	if errors.As(err, &controllerErr) {
		return controllerErr.Code
	}
	return ""
}
