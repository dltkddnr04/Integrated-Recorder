package bootstrap

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/domain"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/generation"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/httpapi"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/install"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/installation"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/release"
	"github.com/dltkddnr04/integrated-recorder/internal/storage"
)

const (
	runtimeE2EAdapterID = "runtime-update-fixture"
	e2eVersionA         = "0.0.0-e2e.1"
	e2eVersionB         = "0.0.0-e2e.2"
	e2eCommitA          = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	e2eCommitB          = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// TestProductionSignedUpdateAcceptanceE2E drives a signed A→B application
// update through the actual Runtime Host executable and its public HTTP API.
// The only test-specific product behavior is the Recorder Engine's narrowly
// allowlisted loopback source transport, compiled with runtime_e2e.
func TestProductionSignedUpdateAcceptanceE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("requires building and running the production Runtime Host, Control Plane, and Recorder Engine")
	}
	if runtime.GOOS == "windows" {
		t.Skip("process-table assertions currently use the Unix ps interface")
	}
	fixture := newRuntimeUpdateFixture(t)
	artifacts := buildRuntimeUpdateArtifacts(t, fixture.server.URL)
	for iteration := 1; iteration <= 3; iteration++ {
		t.Run(fmt.Sprintf("iteration_%d", iteration), func(t *testing.T) {
			runProductionUpdateScenario(t, artifacts, fixture, iteration)
		})
	}
}

type runtimeUpdateArtifacts struct {
	root       string
	fixtureURL string
	bundleA    string
	hostA      string
	adapterDir string
	packageB   string
	publicKeys string
	manifestB  release.Manifest
}

func buildRuntimeUpdateArtifacts(t *testing.T, fixtureURL string) runtimeUpdateArtifacts {
	t.Helper()
	moduleRoot := findRuntimeE2EModuleRoot(t)
	root := newRuntimeE2ETempDir(t)
	for _, name := range []string{"bin", "releases", "initial-a", "adapters"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	fixtureURL = strings.TrimRight(fixtureURL, "/")
	ldflags := func(version, commit string) string {
		return strings.Join([]string{
			"-X github.com/dltkddnr04/integrated-recorder/internal/buildinfo.version=" + version,
			"-X github.com/dltkddnr04/integrated-recorder/internal/buildinfo.commit=" + commit,
			"-X github.com/dltkddnr04/integrated-recorder/internal/buildinfo.buildTime=2026-09-30T00:00:00Z",
			"-X github.com/dltkddnr04/integrated-recorder/internal/buildinfo.releaseChannel=prerelease",
		}, " ")
	}
	build := func(output, packagePath string, tags string, flags string) string {
		t.Helper()
		args := []string{"build", "-o", output}
		if tags != "" {
			args = append(args, "-tags", tags)
		}
		if flags != "" {
			args = append(args, "-ldflags", flags)
		}
		args = append(args, packagePath)
		runBuildCommand(t, moduleRoot, 4*time.Minute, args...)
		return output
	}
	bin := filepath.Join(root, "bin")
	controlA := build(filepath.Join(bin, "control-a"), "./cmd/control-plane", "", ldflags(e2eVersionA, e2eCommitA))
	engineFlagsA := ldflags(e2eVersionA, e2eCommitA) + " -X main.runtimeE2EFixtureOrigin=" + fixtureURL
	engineA := build(filepath.Join(bin, "engine-a"), "./cmd/recorder-engine", "runtime_e2e", engineFlagsA)
	controlB := build(filepath.Join(bin, "control-b"), "./cmd/control-plane", "", ldflags(e2eVersionB, e2eCommitB))
	engineFlagsB := ldflags(e2eVersionB, e2eCommitB) + " -X main.runtimeE2EFixtureOrigin=" + fixtureURL
	engineB := build(filepath.Join(bin, "engine-b"), "./cmd/recorder-engine", "runtime_e2e", engineFlagsB)
	adapterRuntime := build(filepath.Join(bin, "adapter-runtime"), "./cmd/adapters/owncast", "", "")
	packager := build(filepath.Join(bin, "release-pack"), "./cmd/release-pack", "", "")
	fixtureAdapter := build(filepath.Join(root, "adapters", "integrated-recorder-adapter-runtime-update-fixture"), "./web/e2e/runtime_update_adapter", "", "")
	_ = fixtureAdapter

	bundleA := filepath.Join(root, "initial-a")
	copyRuntimeArtifact(t, controlA, filepath.Join(bundleA, "control-plane"), 0555)
	copyRuntimeArtifact(t, engineA, filepath.Join(bundleA, "recorder-engine"), 0555)
	if err := os.Chmod(bundleA, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(bundleA, 0700)
		_ = os.Chmod(filepath.Join(bundleA, "control-plane"), 0600)
		_ = os.Chmod(filepath.Join(bundleA, "recorder-engine"), 0600)
	})
	hostA := build(filepath.Join(bin, "runtime-host-a"), "./cmd/runtime-host", "", ldflags(e2eVersionA, e2eCommitA)+" -X github.com/dltkddnr04/integrated-recorder/internal/runtimehost/bootstrap.defaultBundleDir="+bundleA)
	hostB := build(filepath.Join(bin, "runtime-host-b"), "./cmd/runtime-host", "", ldflags(e2eVersionB, e2eCommitB)+" -X github.com/dltkddnr04/integrated-recorder/internal/runtimehost/bootstrap.defaultBundleDir="+bundleA)

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	keyID := "runtime-e2e-test"
	publicKeys, err := json.Marshal([]map[string]string{{"key_id": keyID, "public_key_base64": base64.StdEncoding.EncodeToString(publicKey)}})
	if err != nil {
		t.Fatal(err)
	}
	privateKeyEnv := base64.StdEncoding.EncodeToString(privateKey)

	packageA := filepath.Join(root, "releases", "a")
	packageReleaseWithProductionTool(t, root, packager, privateKeyEnv, packageA, e2eVersionA, e2eCommitA, hostA, controlA, engineA, adapterRuntime, keyID)
	packageB := filepath.Join(root, "releases", "b")
	packageReleaseWithProductionTool(t, root, packager, privateKeyEnv, packageB, e2eVersionB, e2eCommitB, hostB, controlB, engineB, adapterRuntime, keyID)
	manifestBytes, err := os.ReadFile(filepath.Join(packageB, "release.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifestB release.Manifest
	if err := json.Unmarshal(manifestBytes, &manifestB); err != nil {
		t.Fatal(err)
	}
	verifyTestPackageArtifacts(t, packageB, manifestB)
	return runtimeUpdateArtifacts{
		root: root, fixtureURL: fixtureURL, bundleA: bundleA, hostA: hostA,
		adapterDir: filepath.Join(root, "adapters"), packageB: packageB,
		publicKeys: string(publicKeys), manifestB: manifestB,
	}
}

func findRuntimeE2EModuleRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not locate repository go.mod from test working directory")
		}
		directory = parent
	}
}

func newRuntimeE2ETempDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/private/tmp", "integrated-recorder-runtime-e2e-")
	if err != nil {
		t.Fatalf("create symlink-free runtime E2E directory: %v", err)
	}
	t.Cleanup(func() {
		makeRuntimeE2ETreeWritable(root)
		_ = os.RemoveAll(root)
	})
	return root
}

func packageReleaseWithProductionTool(t *testing.T, root, packager, signingKey, output, version, commit, host, control, engine, adapterRuntime, keyID string) {
	t.Helper()
	args := []string{
		"-version", version, "-commit", commit, "-build-time", "2026-09-30T00:00:00Z",
		"-channel", "prerelease", "-platform", runtime.GOOS, "-architecture", runtime.GOARCH,
		"-key-id", keyID, "-output", output, "-runtime-host", host,
		"-control-plane", control, "-recorder-engine", engine, "-adapter-runtime", adapterRuntime,
	}
	runCommand(t, root, 2*time.Minute, []string{"IR_RELEASE_SIGNING_PRIVATE_KEY_BASE64=" + signingKey}, packager, args...)
}

func runBuildCommand(t *testing.T, directory string, timeout time.Duration, args ...string) {
	t.Helper()
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("go tool is unavailable")
	}
	runCommand(t, directory, timeout, nil, goBinary, args...)
}

func runCommand(t *testing.T, directory string, timeout time.Duration, extraEnv []string, name string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	command.Env = minimalRuntimeE2EEnv(extraEnv)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("command timed out (%s %s): %v\n%s", name, strings.Join(args, " "), ctx.Err(), boundedOutput(output))
	}
	if err != nil {
		t.Fatalf("command failed (%s %s): %v\n%s", name, strings.Join(args, " "), err, boundedOutput(output))
	}
}

func minimalRuntimeE2EEnv(extra []string) []string {
	values := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TMPDIR=" + os.TempDir()}
	if cache := os.Getenv("GOCACHE"); cache != "" {
		values = append(values, "GOCACHE="+cache)
	}
	if modcache := os.Getenv("GOMODCACHE"); modcache != "" {
		values = append(values, "GOMODCACHE="+modcache)
	}
	return append(values, extra...)
}

func boundedOutput(output []byte) string {
	const limit = 12 << 10
	if len(output) > limit {
		output = output[len(output)-limit:]
	}
	return string(output)
}

func copyRuntimeArtifact(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Chmod(mode); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

func verifyTestPackageArtifacts(t *testing.T, directory string, manifest release.Manifest) {
	t.Helper()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("production release-pack emitted invalid B manifest: %v", err)
	}
	for _, artifact := range manifest.Artifacts {
		path := filepath.Join(directory, artifact.Filename)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != artifact.Size {
			t.Fatalf("B artifact %q does not match signed size: info=%v err=%v", artifact.Role, info, err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
			t.Fatalf("B artifact %q does not match signed SHA-256", artifact.Role)
		}
	}
}

type runtimeUpdateFixture struct {
	server  *httptest.Server
	mu      sync.Mutex
	streams map[string]*runtimeUpdateStream
}

type runtimeUpdateStream struct {
	latest           uint64
	online           bool
	sessionID        string
	title            string
	description      string
	tokenGeneration  uint64
	expired          bool
	segmentRequests  map[uint64][]runtimeSegmentRequest
	manifestRequests []runtimeManifestRequest
	refreshes        []runtimeRefreshRequest
	metadataRequests []time.Time
	watchRequests    []time.Time
	refreshStarted   chan time.Time
	refreshRelease   chan struct{}
}

type runtimeSegmentRequest struct {
	Token     string
	At        time.Time
	Succeeded bool
}
type runtimeManifestRequest struct {
	Token string
	At    time.Time
}
type runtimeRefreshRequest struct {
	Token string
	At    time.Time
}

func newRuntimeUpdateFixture(t *testing.T) *runtimeUpdateFixture {
	t.Helper()
	fixture := &runtimeUpdateFixture{streams: make(map[string]*runtimeUpdateStream)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /e2e/state", fixture.handleState)
	mux.HandleFunc("GET /e2e/metadata", fixture.handleMetadata)
	mux.HandleFunc("GET /e2e/refresh", fixture.handleRefresh)
	mux.HandleFunc("GET /hls/stream.m3u8", fixture.handleManifest)
	mux.HandleFunc("GET /hls/segments/{sequence}", fixture.handleSegment)
	fixture.server = httptest.NewServer(mux)
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *runtimeUpdateFixture) reset(stream, title, description, sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streams[stream] = &runtimeUpdateStream{
		sessionID: sessionID, title: title, description: description,
		segmentRequests: make(map[uint64][]runtimeSegmentRequest),
	}
}

func (f *runtimeUpdateFixture) advance(stream string, count uint64) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	value := f.streams[stream]
	value.latest += count
	return value.latest
}

func (f *runtimeUpdateFixture) setOnline(stream string, online bool) {
	f.mu.Lock()
	f.streams[stream].online = online
	f.mu.Unlock()
}

func (f *runtimeUpdateFixture) setMetadata(stream, title, description string) {
	f.mu.Lock()
	f.streams[stream].title, f.streams[stream].description = title, description
	f.mu.Unlock()
}

func (f *runtimeUpdateFixture) expireAndBlockRefresh(stream string) (<-chan time.Time, func()) {
	f.mu.Lock()
	value := f.streams[stream]
	value.expired = true
	value.refreshStarted = make(chan time.Time, 1)
	value.refreshRelease = make(chan struct{})
	started, release := value.refreshStarted, value.refreshRelease
	f.mu.Unlock()
	var once sync.Once
	return started, func() { once.Do(func() { close(release) }) }
}

func (f *runtimeUpdateFixture) segmentRequestsFor(stream string) map[uint64][]runtimeSegmentRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make(map[uint64][]runtimeSegmentRequest)
	for sequence, requests := range f.streams[stream].segmentRequests {
		result[sequence] = append([]runtimeSegmentRequest(nil), requests...)
	}
	return result
}

func (f *runtimeUpdateFixture) refreshesFor(stream string) []runtimeRefreshRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runtimeRefreshRequest(nil), f.streams[stream].refreshes...)
}

func (f *runtimeUpdateFixture) metadataRequestsFor(stream string) []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.streams[stream].metadataRequests...)
}

func (f *runtimeUpdateFixture) watchRequestCount(stream string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.streams[stream].watchRequests)
}

func (f *runtimeUpdateFixture) handleState(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	value := f.streams[r.URL.Query().Get("stream")]
	if value == nil {
		f.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Query().Get("stream"), "w-") {
		value.watchRequests = append(value.watchRequests, time.Now().UTC())
	}
	result := map[string]any{"online": value.online, "session_id": value.sessionID, "title": value.title}
	f.mu.Unlock()
	writeRuntimeJSON(w, result)
}

func (f *runtimeUpdateFixture) handleMetadata(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	value := f.streams[r.URL.Query().Get("stream")]
	if value == nil {
		f.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	value.metadataRequests = append(value.metadataRequests, time.Now().UTC())
	result := map[string]string{"title": value.title, "description": value.description}
	f.mu.Unlock()
	writeRuntimeJSON(w, result)
}

func (f *runtimeUpdateFixture) handleRefresh(w http.ResponseWriter, r *http.Request) {
	stream, token := r.URL.Query().Get("stream"), r.URL.Query().Get("token")
	f.mu.Lock()
	value := f.streams[stream]
	if value == nil || !value.expired || token != fmt.Sprintf("token-%d", value.tokenGeneration) {
		f.mu.Unlock()
		http.Error(w, "refresh is not currently required", http.StatusConflict)
		return
	}
	started, release := value.refreshStarted, value.refreshRelease
	at := time.Now().UTC()
	value.refreshes = append(value.refreshes, runtimeRefreshRequest{Token: token, At: at})
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- at:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-r.Context().Done():
			http.Error(w, "refresh request canceled", http.StatusGatewayTimeout)
			return
		case <-time.After(40 * time.Second):
			http.Error(w, "refresh fixture timed out", http.StatusGatewayTimeout)
			return
		}
	}
	f.mu.Lock()
	value = f.streams[stream]
	if value == nil {
		f.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	if value.expired {
		value.tokenGeneration++
		value.expired = false
	}
	refreshed := fmt.Sprintf("token-%d", value.tokenGeneration)
	f.mu.Unlock()
	writeRuntimeJSON(w, map[string]string{"token": refreshed})
}

func (f *runtimeUpdateFixture) handleManifest(w http.ResponseWriter, r *http.Request) {
	stream, token := r.URL.Query().Get("stream"), r.URL.Query().Get("token")
	f.mu.Lock()
	value := f.streams[stream]
	if value == nil {
		f.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	value.manifestRequests = append(value.manifestRequests, runtimeManifestRequest{Token: token, At: time.Now().UTC()})
	valid := token == fmt.Sprintf("token-%d", value.tokenGeneration) && !value.expired
	latest := value.latest
	f.mu.Unlock()
	if !valid {
		http.Error(w, "fixture media token expired", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n")
	for sequence := uint64(1); sequence <= latest; sequence++ {
		_, _ = fmt.Fprintf(w, "#EXTINF:1.0,\n/hls/segments/%06d.ts?stream=%s&token=%s\n", sequence, urlQueryEscape(stream), urlQueryEscape(token))
	}
}

func (f *runtimeUpdateFixture) handleSegment(w http.ResponseWriter, r *http.Request) {
	stream, token := r.URL.Query().Get("stream"), r.URL.Query().Get("token")
	sequence, err := strconv.ParseUint(strings.TrimSuffix(r.PathValue("sequence"), ".ts"), 10, 64)
	if err != nil || sequence == 0 {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	value := f.streams[stream]
	if value == nil {
		f.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	valid := token == fmt.Sprintf("token-%d", value.tokenGeneration) && !value.expired && sequence <= value.latest
	value.segmentRequests[sequence] = append(value.segmentRequests[sequence], runtimeSegmentRequest{Token: token, At: time.Now().UTC(), Succeeded: valid})
	f.mu.Unlock()
	if !valid {
		http.Error(w, "fixture media token expired", http.StatusForbidden)
		return
	}
	_, _ = io.WriteString(w, runtimeSegmentPayload(stream, sequence))
}

func runtimeSegmentPayload(stream string, sequence uint64) string {
	return fmt.Sprintf("stream=%s;sequence=%06d;canonical-source-payload", stream, sequence)
}

func urlQueryEscape(value string) string { return url.QueryEscape(value) }

func writeRuntimeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

type runtimeHostProcess struct {
	command *exec.Cmd
	done    chan struct{}
	err     error
	output  testOutputBuffer
}

func runProductionUpdateScenario(t *testing.T, artifacts runtimeUpdateArtifacts, fixture *runtimeUpdateFixture, iteration int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dataRoot := newRuntimeE2ETempDir(t)
	dataDir := filepath.Join(dataRoot, "data")
	if err := os.Mkdir(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeRuntimeE2ETreeWritable(dataDir) })
	streamR := fmt.Sprintf("r-%d", iteration)
	streamW := fmt.Sprintf("w-%d", iteration)
	streamS := fmt.Sprintf("s-%d", iteration)
	fixture.reset(streamR, "Before update", "Before update", "session-r-"+strconv.Itoa(iteration))
	fixture.reset(streamW, "Watch source", "Watch description", fmt.Sprintf("session-w-%d", iteration))
	fixture.reset(streamS, "New source", "New description", "session-s-"+strconv.Itoa(iteration))
	controlABinary := filepath.Join(artifacts.bundleA, "control-plane")
	engineABinary := filepath.Join(artifacts.bundleA, "recorder-engine")
	installedDir := filepath.Join(dataDir, "runtime", "releases", install.ReleaseDirectoryID(e2eVersionB, e2eCommitB))
	controlBBinary := filepath.Join(installedDir, fmt.Sprintf("%s-%s-%s", release.RoleControlPlane, runtime.GOOS, runtime.GOARCH))
	engineBBinary := filepath.Join(installedDir, fmt.Sprintf("%s-%s-%s", release.RoleRecorderEngine, runtime.GOOS, runtime.GOARCH))
	fixtureAdapterBinary := filepath.Join(artifacts.adapterDir, "integrated-recorder-adapter-runtime-update-fixture")

	listenAddr := reserveRuntimeAddress(t)
	command := exec.Command(artifacts.hostA)
	command.Env = minimalRuntimeE2EEnv([]string{
		"DATA_DIR=" + dataDir,
		"ADDR=" + listenAddr,
		"AUTH_DISABLED=1",
		"ADAPTER_DIR=" + artifacts.adapterDir,
		"IR_RELEASE_BUNDLE_DIR=" + artifacts.packageB,
		"IR_RELEASE_TRUSTED_KEYS_JSON=" + artifacts.publicKeys,
	})
	process := &runtimeHostProcess{command: command, done: make(chan struct{})}
	command.Stdout, command.Stderr = &process.output, &process.output
	if err := command.Start(); err != nil {
		t.Fatalf("start production Runtime Host: %v", err)
	}
	go func() {
		process.err = command.Wait()
		close(process.done)
	}()
	t.Cleanup(func() {
		stopRuntimeHostProcess(process)
		for _, executable := range []string{artifacts.hostA, controlABinary, engineABinary, controlBBinary, engineBBinary, fixtureAdapterBinary} {
			if err := waitProcessAbsent(t, executable, 10*time.Second); err != nil {
				t.Errorf("acceptance cleanup left product process running for %s: %v", filepath.Base(executable), err)
			}
		}
	})
	baseURL := "http://" + listenAddr
	client := &http.Client{Timeout: 3 * time.Minute}
	status := waitRuntimeHostStatus(t, ctx, client, baseURL, process)
	installationBefore := installation.ReadOnly(dataDir)
	if installationBefore.State != installation.StateReady || installationBefore.InstallationID == "" {
		t.Fatalf("AUTH_DISABLED bootstrap did not durably initialize installation: %+v", installationBefore)
	}
	if status.Host.Version != e2eVersionA || status.Host.Commit != e2eCommitA || status.Application.Version != e2eVersionA || status.Application.Commit != e2eCommitA {
		t.Fatalf("initial production Host/Application identity = host=%+v application=%+v, want release A", status.Host, status.Application)
	}
	if status.ActiveControl == nil || status.ActiveControl.Version != e2eVersionA || status.ActiveControl.Commit != e2eCommitA || status.DefaultEngine == nil || status.DefaultEngine.Version != e2eVersionA || status.DefaultEngine.Commit != e2eCommitA {
		t.Fatalf("release A is not active/default at bootstrap: %+v", status)
	}
	if status.VerificationState != "not_checked" {
		t.Fatalf("fresh Host verification state=%q, want not_checked", status.VerificationState)
	}

	controlAPID := waitProcessForBinary(t, controlABinary, 20*time.Second)
	engineAPID := waitProcessForBinary(t, engineABinary, 20*time.Second)
	if controlAPID == engineAPID || controlAPID == command.Process.Pid || engineAPID == command.Process.Pid {
		t.Fatalf("Host/Control A/Engine A are not separate production processes: host=%d control=%d engine=%d", command.Process.Pid, controlAPID, engineAPID)
	}

	inputR := map[string]string{"source_url": artifacts.fixtureURL + "/source/" + streamR}
	recordingR := createRuntimeRecording(t, client, baseURL, "Recording R", inputR)
	if recordingR.Title != "Recording R" || recordingR.State != domain.StateRecording {
		t.Fatalf("manual recording R was not created through Control API: id=%s title=%q state=%s", recordingR.ID, recordingR.Title, recordingR.State)
	}
	leaseA := waitRecordingLease(t, dataDir, recordingR.ID, 30*time.Second)
	if leaseA.EngineGeneration != status.DefaultEngine.ID {
		t.Fatalf("R is pinned to generation %s, active A generation is %s", leaseA.EngineGeneration, status.DefaultEngine.ID)
	}
	fixture.advance(streamR, 20)
	waitRecordingSequenceCount(t, client, baseURL, recordingR.ID, 20, 20*time.Second)
	baseline := getRecording(t, client, baseURL, recordingR.ID)
	verifyRuntimeRecordingSegments(t, dataDir, baseline, streamR, 1, 20)
	baselineSequences := recordingSequences(baseline)
	if !equalSequenceRange(baselineSequences, 1, 20) {
		t.Fatalf("pre-update source sequence baseline is not contiguous: %v", baselineSequences)
	}
	metadataBaseline := waitMetadataTimeline(t, ctx, client, baseURL, recordingR.ID, 1, 35*time.Second)
	if len(metadataBaseline.Items) != 1 || stringValue(metadataBaseline.Items[0].Title) != "Before update" || stringValue(metadataBaseline.Items[0].Description) != "Before update" {
		t.Fatalf("unexpected source metadata baseline: %+v", metadataBaseline.Items)
	}

	inputW := map[string]string{"source_url": artifacts.fixtureURL + "/source/" + streamW}
	watchID := createRuntimeWatch(t, client, baseURL, "Watch W", inputW)
	waitFixtureWatchPoll(t, fixture, streamW, 20*time.Second)
	watchBefore, _ := getRuntimeJSON[map[string]any](t, client, baseURL, http.MethodGet, "/api/watches/"+watchID, nil)
	if watchBefore["enabled"] != true {
		t.Fatalf("Watch W did not persist enabled: %#v", watchBefore)
	}

	// The Host API performs signed discovery and immutable staging. Package
	// hashes are also checked above, while successful Host staging proves its
	// actual Ed25519 verifier accepted the exact manifest and artifacts.
	checkStatus, code := getRuntimeJSON[httpapi.Status](t, client, baseURL, http.MethodPost, httpapi.Endpoint+"/check", map[string]any{})
	if code != http.StatusOK || !checkStatus.UpdatesAvailable || checkStatus.AvailableRelease == nil || checkStatus.AvailableRelease.Version != e2eVersionB || checkStatus.VerificationState != "verified" {
		t.Fatalf("production update check did not discover/verify release B: code=%d status=%+v", code, checkStatus)
	}
	stageStatus, code := getRuntimeJSON[httpapi.Status](t, client, baseURL, http.MethodPost, httpapi.Endpoint+"/stage", map[string]any{})
	if code != http.StatusOK || stageStatus.StagedRelease == nil || stageStatus.StagedRelease.Version != e2eVersionB || stageStatus.StagedRelease.Commit != e2eCommitB || stageStatus.VerificationState != "verified" {
		t.Fatalf("production update stage did not install verified B: code=%d status=%+v", code, stageStatus)
	}
	if err := verifyInstalledRuntimeRelease(installedDir, artifacts.manifestB); err != nil {
		t.Fatalf("staged immutable B release failed independent size/hash/signature checks: %v", err)
	}
	if engineProcess := processForBinary(engineABinary); engineProcess == 0 {
		t.Fatal("Engine A exited during signed B staging")
	}

	refreshStarted, releaseRefresh := fixture.expireAndBlockRefresh(streamR)
	t.Cleanup(releaseRefresh)
	fixture.advance(streamR, 10)
	var refreshAt time.Time
	select {
	case refreshAt = <-refreshStarted:
	case <-ctx.Done():
		t.Fatalf("Engine A did not require and begin media refresh after the old token expired: %v", ctx.Err())
	case <-time.After(10 * time.Second):
		t.Fatal("Engine A did not call the fixture adapter's refresh capability after source expiry")
	}
	activateStarted := time.Now().UTC()
	activateStatus, code := getRuntimeJSON[httpapi.Status](t, client, baseURL, http.MethodPost, httpapi.Endpoint+"/activate", map[string]any{})
	activateFinished := time.Now().UTC()
	if code != http.StatusOK || activateStatus.ActiveControl == nil || activateStatus.ActiveControl.Version != e2eVersionB || activateStatus.ActiveControl.Commit != e2eCommitB || activateStatus.DefaultEngine == nil || activateStatus.DefaultEngine.Version != e2eVersionB || activateStatus.DefaultEngine.Commit != e2eCommitB {
		releaseRefresh()
		t.Fatalf("production update API failed to activate B: code=%d status=%+v; host output=%s", code, activateStatus, process.output.String())
	}
	if refreshAt.After(activateFinished) {
		releaseRefresh()
		t.Fatalf("fixture refresh began after activation returned; expected an in-flight Engine A refresh: refresh=%s activation=%s..%s", refreshAt, activateStarted, activateFinished)
	}
	installationAfter := installation.ReadOnly(dataDir)
	if installationAfter.State != installation.StateReady || installationAfter.InstallationID != installationBefore.InstallationID {
		releaseRefresh()
		t.Fatalf("A→B application activation changed Host-owned installation state: before=%+v after=%+v", installationBefore, installationAfter)
	}
	if err := waitProcessAbsent(t, controlABinary, 20*time.Second); err != nil {
		releaseRefresh()
		t.Fatalf("old production Control A process did not exit after activation: %v", err)
	}
	fixture.setMetadata(streamR, "After update title", "After update description")
	controlBPID := waitProcessForBinary(t, controlBBinary, 20*time.Second)
	engineBPID := waitProcessForBinary(t, engineBBinary, 20*time.Second)
	if controlBPID == controlAPID || engineBPID == engineAPID || controlBPID == engineBPID {
		releaseRefresh()
		t.Fatalf("release B did not spawn distinct product Control/Engine processes: A=(%d,%d) B=(%d,%d)", controlAPID, engineAPID, controlBPID, engineBPID)
	}
	if processForBinary(engineABinary) == 0 {
		releaseRefresh()
		t.Fatal("Engine A exited while R still held its generation lease")
	}
	status = waitRuntimeStatus(t, ctx, client, baseURL, func(s httpapi.Status) bool {
		return s.ActiveControl != nil && s.ActiveControl.Version == e2eVersionB && hasGeneration(s.DrainingGenerations, e2eVersionA, 1)
	})
	if !hasGeneration(status.DrainingGenerations, e2eVersionA, 1) {
		releaseRefresh()
		t.Fatalf("old generation A is not draining with an R lease: %+v", status.DrainingGenerations)
	}
	leaseNow := readRuntimeLeases(t, dataDir)
	if leaseNow[recordingR.ID].EngineGeneration != leaseA.EngineGeneration {
		releaseRefresh()
		t.Fatalf("R changed Engine generation across update: before=%s after=%s", leaseA.EngineGeneration, leaseNow[recordingR.ID].EngineGeneration)
	}
	// Complete the request as soon as the activation proof is established. The
	// fixture intentionally holds the adapter call across the A→B switch, but
	// must not outlive the adapter IPC deadline while unrelated B-side setup runs.
	releaseRefresh()
	if err := waitRuntimeRecordingSequenceCount(client, baseURL, recordingR.ID, 30, 20*time.Second); err != nil {
		t.Fatalf("R did not resume capture after adapter refresh: %v; fixture=%s; host=%s", err, fixture.describe(streamR), process.output.String())
	}
	refreshes := fixture.refreshesFor(streamR)
	if len(refreshes) != 1 || refreshes[0].Token != "token-0" || !refreshes[0].At.Before(activateFinished) {
		t.Fatalf("expired source did not require exactly one pre-activation Engine A refresh: %+v, activation=%s", refreshes, activateFinished)
	}
	if err := waitWatchPollingAfter(t, fixture, streamW, activateFinished, 20*time.Second); err != nil {
		t.Fatalf("Watch W did not resume polling after Control B activation: %v", err)
	}
	fixture.setOnline(streamW, true)
	watchRecordingID := waitWatchRecording(t, client, baseURL, watchID, 30*time.Second)
	watchRelations := waitWatchRelationCount(t, client, baseURL, watchID, 1, 30*time.Second)
	if len(watchRelations) != 1 || watchRelations[0].ID != watchRecordingID {
		t.Fatalf("one live Watch session did not produce exactly one linked Recording: watch=%s recording=%s relations=%+v", watchID, watchRecordingID, watchRelations)
	}
	watchLease := waitRecordingLease(t, dataDir, watchRecordingID, 20*time.Second)
	if watchLease.EngineGeneration != activateStatus.DefaultEngine.ID {
		t.Fatalf("Watch-created recording is pinned to %s, want B %s", watchLease.EngineGeneration, activateStatus.DefaultEngine.ID)
	}

	inputS := map[string]string{"source_url": artifacts.fixtureURL + "/source/" + streamS}
	recordingS := createRuntimeRecording(t, client, baseURL, "Recording S", inputS)
	leaseS := waitRecordingLease(t, dataDir, recordingS.ID, 20*time.Second)
	if leaseS.EngineGeneration != activateStatus.DefaultEngine.ID || leaseS.EngineGeneration == leaseA.EngineGeneration {
		t.Fatalf("post-update Recording S does not use B while R uses A: R=%s S=%s B=%s", leaseA.EngineGeneration, leaseS.EngineGeneration, activateStatus.DefaultEngine.ID)
	}
	fixture.advance(streamS, 2)
	waitRecordingSequenceCount(t, client, baseURL, recordingS.ID, 2, 15*time.Second)

	fixture.advance(streamR, 30)
	waitRecordingSequenceCount(t, client, baseURL, recordingR.ID, 60, 20*time.Second)
	finalR := getRecording(t, client, baseURL, recordingR.ID)
	if finalR.ID != recordingR.ID || finalR.Title != "Recording R" || finalR.State != domain.StateRecording || len(finalR.Gaps) != 0 {
		t.Fatalf("R identity/title/state/gaps changed during application update: id=%s title=%q state=%s gaps=%+v", finalR.ID, finalR.Title, finalR.State, finalR.Gaps)
	}
	verifyRuntimeRecordingSegments(t, dataDir, finalR, streamR, 1, 60)
	finalSequences := recordingSequences(finalR)
	if !equalSequenceRange(finalSequences, 1, 60) {
		t.Fatalf("source sequence continuity failed across activation/refresh: %v", finalSequences)
	}
	requests := fixture.segmentRequestsFor(streamR)
	for sequence := uint64(1); sequence <= 60; sequence++ {
		if len(requests[sequence]) != 1 || !requests[sequence][0].Succeeded {
			t.Fatalf("source sequence %d request count/success = %+v; want one successful request", sequence, requests[sequence])
		}
	}
	var firstRefreshedRequest time.Time
	for sequence := uint64(21); sequence <= 60; sequence++ {
		for _, request := range requests[sequence] {
			if request.Token == "token-1" && (firstRefreshedRequest.IsZero() || request.At.Before(firstRefreshedRequest)) {
				firstRefreshedRequest = request.At
			}
		}
	}
	if firstRefreshedRequest.IsZero() || firstRefreshedRequest.Before(refreshes[0].At) {
		t.Fatalf("no post-refresh source sequence was captured with the refreshed token; first=%s refresh=%+v", firstRefreshedRequest, refreshes)
	}
	metadataAfterActivation := waitMetadataTimeline(t, ctx, client, baseURL, recordingR.ID, 2, 40*time.Second)
	if len(metadataAfterActivation.Items) != 2 || stringValue(metadataAfterActivation.Items[0].Title) != "Before update" || stringValue(metadataAfterActivation.Items[1].Title) != "After update title" {
		t.Fatalf("R metadata timeline is not exactly Before→After: %+v", metadataAfterActivation.Items)
	}
	metadataRequests := fixture.metadataRequestsFor(streamR)
	if !containsTimeAfter(metadataRequests, activateFinished) {
		t.Fatalf("no source metadata poll occurred after old Control A exited/Control B activated: %v activation=%s", metadataRequests, activateFinished)
	}
	// Repeated identical metadata polling must not add semantic revisions.
	if len(metadataAfterActivation.Items) != 2 {
		t.Fatalf("identical metadata polling created duplicate revisions: %+v", metadataAfterActivation.Items)
	}

	status = waitRuntimeStatus(t, ctx, client, baseURL, func(s httpapi.Status) bool {
		return hasGeneration(s.DrainingGenerations, e2eVersionA, 1)
	})
	if processForBinary(engineABinary) == 0 {
		t.Fatal("old Engine A did not remain alive while R still held its lease")
	}
	currentLeases := readRuntimeLeases(t, dataDir)
	if currentLeases[recordingR.ID].EngineGeneration != leaseA.EngineGeneration || currentLeases[recordingS.ID].EngineGeneration != leaseS.EngineGeneration || currentLeases[watchRecordingID].EngineGeneration != watchLease.EngineGeneration {
		t.Fatalf("recording generation pins are inconsistent during overlap: leases=%+v", currentLeases)
	}
	if got := len(waitWatchRelationCount(t, client, baseURL, watchID, 1, 5*time.Second)); got != 1 {
		t.Fatalf("Watch W created duplicate recordings for one live session: relation count=%d", got)
	}

	// End B-owned work before proving that A's last lease is the only blocker.
	setWatchEnabled(t, client, baseURL, watchID, false)
	stopRecording(t, client, baseURL, watchRecordingID)
	stopRecording(t, client, baseURL, recordingS.ID)
	stopRecording(t, client, baseURL, recordingR.ID)
	waitRuntimeRecordingState(t, client, baseURL, recordingR.ID, domain.StateStopped, 20*time.Second)
	waitLeaseAbsent(t, dataDir, recordingR.ID, 20*time.Second)
	if err := waitProcessAbsent(t, engineABinary, 30*time.Second); err != nil {
		t.Fatalf("A Engine was not retired after R's final generation lease reached zero: %v", err)
	}
	state := readRuntimeGenerationSnapshot(t, dataDir)
	if lease, exists := state.Leases[recordingR.ID]; exists {
		t.Fatalf("R lease remained after stop/drain: %+v", lease)
	}
	retained, exists := state.Generations[leaseA.EngineGeneration]
	if !exists || retained.State != generation.StateDraining || !retained.EngineDormant || state.PreviousGenerationID != leaseA.EngineGeneration {
		t.Fatalf("A rollback generation was not retained dormant after its final lease: generation=%+v exists=%v snapshot=%+v", retained, exists, state)
	}
	if _, err := os.Stat(filepath.Join(artifacts.bundleA, "control-plane")); err != nil {
		t.Fatalf("immutable A rollback release was removed: %v", err)
	}
	if status.PreviousRelease == nil || status.PreviousRelease.Version != e2eVersionA || status.PreviousRelease.Commit != e2eCommitA {
		t.Fatalf("Host status no longer exposes retained A rollback release: %+v", status.PreviousRelease)
	}
	registryPath := filepath.Join(dataDir, "runtime", "state", "generations.json")
	registryInfo, err := os.Stat(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	reconcileAfter := registryInfo.ModTime()
	waitRuntimeCondition(t, 12*time.Second, func() bool {
		info, err := os.Stat(registryPath)
		return err == nil && info.ModTime().After(reconcileAfter)
	}, "post-drain lease reconciliation")
	status, code = getRuntimeJSON[httpapi.Status](t, client, baseURL, http.MethodGet, httpapi.Endpoint, nil)
	if code != http.StatusOK || status.ActiveControl == nil || status.ActiveControl.Version != e2eVersionB || status.PreviousRelease == nil || status.PreviousRelease.Version != e2eVersionA {
		t.Fatalf("Runtime Host did not remain ready after dormant-generation lease reconciliation: code=%d status=%+v", code, status)
	}
	if processForBinary(controlABinary) != 0 || processForBinary(engineABinary) != 0 {
		t.Fatal("release A product processes remain after terminal drain")
	}
	if processForBinary(artifacts.hostA) == 0 {
		t.Fatal("Runtime Host exited before scenario cleanup")
	}
	// Keep evidence that the same session created one Watch relation and that
	// the new manual Recording used B after the route switch.
	if recordingS.State == domain.StateInterrupted || recordingS.ID == recordingR.ID {
		t.Fatalf("post-update Recording S identity/state is invalid: %+v", recordingS)
	}
	t.Logf("production process E2E iteration %d: host pid=%d; Control A pid=%d exited; Engine A pid=%d drained after R; Control B pid=%d; Engine B pid=%d; R %s sequence=1..60 on A; S %s on B; Watch session %s produced %s", iteration, command.Process.Pid, controlAPID, engineAPID, controlBPID, engineBPID, recordingR.ID, recordingS.ID, fmt.Sprintf("session-w-%d", iteration), watchRecordingID)
}

func verifyInstalledRuntimeRelease(directory string, manifest release.Manifest) error {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0500 {
		return errors.New("installed release directory is not immutable/private")
	}
	for _, artifact := range manifest.Artifacts {
		path := filepath.Join(directory, artifact.Filename)
		fileInfo, err := os.Lstat(path)
		if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Mode()&os.ModeSymlink != 0 || fileInfo.Size() != artifact.Size || fileInfo.Mode().Perm() != 0500 {
			return fmt.Errorf("installed artifact %s metadata differs from signed manifest", artifact.Role)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
			return fmt.Errorf("installed artifact %s hash differs from signed manifest", artifact.Role)
		}
	}
	return nil
}

func makeRuntimeE2ETreeWritable(root string) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		mode := info.Mode().Perm()
		if info.IsDir() {
			mode |= 0700
		} else {
			mode |= 0600
		}
		_ = os.Chmod(path, mode)
		return nil
	})
}

func waitRuntimeHostStatus(t *testing.T, ctx context.Context, client *http.Client, baseURL string, process *runtimeHostProcess) httpapi.Status {
	t.Helper()
	return waitRuntimeStatus(t, ctx, client, baseURL, func(status httpapi.Status) bool {
		return status.Host.Version == e2eVersionA
	}, process)
}

func waitRuntimeStatus(t *testing.T, ctx context.Context, client *http.Client, baseURL string, condition func(httpapi.Status) bool, process ...*runtimeHostProcess) httpapi.Status {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		var status httpapi.Status
		code, err := requestRuntimeJSON(client, baseURL, http.MethodGet, httpapi.Endpoint, map[string]any{}, &status)
		if err == nil && code == http.StatusOK && condition(status) {
			return status
		}
		for _, item := range process {
			select {
			case <-item.done:
				t.Fatalf("production Runtime Host exited before update API readiness (err=%v): %s", item.err, item.output.String())
			default:
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for Runtime Host status failed: %v", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("Runtime Host status did not reach expected state at %s", baseURL)
	return httpapi.Status{}
}

func getRuntimeJSON[T any](t *testing.T, client *http.Client, baseURL, method, path string, body any) (T, int) {
	t.Helper()
	var result T
	code, err := requestRuntimeJSON(client, baseURL, method, path, body, &result)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return result, code
}

func requestRuntimeJSON(client *http.Client, baseURL, method, path string, body any, target any) (int, error) {
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		requestBody = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequest(method, baseURL+path, requestBody)
	if err != nil {
		return 0, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return response.StatusCode, err
	}
	if target != nil && len(data) != 0 {
		if err := json.Unmarshal(data, target); err != nil {
			return response.StatusCode, fmt.Errorf("decode response status %d: %w; body=%s", response.StatusCode, err, boundedOutput(data))
		}
	}
	return response.StatusCode, nil
}

func createRuntimeRecording(t *testing.T, client *http.Client, baseURL, title string, input map[string]string) domain.Recording {
	t.Helper()
	value, code := getRuntimeJSON[domain.Recording](t, client, baseURL, http.MethodPost, "/api/recordings", map[string]any{
		"adapter_id": runtimeE2EAdapterID, "input": input, "title": title, "preview_mode": "disabled",
	})
	if code != http.StatusCreated {
		t.Fatalf("create recording through Control API returned %d: %+v", code, value)
	}
	return value
}

func createRuntimeWatch(t *testing.T, client *http.Client, baseURL, title string, input map[string]string) string {
	t.Helper()
	value, code := getRuntimeJSON[map[string]any](t, client, baseURL, http.MethodPost, "/api/watches", map[string]any{
		"adapter_id": runtimeE2EAdapterID, "input": input, "title": title, "preview_mode": "disabled", "check_interval_seconds": 2,
	})
	if code != http.StatusCreated {
		t.Fatalf("create Watch through Control API returned %d: %#v", code, value)
	}
	id, _ := value["id"].(string)
	if len(id) != 32 {
		t.Fatalf("Watch API returned invalid ID: %#v", value)
	}
	return id
}

func waitFixtureWatchPoll(t *testing.T, fixture *runtimeUpdateFixture, stream string, timeout time.Duration) {
	t.Helper()
	waitRuntimeCondition(t, timeout, func() bool { return fixture.watchRequestCount(stream) > 0 }, "offline Watch poll")
}

func waitWatchPollingAfter(t *testing.T, fixture *runtimeUpdateFixture, stream string, after time.Time, timeout time.Duration) error {
	t.Helper()
	return waitRuntimeConditionError(timeout, func() bool {
		f := fixture
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, at := range f.streams[stream].watchRequests {
			if at.After(after) {
				return true
			}
		}
		return false
	}, "post-activation Watch poll")
}

func waitWatchRecording(t *testing.T, client *http.Client, baseURL, watchID string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		value, code := getRuntimeJSON[map[string]any](t, client, baseURL, http.MethodGet, "/api/watches/"+watchID, nil)
		if code == http.StatusOK && value["state"] == "recording" {
			if id, _ := value["current_recording_id"].(string); len(id) == 32 {
				return id
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Watch %s did not create a recording; latest=%#v", watchID, mustRuntimeMap(t, client, baseURL, "/api/watches/"+watchID))
	return ""
}

type watchRecordingSummary struct {
	ID string `json:"id"`
}

func waitWatchRelationCount(t *testing.T, client *http.Client, baseURL, watchID string, expected int, timeout time.Duration) []watchRecordingSummary {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		value, code := getRuntimeJSON[struct {
			Items []watchRecordingSummary `json:"items"`
		}](t, client, baseURL, http.MethodGet, "/api/watches/"+watchID+"/recordings?limit=20", nil)
		if code == http.StatusOK && len(value.Items) == expected {
			return value.Items
		}
		time.Sleep(100 * time.Millisecond)
	}
	value := mustRuntimeMap(t, client, baseURL, "/api/watches/"+watchID+"/recordings?limit=20")
	t.Fatalf("Watch relation count did not become %d: %#v", expected, value)
	return nil
}

func mustRuntimeMap(t *testing.T, client *http.Client, baseURL, path string) map[string]any {
	t.Helper()
	value, code := getRuntimeJSON[map[string]any](t, client, baseURL, http.MethodGet, path, nil)
	if code != http.StatusOK {
		t.Fatalf("GET %s returned %d: %#v", path, code, value)
	}
	return value
}

func getRecording(t *testing.T, client *http.Client, baseURL, id string) *domain.Recording {
	t.Helper()
	recording, code := getRuntimeJSON[domain.Recording](t, client, baseURL, http.MethodGet, "/api/recordings/"+id, nil)
	if code != http.StatusOK {
		t.Fatalf("get recording %s returned %d", id, code)
	}
	return &recording
}

func waitRecordingSequenceCount(t *testing.T, client *http.Client, baseURL, id string, minimum int, timeout time.Duration) {
	t.Helper()
	if err := waitRuntimeRecordingSequenceCount(client, baseURL, id, minimum, timeout); err != nil {
		t.Fatal(err)
	}
}

func waitRuntimeRecordingSequenceCount(client *http.Client, baseURL, id string, minimum int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var latest *domain.Recording
	for time.Now().Before(deadline) {
		var recording domain.Recording
		code, err := requestRuntimeJSON(client, baseURL, http.MethodGet, "/api/recordings/"+id, nil, &recording)
		if err == nil && code == http.StatusOK {
			latest = &recording
			if recording.Tracks["main"] != nil && len(recording.Tracks["main"].Segments) >= minimum {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if latest == nil {
		return fmt.Errorf("recording %s could not be read while waiting for %d segments", id, minimum)
	}
	var count int
	var sequences []uint64
	if track := latest.Tracks["main"]; track != nil {
		count = len(track.Segments)
		sequences = recordingSequences(latest)
	}
	return fmt.Errorf("recording %s did not reach %d segments (state=%s count=%d sequences=%v gaps=%+v error=%q)", id, minimum, latest.State, count, sequences, latest.Gaps, latest.LastError)
}

func (f *runtimeUpdateFixture) describe(stream string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	value := f.streams[stream]
	if value == nil {
		return "missing stream"
	}
	return fmt.Sprintf("latest=%d token_generation=%d expired=%t manifests=%+v refreshes=%+v segment_requests=%+v", value.latest, value.tokenGeneration, value.expired, value.manifestRequests, value.refreshes, value.segmentRequests)
}

func waitMetadataTimeline(t *testing.T, ctx context.Context, client *http.Client, baseURL, id string, minimum int, timeout time.Duration) struct {
	Current   *domain.MetadataRevision  `json:"current"`
	Items     []domain.MetadataRevision `json:"items"`
	Truncated bool                      `json:"truncated"`
} {
	t.Helper()
	type response struct {
		Current   *domain.MetadataRevision  `json:"current"`
		Items     []domain.MetadataRevision `json:"items"`
		Truncated bool                      `json:"truncated"`
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var value response
		code, err := requestRuntimeJSON(client, baseURL, http.MethodGet, "/api/recordings/"+id+"/metadata", nil, &value)
		if err == nil && code == http.StatusOK && len(value.Items) >= minimum {
			return value
		}
		select {
		case <-ctx.Done():
			t.Fatalf("metadata timeline wait canceled: %v", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Fatalf("recording %s metadata timeline did not reach %d items", id, minimum)
	return response{}
}

func stringValue(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

func recordingSequences(recording *domain.Recording) []uint64 {
	if recording == nil || recording.Tracks["main"] == nil {
		return nil
	}
	result := make([]uint64, 0, len(recording.Tracks["main"].Segments))
	for _, segment := range recording.Tracks["main"].Segments {
		result = append(result, segment.Sequence)
	}
	return result
}

func equalSequenceRange(sequences []uint64, first, last uint64) bool {
	if uint64(len(sequences)) != last-first+1 {
		return false
	}
	for index, sequence := range sequences {
		if sequence != first+uint64(index) {
			return false
		}
	}
	return true
}

func verifyRuntimeRecordingSegments(t *testing.T, dataDir string, recording *domain.Recording, stream string, first, last uint64) {
	t.Helper()
	if recording == nil || recording.Tracks["main"] == nil {
		t.Fatal("recording has no main track")
	}
	segments := recording.Tracks["main"].Segments
	if uint64(len(segments)) < last-first+1 {
		t.Fatalf("recording segment count=%d, want at least %d", len(segments), last-first+1)
	}
	store, err := storage.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	seenOrdinals := make(map[uint64]struct{}, len(segments))
	seenSequence := make(map[uint64]struct{}, len(segments))
	for _, segment := range segments {
		if _, exists := seenOrdinals[segment.ArchiveOrdinal]; exists {
			t.Fatalf("duplicate archive ordinal %d", segment.ArchiveOrdinal)
		}
		seenOrdinals[segment.ArchiveOrdinal] = struct{}{}
		if _, exists := seenSequence[segment.Sequence]; exists {
			t.Fatalf("duplicate source sequence %d", segment.Sequence)
		}
		seenSequence[segment.Sequence] = struct{}{}
		if segment.Sequence < first || segment.Sequence > last {
			continue
		}
		reader, err := store.OpenPayloadReader(recording.ID, segment.StoragePath)
		if err != nil {
			t.Fatalf("open canonical payload for sequence %d: %v", segment.Sequence, err)
		}
		payload, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		digest := sha256.Sum256(payload)
		if readErr != nil || closeErr != nil || string(payload) != runtimeSegmentPayload(stream, segment.Sequence) || int64(len(payload)) != segment.PayloadSize || hex.EncodeToString(digest[:]) != segment.SHA256 {
			t.Fatalf("canonical payload/hash mismatch for source sequence %d: read=%v close=%v", segment.Sequence, readErr, closeErr)
		}
	}
	for sequence := first; sequence <= last; sequence++ {
		if _, exists := seenSequence[sequence]; !exists {
			t.Fatalf("canonical recording is missing source sequence %d", sequence)
		}
	}
}

func waitRecordingLease(t *testing.T, dataDir, recordingID string, timeout time.Duration) generation.Lease {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state := readRuntimeGenerationSnapshot(t, dataDir)
		if lease, ok := state.Leases[recordingID]; ok {
			return lease
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("recording %s did not receive a Host generation lease", recordingID)
	return generation.Lease{}
}

func waitLeaseAbsent(t *testing.T, dataDir, recordingID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, exists := readRuntimeGenerationSnapshot(t, dataDir).Leases[recordingID]; !exists {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("recording %s Host generation lease did not clear", recordingID)
}

func readRuntimeLeases(t *testing.T, dataDir string) map[string]generation.Lease {
	t.Helper()
	return readRuntimeGenerationSnapshot(t, dataDir).Leases
}

func readRuntimeGenerationSnapshot(t *testing.T, dataDir string) generation.Snapshot {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, "runtime", "state", "generations.json"))
	if err != nil {
		t.Fatalf("read Host runtime diagnostics: %v", err)
	}
	var snapshot generation.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("decode Host runtime diagnostics: %v", err)
	}
	return snapshot
}

func hasGeneration(values []httpapi.GenerationSummary, version string, minLeases int) bool {
	for _, value := range values {
		if value.Version == version && value.ActiveRecordings >= minLeases {
			return true
		}
	}
	return false
}

func waitRuntimeRecordingState(t *testing.T, client *http.Client, baseURL, recordingID string, expected domain.RecordingState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		current := getRecording(t, client, baseURL, recordingID)
		if current.State == expected {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("recording %s did not reach terminal state %q", recordingID, expected)
}

func stopRecording(t *testing.T, client *http.Client, baseURL, recordingID string) {
	t.Helper()
	var value domain.Recording
	code, err := requestRuntimeJSON(client, baseURL, http.MethodPost, "/api/recordings/"+recordingID+"/stop", map[string]any{}, &value)
	if err != nil || (code != http.StatusOK && code != http.StatusConflict) {
		t.Fatalf("stop recording %s returned code=%d err=%v", recordingID, code, err)
	}
}

func setWatchEnabled(t *testing.T, client *http.Client, baseURL, watchID string, enabled bool) {
	t.Helper()
	method := http.MethodPost
	path := "/api/watches/" + watchID + "/disable"
	if enabled {
		path = "/api/watches/" + watchID + "/enable"
	}
	var value map[string]any
	code, err := requestRuntimeJSON(client, baseURL, method, path, map[string]any{}, &value)
	if err != nil || code != http.StatusOK {
		t.Fatalf("set Watch enabled=%v returned code=%d err=%v", enabled, code, err)
	}
}

func containsTimeAfter(values []time.Time, after time.Time) bool {
	for _, value := range values {
		if value.After(after) {
			return true
		}
	}
	return false
}

func waitRuntimeCondition(t *testing.T, timeout time.Duration, condition func() bool, what string) {
	t.Helper()
	if err := waitRuntimeConditionError(timeout, condition, what); err != nil {
		t.Fatal(err)
	}
}

func waitRuntimeConditionError(timeout time.Duration, condition func() bool, what string) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", what)
}

func reserveRuntimeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

type runtimeProcessInfo struct {
	PID     int
	Parent  int
	Command string
}

func runtimeProcessTable() ([]runtimeProcessInfo, error) {
	output, err := exec.Command("ps", "-axo", "pid=,ppid=,command=").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("read process table: %w: %s", err, boundedOutput(output))
	}
	lines := strings.Split(string(output), "\n")
	result := make([]runtimeProcessInfo, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		if pidErr != nil || parentErr != nil {
			continue
		}
		result = append(result, runtimeProcessInfo{PID: pid, Parent: parent, Command: strings.Join(fields[2:], " ")})
	}
	return result, nil
}

func processForBinary(path string) int {
	processes, err := runtimeProcessTable()
	if err != nil {
		return 0
	}
	clean := filepath.Clean(path)
	for _, process := range processes {
		if strings.Contains(process.Command, clean) {
			return process.PID
		}
	}
	return 0
}

func waitProcessForBinary(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pid := processForBinary(path); pid != 0 {
			return pid
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("process for production executable %s did not start", filepath.Base(path))
	return 0
}

func waitProcessAbsent(t *testing.T, path string, timeout time.Duration) error {
	t.Helper()
	return waitRuntimeConditionError(timeout, func() bool { return processForBinary(path) == 0 }, "process exit for "+filepath.Base(path))
}

func stopRuntimeHostProcess(process *runtimeHostProcess) {
	if process == nil || process.command == nil || process.command.Process == nil {
		return
	}
	select {
	case <-process.done:
		return
	default:
	}
	_ = process.command.Process.Signal(syscall.SIGTERM)
	select {
	case <-process.done:
	case <-time.After(25 * time.Second):
		_ = process.command.Process.Kill()
		select {
		case <-process.done:
		case <-time.After(5 * time.Second):
		}
	}
}
