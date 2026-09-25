package adapterhost

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
)

func TestMain(m *testing.M) {
	if os.Getenv("IR_ADAPTER_HELPER") == "1" {
		os.Exit(runHelperAdapter())
	}
	os.Exit(m.Run())
}

func runHelperAdapter() int {
	mode := os.Getenv("IR_ADAPTER_MODE")
	reader := bufio.NewReader(os.Stdin)
	for {
		request, err := adapterproto.ReadRequest(reader)
		if err == io.EOF {
			return 0
		}
		if err != nil {
			return 2
		}
		if mode == "hang" && request.Method == adapterproto.MethodDescribe {
			time.Sleep(30 * time.Second)
			return 3
		}
		if mode == "badjson" {
			_, _ = io.WriteString(os.Stdout, "not-json\n")
			return 0
		}
		if mode == "mismatch" {
			response, _ := adapterproto.Success("wrong-id", map[string]any{})
			_ = adapterproto.WriteResponse(os.Stdout, response)
			return 0
		}
		var response adapterproto.Response
		switch request.Method {
		case adapterproto.MethodDescribe:
			protocolVersion := adapterproto.Version
			if mode == "incompatible" {
				protocolVersion = adapterproto.Version + 1
			}
			descriptor := adapterproto.Descriptor{ID: os.Getenv("IR_ADAPTER_ID"), Name: "Test adapter", Version: "1", ProtocolVersion: protocolVersion, Capabilities: []string{"resolve"}, InputSchema: adapterproto.Schema{Fields: []adapterproto.Field{}}, ConfigurationSchema: adapterproto.Schema{Fields: []adapterproto.Field{}}, MediaTypes: []string{"hls"}}
			response, _ = adapterproto.Success(request.ID, descriptor)
		case adapterproto.MethodResolve:
			if mode == "structured_error" {
				const sentinel = "secret-sentinel"
				response = adapterproto.Failure(request.ID, sentinel, sentinel, map[string]string{"echo": sentinel})
			} else {
				var params adapterproto.ResolveParams
				_ = json.Unmarshal(request.Params, &params)
				var input struct {
					ManifestURL string `json:"manifest_url"`
				}
				_ = json.Unmarshal(params.Input, &input)
				response, _ = adapterproto.Success(request.ID, adapterproto.MediaSource{Type: "hls", ManifestURL: input.ManifestURL})
			}
		case adapterproto.MethodShutdown:
			response, _ = adapterproto.Success(request.ID, map[string]bool{"stopped": true})
		default:
			response = adapterproto.Failure(request.ID, "unsupported_method", "unsupported", nil)
		}
		_ = adapterproto.WriteResponse(os.Stdout, response)
		if request.Method == adapterproto.MethodShutdown || mode == "exitafterdescribe" && request.Method == adapterproto.MethodDescribe {
			return 0
		}
	}
}

func TestDiscoverExecutableAndIgnoresNonExecutable(t *testing.T) {
	dir := t.TempDir()
	writeAdapter(t, dir, "integrated-recorder-adapter-ready", "normal", "ready", true)
	writeAdapter(t, dir, "integrated-recorder-adapter-ignored", "normal", "ignored", false)
	host, err := Discover(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	list := host.List()
	if len(list) != 1 || list[0].Status.ID != "ready" || list[0].Status.State != "ready" {
		t.Fatalf("adapters = %#v", list)
	}
	got, err := host.Get("ready")
	if err != nil || got.Descriptor == nil || got.Descriptor.Name != "Test adapter" {
		t.Fatalf("Get = %#v, %v", got, err)
	}
}

func TestDuplicateIDRejectedAndFailedProcessIsolated(t *testing.T) {
	dir := t.TempDir()
	writeAdapter(t, dir, "integrated-recorder-adapter-one", "normal", "duplicate", true)
	writeAdapter(t, dir, "integrated-recorder-adapter-two", "normal", "duplicate", true)
	writeAdapter(t, dir, "integrated-recorder-adapter-bad", "badjson", "bad", true)
	host, err := Discover(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if _, err = host.Get("duplicate"); err != nil {
		t.Fatal(err)
	}
	duplicate, _ := host.Get("duplicate")
	if duplicate.Status.State != "rejected" {
		t.Fatalf("duplicate status = %#v", duplicate.Status)
	}
	duplicateCount := 0
	for _, adapter := range host.List() {
		if adapter.Status.ID == "duplicate" {
			duplicateCount++
		}
	}
	if duplicateCount != 1 {
		t.Fatalf("duplicate id appeared %d times in discovery list", duplicateCount)
	}
	bad, err := host.Get("bad")
	if err != nil || bad.Status.State != "failed" && bad.Status.State != "unavailable" {
		t.Fatalf("failed adapter status = %#v, %v", bad, err)
	}
}

func TestWrongRequestIDAndDescribeTimeoutFailSafely(t *testing.T) {
	for _, tc := range []struct{ name, mode string }{{"mismatch", "mismatch"}, {"timeout", "hang"}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeAdapter(t, dir, "integrated-recorder-adapter-"+tc.name, tc.mode, tc.name, true)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			host, err := Discover(ctx, dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer host.Close()
			adapter, err := host.Get(tc.name)
			if err != nil || adapter.Status.State != "failed" && adapter.Status.State != "unavailable" {
				t.Fatalf("status = %#v, %v", adapter, err)
			}
		})
	}
}

func TestIncompatibleDescriptorVersionIsRejected(t *testing.T) {
	dir := t.TempDir()
	writeAdapter(t, dir, "integrated-recorder-adapter-incompatible", "incompatible", "incompatible", true)
	host, err := Discover(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	adapter, err := host.Get("incompatible")
	if err != nil || adapter.Status.State != "failed" {
		t.Fatalf("incompatible adapter = %#v, %v", adapter, err)
	}
}

func TestResolveUsesGenericInputAndHidesStructuredAdapterErrorFields(t *testing.T) {
	dir := t.TempDir()
	writeAdapter(t, dir, "integrated-recorder-adapter-good", "normal", "good", true)
	writeAdapter(t, dir, "integrated-recorder-adapter-errors", "structured_error", "errors", true)
	host, err := Discover(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	media, err := host.Resolve(context.Background(), "good", json.RawMessage(`{"manifest_url":"https://stream.example/live.m3u8"}`), nil)
	if err != nil || media.Type != "hls" || media.ManifestURL != "https://stream.example/live.m3u8" {
		t.Fatalf("resolve = %#v, %v", media, err)
	}
	_, err = host.Resolve(context.Background(), "errors", json.RawMessage(`{"manifest_url":"https://stream.example/live.m3u8"}`), nil)
	if err == nil || err.Error() != "adapter returned an error" || strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatalf("adapter error was not replaced with a stable generic error: %v", err)
	}
}

func TestProcessExitUpdatesReportedStatus(t *testing.T) {
	dir := t.TempDir()
	writeExitingAdapter(t, dir, "integrated-recorder-adapter-exit")
	host, err := Discover(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		item, _ := host.Get("exit")
		if item.Status.State == "unavailable" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	item, _ := host.Get("exit")
	t.Fatalf("process exit state = %q", item.Status.State)
}

func writeExitingAdapter(t *testing.T, dir, name string) {
	t.Helper()
	response := `{"protocol_version":1,"id":"1","result":{"id":"exit","name":"Exit adapter","version":"1","protocol_version":1,"capabilities":["resolve"],"input_schema":{"fields":[]},"configuration_schema":{"fields":[]},"resource_types":[],"media_types":["hls"]}}`
	script := "#!/bin/sh\nIFS= read -r request || exit 1\nprintf '%s\\n' '" + response + "'\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func writeAdapter(t *testing.T, dir, name, mode, id string, executable bool) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nIR_ADAPTER_HELPER=1 IR_ADAPTER_MODE=%s IR_ADAPTER_ID=%s exec %s\n", shellQuote(mode), shellQuote(id), shellQuote(binary))
	path := filepath.Join(dir, name)
	permissions := os.FileMode(0600)
	if executable {
		permissions = 0700
	}
	if err = os.WriteFile(path, []byte(script), permissions); err != nil {
		t.Fatal(err)
	}
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
