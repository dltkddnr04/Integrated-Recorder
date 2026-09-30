package bootstrap

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/buildinfo"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/generation"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/resources"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/supervisor"
)

func TestConfigRejectsAuthDisabledOnPublicListener(t *testing.T) {
	config := DefaultConfig()
	config.DataDir = t.TempDir()
	config.AuthDisabled = true
	config.ListenAddr = ":8080"
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("Validate() error = %v, want loopback restriction", err)
	}
	config.ListenAddr = "127.0.0.1:8080"
	if err := config.Validate(); err != nil {
		t.Fatalf("loopback auth-disabled config rejected: %v", err)
	}
}

func TestPrivateRuntimeFilesAndDirectories(t *testing.T) {
	root := filepath.Join("/private", "tmp", fmt.Sprintf("runtime-host-private-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := makePrivateRuntimeDirs(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "runtime"), filepath.Join(root, "runtime", "ipc"), filepath.Join(root, "runtime", "state")} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatalf("private directory %q info=%v err=%v", path, info, err)
		}
	}
	ipc := filepath.Join(root, "runtime", "ipc")
	credential := filepath.Join(ipc, "secret")
	if err := writeAtomicPrivate(credential, []byte("01234567890123456789012345678901")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(credential)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("private credential info=%v err=%v", info, err)
	}
	link := filepath.Join(root, "runtime", "linked")
	if err := os.Symlink(ipc, link); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(filepath.Join(link, "nested")); err == nil {
		t.Fatal("symlinked private directory path unexpectedly accepted")
	}
}

func TestTrustedReleaseKeyConfigurationIsOptionalAndBounded(t *testing.T) {
	if keys, err := parseTrustedReleaseKeys(""); err != nil || len(keys) != 0 {
		t.Fatalf("empty trust configuration = %v, %v", keys, err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(public)
	keys, err := parseTrustedReleaseKeys(`[{"key_id":"stable-2026","public_key_base64":"` + encoded + `"}]`)
	if err != nil || len(keys) != 1 || len(keys["stable-2026"]) != ed25519.PublicKeySize {
		t.Fatalf("valid trust configuration = %v, %v", keys, err)
	}
	for name, value := range map[string]string{
		"duplicate key id": `[{"key_id":"stable","public_key_base64":"` + encoded + `"},{"key_id":"stable","public_key_base64":"` + encoded + `"}]`,
		"unknown field":    `[{"key_id":"stable","public_key_base64":"` + encoded + `","private_key":"not accepted"}]`,
		"trailing data":    `[{"key_id":"stable","public_key_base64":"` + encoded + `"}] {}`,
		"invalid key":      `[{"key_id":"stable","public_key_base64":"bad"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseTrustedReleaseKeys(value); err == nil {
				t.Fatal("invalid trust configuration was accepted")
			}
		})
	}
}

func TestSelectGenerationReusesMatchingActiveBundle(t *testing.T) {
	build := buildinfoForTest()
	id := strings.Repeat("a", 32)
	snapshot := generationSnapshotForTest(id, build)
	got, needsStage, err := selectGeneration(snapshot, build)
	if err != nil || needsStage || got != id {
		t.Fatalf("selectGeneration = (%q, %t, %v), want existing active identity", got, needsStage, err)
	}
	build.Commit = "different"
	got, needsStage, err = selectGeneration(snapshot, build)
	if err != nil || !needsStage || !generationPattern.MatchString(got) || got == id {
		t.Fatalf("changed release selection = (%q, %t, %v), want fresh stage identity", got, needsStage, err)
	}
}

func TestSelectRuntimeReleaseUsesDurableActiveIdentity(t *testing.T) {
	bundle := t.TempDir()
	build := buildinfoForTest()
	id := strings.Repeat("a", 32)
	selected, err := selectRuntimeRelease(generationSnapshotForTest(id, build), build, bundle, filepath.Join(t.TempDir(), "runtime"), nil)
	if err != nil || selected.generationID != id || selected.needsStage || selected.controlPath != filepath.Join(bundle, "control-plane") || selected.enginePath != filepath.Join(bundle, "recorder-engine") {
		t.Fatalf("matching bundled generation selection = %+v, %v", selected, err)
	}

	other := build
	other.Commit = strings.Repeat("f", 40)
	if _, err := selectRuntimeRelease(generationSnapshotForTest(id, other), build, bundle, filepath.Join(t.TempDir(), "runtime"), nil); err == nil {
		t.Fatal("missing signed installation for the active generation silently fell back to the image bundle")
	}

	empty := generation.Snapshot{Generations: map[string]generation.Generation{}, Leases: map[string]generation.Lease{}}
	selected, err = selectRuntimeRelease(empty, build, bundle, filepath.Join(t.TempDir(), "runtime"), nil)
	if err != nil || !selected.needsStage || !generationPattern.MatchString(selected.generationID) {
		t.Fatalf("fresh bundle selection = %+v, %v", selected, err)
	}
}

func TestExitedEngineReleasesHostOwnedIngestResources(t *testing.T) {
	coordinator, err := resources.New(resources.Limits{GlobalBufferBytes: 100, PerRecordingBufferBytes: 100, QueueObjects: 1, WriterConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	owner := "e" + strings.Repeat("a", 32) + "-" + strings.Repeat("b", 32)
	if err := coordinator.SetReservation(context.Background(), owner, strings.Repeat("c", 32), "payload-1", 80); err != nil {
		t.Fatal(err)
	}
	if got := coordinator.Snapshot().UsedBytes; got != 80 {
		t.Fatalf("reserved bytes = %d, want 80", got)
	}
	releaseEngineResources(coordinator, supervisor.ProcessSpec{GenerationID: strings.Repeat("a", 32), Role: supervisor.RoleEngine, Env: []string{"RUNTIME_RESOURCE_OWNER=" + owner}})
	if got := coordinator.Snapshot().UsedBytes; got != 0 {
		t.Fatalf("bytes retained after confirmed Engine exit = %d, want 0", got)
	}
	if err := coordinator.SetReservation(context.Background(), "e"+strings.Repeat("b", 32), strings.Repeat("c", 32), "payload-2", 100); err != nil {
		t.Fatalf("capacity did not recover after Engine exit: %v", err)
	}
}

func TestExitedControlReleasesProcessTelemetryOwner(t *testing.T) {
	coordinator, err := resources.New(resources.Limits{GlobalBufferBytes: 100, PerRecordingBufferBytes: 100, QueueObjects: 1, WriterConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := newResourceOwnerIDForRole("c", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ReportTelemetry(owner, 10, 20, 0); err != nil {
		t.Fatal(err)
	}
	if got := coordinator.TelemetrySnapshot().Throughput.ReadBytesTotal; got != 10 {
		t.Fatalf("read bytes before Control exit = %d, want 10", got)
	}
	releaseProcessResources(coordinator, supervisor.ProcessSpec{GenerationID: strings.Repeat("a", 32), Role: supervisor.RoleControl, Env: []string{"RUNTIME_RESOURCE_OWNER=" + owner}})
	// Owner cleanup removes its cumulative baseline so an identically restarted
	// process can report counters from zero without retaining stale gauges.
	if err := coordinator.ReportTelemetry(owner, 0, 0, 0); err != nil {
		t.Fatalf("restarted Control could not report fresh telemetry: %v", err)
	}
}

func TestBootstrapSupervisorRealProcessSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("process-boundary smoke test")
	}
	work := t.TempDir()
	controlAddr := reserveControlAddress(t)
	target, err := url.Parse("http://" + controlAddr)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("b", 32)
	engineMarker := filepath.Join(work, "engine.started")
	controlMarker := filepath.Join(work, "control.started")
	makeProcess := func(role supervisor.Role, marker string, extra ...string) supervisor.ProcessSpec {
		env := []string{"PATH=/usr/bin:/bin", "HOME=/private/tmp", "BOOTSTRAP_HELPER=1", "BOOTSTRAP_ROLE=" + string(role), "BOOTSTRAP_MARKER=" + marker}
		env = append(env, extra...)
		return supervisor.ProcessSpec{
			GenerationID: id, Role: role, Executable: executable,
			Args: []string{"-test.run=^TestBootstrapSubprocessHelper$"}, Env: env,
		}
	}
	launcher := supervisor.ExecLauncher{}
	readiness := supervisor.ReadinessFunc(func(ctx context.Context, spec supervisor.ProcessSpec, _ supervisor.Child) error {
		return waitMarker(ctx, envValueForTest(spec.Env, "BOOTSTRAP_MARKER"))
	})
	lifecycle := &smokeLifecycle{}
	sup, err := supervisor.New(supervisor.Options{Launcher: launcher, Readiness: readiness, ControlLifecycle: lifecycle, ShutdownTimeout: 4 * time.Second, CleanupTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sup.Close(ctx)
	}()
	engine := makeProcess(supervisor.RoleEngine, engineMarker)
	control := makeProcess(supervisor.RoleControl, controlMarker, "BOOTSTRAP_CONTROL_ADDR="+controlAddr)
	if err := sup.StageGeneration(context.Background(), supervisor.GenerationSpec{ID: id, Engine: engine, Control: control, ControlTarget: target}); err != nil {
		t.Fatal(err)
	}
	if err := sup.ActivateControl(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancelServe := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- sup.Serve(serveCtx, listener) }()
	responseBody := waitForHostResponse(t, "http://"+listener.Addr().String())
	if responseBody != "runtime-host-smoke-control" {
		t.Fatalf("stable listener response = %q", responseBody)
	}
	enginePID := readPIDMarker(t, engineMarker)
	controlPID := readPIDMarker(t, controlMarker)
	if enginePID == controlPID || enginePID == os.Getpid() || controlPID == os.Getpid() {
		t.Fatalf("expected distinct OS children (host=%d engine=%d control=%d)", os.Getpid(), enginePID, controlPID)
	}
	cancelServe()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Supervisor Serve shutdown: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Runtime Host smoke listener did not shut down in time")
	}
	for _, marker := range []string{engineMarker + ".stopped", controlMarker + ".stopped"} {
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("subprocess shutdown marker %q: %v", marker, err)
		}
	}
}

func envValueForTest(env []string, key string) string {
	for _, item := range env {
		name, value, ok := strings.Cut(item, "=")
		if ok && name == key {
			return value
		}
	}
	return ""
}

// TestBootstrapSubprocessHelper is re-executed as an OS child by the smoke test.
func TestBootstrapSubprocessHelper(t *testing.T) {
	if os.Getenv("BOOTSTRAP_HELPER") != "1" {
		return
	}
	role := os.Getenv("BOOTSTRAP_ROLE")
	marker := os.Getenv("BOOTSTRAP_MARKER")
	writeMarker := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			os.Exit(3)
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if role == string(supervisor.RoleControl) {
		listener, err := net.Listen("tcp", os.Getenv("BOOTSTRAP_CONTROL_ADDR"))
		if err != nil {
			os.Exit(4)
		}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "runtime-host-smoke-control") })}
		go func() { _ = server.Serve(listener) }()
		writeMarker(marker, fmt.Sprint(os.Getpid()))
		<-ctx.Done()
		shutdown, stop := context.WithTimeout(context.Background(), time.Second)
		_ = server.Shutdown(shutdown)
		stop()
	} else if role == string(supervisor.RoleEngine) {
		writeMarker(marker, fmt.Sprint(os.Getpid()))
		<-ctx.Done()
	} else {
		os.Exit(5)
	}
	writeMarker(marker+".stopped", "stopped")
}

func reserveControlAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func waitMarker(ctx context.Context, path string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func waitForHostResponse(t *testing.T, address string) string {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(address)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK {
				return string(body)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stable Runtime Host listener did not reach its Control child")
	return ""
}

func readPIDMarker(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil || pid <= 0 {
		t.Fatalf("process marker %q has invalid PID %q: %v", path, data, err)
	}
	return pid
}

type smokeLifecycle struct{}

func (*smokeLifecycle) PrepareActivation(context.Context, string) error      { return nil }
func (*smokeLifecycle) PrepareHandoff(context.Context, string, string) error { return nil }
func (*smokeLifecycle) Activate(context.Context, string) error               { return nil }
func (*smokeLifecycle) Rollback(context.Context, string, string) error       { return nil }

var _ supervisor.ControlLifecycle = (*smokeLifecycle)(nil)

func buildinfoForTest() buildinfo.Info {
	return buildinfo.Info{Version: "dev", Commit: "unknown", BuildTime: "unknown", ReleaseChannel: "development", RuntimeProtocolVersion: buildinfo.RuntimeProtocolVersion}
}

func generationSnapshotForTest(id string, build buildinfo.Info) generation.Snapshot {
	item := runtimeGeneration(id, build)
	item.State = generation.StateActive
	return generation.Snapshot{
		SchemaVersion: generation.SchemaVersion, ActiveGenerationID: id,
		Generations: map[string]generation.Generation{id: item}, Leases: map[string]generation.Lease{},
	}
}
