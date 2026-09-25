package acquire_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/acquire"
	"github.com/dltkddnr04/integrated-recorder/internal/adapterhost"
	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
	"github.com/dltkddnr04/integrated-recorder/internal/domain"
	"github.com/dltkddnr04/integrated-recorder/internal/pluginconfig"
	"github.com/dltkddnr04/integrated-recorder/internal/server"
	"github.com/dltkddnr04/integrated-recorder/internal/storage"
)

func TestMain(m *testing.M) {
	if os.Getenv("IR_ACQUIRE_ADAPTER_HELPER") == "1" {
		os.Exit(runAcquireAdapterHelper())
	}
	os.Exit(m.Run())
}

func runAcquireAdapterHelper() int {
	reader := bufio.NewReader(os.Stdin)
	for {
		request, err := adapterproto.ReadRequest(reader)
		if err == io.EOF {
			return 0
		}
		if err != nil {
			return 2
		}
		var response adapterproto.Response
		switch request.Method {
		case adapterproto.MethodDescribe:
			descriptor := adapterproto.Descriptor{ID: "test-helper", Name: "Test Helper", Version: "1", ProtocolVersion: adapterproto.Version, Capabilities: []string{"resolve"}, InputSchema: adapterproto.Schema{Fields: []adapterproto.Field{{Key: "manifest_url", Control: "text", Label: "Manifest URL", Required: true}}}, ConfigurationSchema: adapterproto.Schema{Fields: []adapterproto.Field{{Key: "opaque_value", Control: "text", Label: "Opaque value"}, {Key: "opaque_secret", Control: "secret", Label: "Opaque secret"}}}, MediaTypes: []string{"hls"}}
			response, _ = adapterproto.Success(request.ID, descriptor)
		case adapterproto.MethodResolve:
			var params adapterproto.ResolveParams
			_ = json.Unmarshal(request.Params, &params)
			var input struct {
				ManifestURL string            `json:"manifest_url"`
				Headers     map[string]string `json:"headers"`
			}
			_ = json.Unmarshal(params.Input, &input)
			metadata, _ := json.Marshal(map[string]bool{"secret_received": params.Secrets["opaque_secret"] != "", "value_received": len(params.Configuration["opaque_value"]) > 0})
			response, _ = adapterproto.Success(request.ID, adapterproto.MediaSource{Type: "hls", ManifestURL: input.ManifestURL, Headers: input.Headers, Metadata: metadata})
		case adapterproto.MethodShutdown:
			response, _ = adapterproto.Success(request.ID, map[string]bool{"stopped": true})
		default:
			response = adapterproto.Failure(request.ID, "unsupported_method", "unsupported", nil)
		}
		if err = adapterproto.WriteResponse(os.Stdout, response); err != nil {
			return 3
		}
		if request.Method == adapterproto.MethodShutdown {
			return 0
		}
	}
}

func writeAcquireAdapter(t *testing.T, dir string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nIR_ACQUIRE_ADAPTER_HELPER=1 exec " + shellQuote(binary) + "\n"
	if err = os.WriteFile(filepath.Join(dir, "integrated-recorder-adapter-test-helper"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func TestLocalHTTPAcquireStopReloadAndVOD(t *testing.T) {
	initBytes := []byte{0, 1, 2, 3, 4, 5, 6, 7}
	blobBytes := []byte{0xff, 0x00, 0x10, 0x20, 0x30, 0x40, 0x50}
	var mu sync.Mutex
	mediaRequests := 0
	segmentRequests := map[string]int{}
	secondCaptured := make(chan struct{})
	thirdMedia := make(chan struct{})
	var secondOnce, thirdOnce sync.Once

	source := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hls/stream.m3u8":
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=900000\nrendition.m3u8\n")
		case "/hls/rendition.m3u8":
			mu.Lock()
			mediaRequests++
			requestNum := mediaRequests
			mu.Unlock()
			if requestNum >= 3 {
				thirdOnce.Do(func() { close(thirdMedia) })
			}
			playlist := "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1.25,\n#EXT-X-BYTERANGE:4@0\nblob.m4s\n"
			if requestNum >= 2 {
				playlist += "#EXTINF:1.5,\n#EXT-X-BYTERANGE:3@4\nblob.m4s\n"
			}
			_, _ = io.WriteString(w, playlist)
		case "/hls/init.mp4":
			_, _ = w.Write(initBytes)
		case "/hls/blob.m4s":
			rangeValue := r.Header.Get("Range")
			var start, end int
			if _, err := fmt.Sscanf(rangeValue, "bytes=%d-%d", &start, &end); err != nil || start < 0 || end >= len(blobBytes) || end < start {
				http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			mu.Lock()
			segmentRequests[rangeValue]++
			mu.Unlock()
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(blobBytes)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(blobBytes[start : end+1])
			if start == 4 {
				secondOnce.Do(func() { close(secondCaptured) })
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer source.Close()

	dataDir := t.TempDir()
	store, err := storage.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	validate := func(context.Context, string) error { return nil }
	resolver := fixtureResolver{manifestURL: source.URL + "/hls/stream.m3u8"}
	manager, err := acquire.NewManager(store, source.Client(), resolver, validate)
	if err != nil {
		t.Fatal(err)
	}
	recording, err := manager.Start(context.Background(), "fixture", json.RawMessage(`{"opaque":"fixture"}`), nil, "local fixture")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondCaptured:
	case <-time.After(5 * time.Second):
		t.Fatal("second original byte range was not captured")
	}
	select {
	case <-thirdMedia:
	case <-time.After(5 * time.Second):
		t.Fatal("manifest was not polled repeatedly")
	}
	time.Sleep(100 * time.Millisecond)
	stopped, err := manager.Stop(recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != domain.StateStopped {
		t.Fatalf("state = %q", stopped.State)
	}
	mu.Lock()
	gotMediaRequests := mediaRequests
	firstCount, secondCount := segmentRequests["bytes=0-3"], segmentRequests["bytes=4-6"]
	mu.Unlock()
	if gotMediaRequests < 3 {
		t.Fatalf("media polls = %d, want repeated polls", gotMediaRequests)
	}
	if firstCount != 1 || secondCount != 1 {
		t.Fatalf("range fetch counts = %d, %d; duplicate suppression failed", firstCount, secondCount)
	}
	track := stopped.Tracks["main"]
	if len(track.Segments) != 2 || track.Segments[0].Sequence != 10 || track.Segments[1].Sequence != 11 {
		t.Fatalf("stored segments = %#v", track.Segments)
	}
	if track.Segments[0].InitSegmentID == "" || len(track.InitSegments) != 1 {
		t.Fatalf("init segment was not linked: %#v", track)
	}
	for i, expected := range [][]byte{blobBytes[0:4], blobBytes[4:7]} {
		segment := track.Segments[i]
		stored, readErr := os.ReadFile(filepath.Join(store.Root(), "recordings", recording.ID, filepath.FromSlash(segment.StoragePath)))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(stored) != string(expected) {
			t.Fatalf("segment %d bytes = %v, want %v", i, stored, expected)
		}
		hash := sha256.Sum256(expected)
		if segment.SHA256 != fmt.Sprintf("%x", hash[:]) || segment.PayloadSize != int64(len(expected)) {
			t.Fatalf("segment integrity metadata incorrect: %#v", segment)
		}
	}

	// A new manager simulates a full process restart and reads only directory metadata.
	reloaded, err := acquire.NewManager(store, source.Client(), resolver, validate)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := reloaded.Get(recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	if previous.State != domain.StateStopped || previous.SegmentCount() != 2 {
		t.Fatalf("reloaded recording %#v", previous)
	}
	api := newIPv4TestServer(t, server.New(reloaded, nil, nil))
	defer api.Close()
	response, err := api.Client().Get(api.URL + "/api/recordings/" + recording.ID + "/play/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	master, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(master), "playlist.m3u8") {
		t.Fatalf("master status/body = %d %s", response.StatusCode, master)
	}
	response, err = api.Client().Get(api.URL + "/api/recordings/" + recording.ID + "/play/tracks/main/playlist.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	playlistBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	playlist := string(playlistBody)
	if response.StatusCode != http.StatusOK || !strings.Contains(playlist, "#EXT-X-PLAYLIST-TYPE:VOD") || !strings.Contains(playlist, "#EXT-X-MAP:") || !strings.Contains(playlist, "#EXT-X-ENDLIST") || strings.Index(playlist, "seg-00000000000000000010") > strings.Index(playlist, "seg-00000000000000000011") {
		t.Fatalf("VOD playlist invalid:\n%s", playlist)
	}
	response, err = api.Client().Get(api.URL + "/api/recordings/" + recording.ID + "/play/segments/seg-00000000000000000010")
	if err != nil {
		t.Fatal(err)
	}
	played, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(played) != string(blobBytes[:4]) {
		t.Fatalf("served media bytes = %v, status %d", played, response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); got != "video/iso.segment" {
		t.Fatalf(".m4s Content-Type = %q", got)
	}
	if strings.Contains(playlist, "#EXT-X-DISCONTINUITY") {
		t.Fatalf("unexpected discontinuity: %s", playlist)
	}
}

func TestExternalAdapterSubprocessAcquireReloadAndVOD(t *testing.T) {
	const headerName = "X-Generic-Session"
	const headerValue = "test-only-sensitive-value"
	payload := []byte{0x00, 0xff, 0x10, 0x80, 0x01}
	captured := make(chan struct{})
	var captureOnce sync.Once
	var gotManifestHeader, gotSegmentHeader string
	var mu sync.Mutex
	source := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.URL.Path == "/live.m3u8" {
			gotManifestHeader = r.Header.Get(headerName)
		} else if r.URL.Path == "/segment.ts" {
			gotSegmentHeader = r.Header.Get(headerName)
		}
		mu.Unlock()
		switch r.URL.Path {
		case "/live.m3u8":
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:1.0,\nsegment.ts\n")
		case "/segment.ts":
			_, _ = w.Write(payload)
			captureOnce.Do(func() { close(captured) })
		default:
			http.NotFound(w, r)
		}
	}))
	defer source.Close()
	dataDir := t.TempDir()
	store, err := storage.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	configStore, secretStore, err := pluginconfig.NewTypedFileStores(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	configs, err := pluginconfig.NewService(configStore, secretStore)
	if err != nil {
		t.Fatal(err)
	}
	adapterDir := t.TempDir()
	writeAcquireAdapter(t, adapterDir)
	host, err := adapterhost.Discover(context.Background(), adapterDir, configs)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if list := host.List(); len(list) != 1 || list[0].Status.State != "ready" {
		t.Fatalf("discovered process = %#v", list)
	}
	validate := func(context.Context, string) error { return nil }
	manager, err := acquire.NewManager(store, source.Client(), host, validate)
	if err != nil {
		t.Fatal(err)
	}
	resource := &adapterproto.ResourceRef{Type: "arbitrary-kind", ID: "opaque/resource/id", Parent: &adapterproto.ResourceRef{Type: "outer", ID: "parent"}}
	input, _ := json.Marshal(map[string]any{"manifest_url": source.URL + "/live.m3u8", "headers": map[string]string{headerName: headerValue}})
	descriptor, err := host.Descriptor("test-helper")
	if err != nil {
		t.Fatal(err)
	}
	scope := pluginconfig.Scope{PluginID: "test-helper", Resource: resource}
	if err = configs.Put(scope, descriptor.ConfigurationSchema, map[string]json.RawMessage{"opaque_value": json.RawMessage(`"scope-value"`)}, map[string]string{"opaque_secret": "scope-secret"}); err != nil {
		t.Fatal(err)
	}
	resolved, err := host.Resolve(context.Background(), "test-helper", input, resource)
	if err != nil {
		t.Fatal(err)
	}
	var received map[string]bool
	if err = json.Unmarshal(resolved.Metadata, &received); err != nil || !received["secret_received"] || !received["value_received"] {
		t.Fatalf("resource-scoped settings were not passed to adapter: %#v, %v", received, err)
	}
	recording, err := manager.Start(context.Background(), "test-helper", input, resource, "subprocess fixture")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-captured:
	case <-time.After(5 * time.Second):
		t.Fatal("segment was not captured through the adapter process")
	}
	segmentDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(segmentDeadline) {
		current, getErr := manager.Get(recording.ID)
		if getErr == nil && current.SegmentCount() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ready, err := manager.Get(recording.ID)
	if err != nil || ready.SegmentCount() != 1 {
		t.Fatalf("segment was not committed before stop: %#v, %v", ready, err)
	}
	stopped, err := manager.Stop(recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != domain.StateStopped || stopped.AdapterID != "test-helper" || stopped.Resource == nil || stopped.Resource.Type != "arbitrary-kind" {
		t.Fatalf("stopped metadata = %#v", stopped)
	}
	mu.Lock()
	manifestHeader, segmentHeader := gotManifestHeader, gotSegmentHeader
	mu.Unlock()
	if manifestHeader != headerValue || segmentHeader != headerValue {
		t.Fatalf("generic headers not propagated to same origin: manifest=%q segment=%q", manifestHeader, segmentHeader)
	}
	if len(stopped.Tracks["main"].Segments) != 1 {
		t.Fatalf("segments = %#v", stopped.Tracks["main"].Segments)
	}
	segment := stopped.Tracks["main"].Segments[0]
	stored, err := os.ReadFile(filepath.Join(store.Root(), "recordings", recording.ID, filepath.FromSlash(segment.StoragePath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(payload) {
		t.Fatalf("stored payload = %v want %v", stored, payload)
	}
	digest := sha256.Sum256(payload)
	if segment.SHA256 != fmt.Sprintf("%x", digest[:]) {
		t.Fatalf("hash = %s", segment.SHA256)
	}
	recordingJSON, err := os.ReadFile(filepath.Join(store.Root(), "recordings", recording.ID, "recording.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(recordingJSON), headerValue) {
		t.Fatal("media headers or input were persisted in recording metadata")
	}
	reloaded, err := acquire.NewManager(store, source.Client(), host, validate)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := reloaded.Get(recording.ID)
	if err != nil || previous.State != domain.StateStopped || previous.AdapterID != "test-helper" || previous.SegmentCount() != 1 {
		t.Fatalf("reloaded recording = %#v, %v", previous, err)
	}
	api := newIPv4TestServer(t, server.New(reloaded, host, configs))
	defer api.Close()
	response, err := api.Client().Get(api.URL + "/api/recordings/" + recording.ID + "/play/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	master, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(master), "playlist.m3u8") {
		t.Fatalf("master playlist status/body = %d %s", response.StatusCode, master)
	}
	response, err = api.Client().Get(api.URL + "/api/recordings/" + recording.ID + "/play/tracks/main/playlist.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	playlist, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(playlist), "#EXT-X-PLAYLIST-TYPE:VOD") || !strings.Contains(string(playlist), "#EXT-X-ENDLIST") {
		t.Fatalf("VOD playlist status/body = %d %s", response.StatusCode, playlist)
	}
	response, err = api.Client().Get(api.URL + "/api/recordings/" + recording.ID + "/play/segments/" + segment.ID)
	if err != nil {
		t.Fatal(err)
	}
	played, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(played) != string(payload) {
		t.Fatalf("playback segment = %v status %d", played, response.StatusCode)
	}
	putRequest, err := http.NewRequest(http.MethodPut, api.URL+"/api/adapters/test-helper/config", strings.NewReader(`{"secrets":{"opaque_secret":"secret-api-value"}}`))
	if err != nil {
		t.Fatal(err)
	}
	putRequest.Header.Set("Content-Type", "application/json")
	put, err := api.Client().Do(putRequest)
	if err != nil {
		t.Fatal(err)
	}
	configBody, _ := io.ReadAll(put.Body)
	put.Body.Close()
	if put.StatusCode != http.StatusOK || strings.Contains(string(configBody), "secret-api-value") || !strings.Contains(string(configBody), `"configured":true`) {
		t.Fatalf("masked config PUT status/body = %d %s", put.StatusCode, configBody)
	}
	response, err = api.Client().Get(api.URL + "/api/adapters/test-helper/config")
	if err != nil {
		t.Fatal(err)
	}
	configBody, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || strings.Contains(string(configBody), "secret-api-value") || !strings.Contains(string(configBody), `"configured":true`) {
		t.Fatalf("masked config GET status/body = %d %s", response.StatusCode, configBody)
	}
	for _, endpoint := range []string{"/api/adapters", "/api/adapters/test-helper", "/api/adapters/test-helper/schema"} {
		response, err = api.Client().Get(api.URL + endpoint)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s returned %d: %s", endpoint, response.StatusCode, body)
		}
		if endpoint == "/api/adapters/test-helper/schema" && !strings.Contains(string(body), "manifest_url") {
			t.Fatalf("schema response omitted adapter fields: %s", body)
		}
	}
}

type fixtureResolver struct{ manifestURL string }

func (f fixtureResolver) Resolve(context.Context, string, json.RawMessage, *adapterproto.ResourceRef) (adapterproto.MediaSource, error) {
	return adapterproto.MediaSource{Type: "hls", ManifestURL: f.manifestURL}, nil
}

func newIPv4TestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(handler)
	s.Listener = listener
	s.Start()
	return s
}
