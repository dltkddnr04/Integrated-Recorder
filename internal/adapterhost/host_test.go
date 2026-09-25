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
	"github.com/dltkddnr04/integrated-recorder/internal/interaction"
	"github.com/dltkddnr04/integrated-recorder/internal/pluginconfig"
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
			capabilities := []string{"resolve"}
			configurationSchema := adapterproto.Schema{Fields: []adapterproto.Field{}}
			resourceTypes := []adapterproto.ResourceType{}
			if mode == "required_config" || mode == "required_inherited" || mode == "workflow_required" {
				configurationSchema.Fields = []adapterproto.Field{{Key: "required_setting", Control: "text", Label: "Required setting", Required: true}}
			}
			if mode == "required_inherited" {
				configurationSchema = adapterproto.Schema{Fields: []adapterproto.Field{}}
				resourceTypes = []adapterproto.ResourceType{
					{Type: "alpha", ConfigurationSchema: adapterproto.Schema{Fields: []adapterproto.Field{{Key: "required_setting", Control: "text", Label: "Required setting", Required: true, Inherit: true}}}},
					{Type: "beta", ConfigurationSchema: adapterproto.Schema{Fields: []adapterproto.Field{}}},
				}
			}
			if mode == "workflow_required" {
				capabilities = []string{adapterproto.CapabilityResolveWorkflow}
			}
			descriptor := adapterproto.Descriptor{ID: os.Getenv("IR_ADAPTER_ID"), Name: "Test adapter", Version: "1", ProtocolVersion: protocolVersion, Capabilities: capabilities, InputSchema: adapterproto.Schema{Fields: []adapterproto.Field{{Key: "manifest_url", Control: "text", Label: "Manifest URL", Required: true}}}, ConfigurationSchema: configurationSchema, ResourceTypes: resourceTypes, MediaTypes: []string{"hls"}}
			response, _ = adapterproto.Success(request.ID, descriptor)
		case adapterproto.MethodResolveBegin:
			var params adapterproto.ResolveBeginParams
			_ = json.Unmarshal(request.Params, &params)
			challenge := &adapterproto.WorkflowChallenge{Schema: adapterproto.Schema{Fields: []adapterproto.Field{{Key: "required_setting", Control: "text", Label: "Required setting", Required: true}}}}
			response, _ = adapterproto.Success(request.ID, adapterproto.ResolveWorkflowResult{State: "configuration_required", WorkflowID: params.WorkflowID, Challenge: challenge})
		case adapterproto.MethodResolve:
			if mode == "countresolve" || mode == "required_config" || mode == "required_inherited" {
				marker := os.Getenv("IR_ADAPTER_MARKER")
				file, openErr := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				if openErr != nil {
					return 6
				}
				_, _ = file.WriteString("x")
				_ = file.Close()
			}
			if mode == "crashresolve" {
				return 4
			}
			if mode == "badresolve" {
				_, _ = io.WriteString(os.Stdout, "not-json\n")
				return 4
			}
			if mode == "mismatchresolve" {
				response, _ = adapterproto.Success("wrong-id", map[string]any{})
				_ = adapterproto.WriteResponse(os.Stdout, response)
				return 4
			}
			if mode == "hangresolve" {
				time.Sleep(30 * time.Second)
				return 5
			}
			if mode == "structured_error" {
				const sentinel = "secret-sentinel"
				response = adapterproto.Failure(request.ID, sentinel, sentinel, map[string]string{"echo": sentinel})
			} else {
				var params adapterproto.ResolveParams
				_ = json.Unmarshal(request.Params, &params)
				if mode == "required_config" || mode == "required_inherited" {
					if len(params.Configuration["required_setting"]) == 0 {
						response = adapterproto.Failure(request.ID, "missing_configuration", "required configuration was not forwarded", nil)
						break
					}
				}
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

func TestInputSchemaRejectsBeforeAdapterResolve(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "resolve-called")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nIR_ADAPTER_HELPER=1 IR_ADAPTER_MODE=countresolve IR_ADAPTER_ID=counted IR_ADAPTER_MARKER=" + shellQuote(marker) + " exec " + shellQuote(binary) + "\n"
	if err = os.WriteFile(filepath.Join(dir, binaryPrefix+"counted"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	host, err := Discover(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if _, err = host.Resolve(context.Background(), "counted", json.RawMessage(`{"manifest_url":"https://example.test/live.m3u8","unknown":"private-value"}`), nil); err == nil {
		t.Fatal("unknown input field was accepted")
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("adapter resolve was called for invalid input: stat error=%v", err)
	}
	if _, err = host.Resolve(context.Background(), "counted", json.RawMessage(`{"manifest_url":"https://example.test/live.m3u8"}`), nil); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "x" {
		t.Fatalf("adapter resolve calls=%q, err=%v", data, err)
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

func TestAdapterLazilyRestartsAfterUnusableCalls(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   string
		cancel bool
	}{{"crashresolve", "crashresolve", false}, {"badresolve", "badresolve", false}, {"mismatchresolve", "mismatchresolve", false}, {"timeout", "hangresolve", false}, {"cancel-after-write", "hangresolve", true}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRestartingAdapter(t, dir, "restart", tc.mode, "restart", "normal")
			host, err := Discover(context.Background(), dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer host.Close()
			ctx := context.Background()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				time.AfterFunc(100*time.Millisecond, cancel)
			} else if tc.name == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			_, firstErr := host.Resolve(ctx, "restart", json.RawMessage(`{"manifest_url":"https://example.test/live.m3u8"}`), nil)
			if firstErr == nil {
				t.Fatal("first unusable operation unexpectedly succeeded")
			}
			time.Sleep(120 * time.Millisecond)
			media, err := host.Resolve(context.Background(), "restart", json.RawMessage(`{"manifest_url":"https://example.test/live.m3u8"}`), nil)
			if err != nil || media.ManifestURL != "https://example.test/live.m3u8" {
				t.Fatalf("operation after restart = %#v, %v", media, err)
			}
			adapter, _ := host.Get("restart")
			if adapter.Status.Generation < 2 || adapter.Status.State != "ready" {
				t.Fatalf("restart status = %#v", adapter.Status)
			}
		})
	}
}

func TestCanceledBeforeRequestWriteKeepsAdapterUsable(t *testing.T) {
	dir := t.TempDir()
	writeAdapter(t, dir, "integrated-recorder-adapter-cancel-safe", "normal", "cancel-safe", true)
	host, err := Discover(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = host.Resolve(ctx, "cancel-safe", json.RawMessage(`{"manifest_url":"https://example.test/live.m3u8"}`), nil); err == nil {
		t.Fatal("canceled operation succeeded")
	}
	media, err := host.Resolve(context.Background(), "cancel-safe", json.RawMessage(`{"manifest_url":"https://example.test/live.m3u8"}`), nil)
	if err != nil || media.ManifestURL == "" {
		t.Fatalf("healthy process was lost after pre-cancel: %#v %v", media, err)
	}
}

func TestRestartRejectsChangedDescriptorAndBacksOff(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "spawned")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ ! -f " + shellQuote(marker) + " ]; then MODE=crashresolve; ID=stable; else MODE=normal; ID=changed; fi\nprintf x >> " + shellQuote(marker) + "\nIR_ADAPTER_HELPER=1 IR_ADAPTER_MODE=$MODE IR_ADAPTER_ID=$ID exec " + shellQuote(binary) + "\n"
	if err = os.WriteFile(filepath.Join(dir, "integrated-recorder-adapter-stable"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	host, err := Discover(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	input := json.RawMessage(`{"manifest_url":"https://example.test/live.m3u8"}`)
	_, _ = host.Resolve(context.Background(), "stable", input, nil)
	time.Sleep(120 * time.Millisecond)
	if _, err = host.Resolve(context.Background(), "stable", input, nil); err == nil {
		t.Fatal("changed descriptor was accepted")
	}
	if _, err = host.Resolve(context.Background(), "stable", input, nil); err == nil {
		t.Fatal("rejected process was retried")
	}
	countData, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(countData) != "xx" {
		t.Fatalf("restart storm or missing replacement spawn; process count=%q", countData)
	}
	adapter, _ := host.Get("stable")
	if adapter.Status.State != "rejected" {
		t.Fatalf("rejected adapter state = %#v", adapter.Status)
	}
}

func TestRestartRejectsIncompatibleProtocolVersion(t *testing.T) {
	dir := t.TempDir()
	writeRestartingAdapter(t, dir, "stable", "crashresolve", "stable", "incompatible")
	host, err := Discover(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	input := json.RawMessage(`{"manifest_url":"https://example.test/live.m3u8"}`)
	if _, err = host.Resolve(context.Background(), "stable", input, nil); err == nil {
		t.Fatal("crashing first process unexpectedly resolved")
	}
	time.Sleep(120 * time.Millisecond)
	if _, err = host.Resolve(context.Background(), "stable", input, nil); err == nil {
		t.Fatal("incompatible replacement protocol was accepted")
	}
	adapter, _ := host.Get("stable")
	if adapter.Status.State != "rejected" {
		t.Fatalf("incompatible replacement status = %#v", adapter.Status)
	}
	if _, err = host.Resolve(context.Background(), "stable", input, nil); err == nil {
		t.Fatal("rejected adapter unexpectedly retried")
	}
}

func TestLegacyResolveRequiresEffectiveConfigurationButWorkflowCanChallenge(t *testing.T) {
	input := json.RawMessage(`{"manifest_url":"https://example.test/live.m3u8"}`)
	t.Run("missing required config blocks legacy IPC", func(t *testing.T) {
		dir := t.TempDir()
		marker := filepath.Join(dir, "resolve-called")
		writeAdapterWithMarker(t, dir, "required_config", "legacy", marker)
		host, err := Discover(context.Background(), dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer host.Close()
		if _, err = host.Resolve(context.Background(), "legacy", input, nil); err == nil || !strings.Contains(err.Error(), "required configuration key") {
			t.Fatalf("missing required config error = %v", err)
		}
		if _, err = os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("legacy resolve IPC ran without required config: %v", err)
		}
	})
	t.Run("required config inherited from parent passes", func(t *testing.T) {
		root := t.TempDir()
		configStore, secretStore, err := pluginconfig.NewTypedFileStores(root)
		if err != nil {
			t.Fatal(err)
		}
		configs, err := pluginconfig.NewService(configStore, secretStore)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		marker := filepath.Join(dir, "resolve-called")
		writeAdapterWithMarker(t, dir, "required_inherited", "legacy", marker)
		host, err := Discover(context.Background(), dir, configs)
		if err != nil {
			t.Fatal(err)
		}
		defer host.Close()
		descriptor, err := host.Descriptor("legacy")
		if err != nil {
			t.Fatal(err)
		}
		parent := &adapterproto.ResourceRef{Type: "alpha", ID: "parent"}
		child := &adapterproto.ResourceRef{Type: "beta", ID: "child", Parent: parent}
		if err = configs.Put(pluginconfig.Scope{PluginID: "legacy", Resource: parent}, descriptor.ResourceTypes[0].ConfigurationSchema, map[string]json.RawMessage{"required_setting": json.RawMessage(`"inherited"`)}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err = host.Resolve(context.Background(), "legacy", input, child); err != nil {
			t.Fatalf("inherited required config rejected: %v", err)
		}
		if data, err := os.ReadFile(marker); err != nil || string(data) != "x" {
			t.Fatalf("legacy resolve was not called with inherited config: %q, %v", data, err)
		}
	})
	t.Run("workflow may issue a generic challenge", func(t *testing.T) {
		dir := t.TempDir()
		writeAdapter(t, dir, binaryPrefix+"workflow", "workflow_required", "workflow", true)
		host, err := Discover(context.Background(), dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer host.Close()
		progress, err := host.BeginResolution(context.Background(), "workflow", input, nil)
		if err != nil || progress.State != "configuration_required" || progress.Challenge == nil {
			t.Fatalf("workflow challenge = %#v, %v", progress, err)
		}
	})
}

func TestAdvanceWorkflowPreservesContinuationReservationAcrossChallenge(t *testing.T) {
	const workflowID = "opaque-workflow"
	host := &Host{
		interactions: interaction.NewTracker(),
		workflows: map[string]workflowSession{
			workflowID: {adapterID: "adapter", continuing: true, continuationID: 41},
		},
	}
	challenge := &adapterproto.WorkflowChallenge{Schema: adapterproto.Schema{Fields: []adapterproto.Field{{Key: "answer", Control: "text", Label: "Answer"}}}}
	progress, err := host.advanceWorkflow(context.Background(), adapterproto.Descriptor{ID: "adapter"}, adapterproto.ResolveWorkflowResult{State: "configuration_required", WorkflowID: workflowID, Challenge: challenge}, 1, nil)
	if err != nil || progress.Challenge == nil {
		t.Fatalf("advanced workflow = %#v, %v", progress, err)
	}
	session, exists := host.workflows[workflowID]
	if !exists || !session.continuing || session.continuationID != 41 {
		t.Fatalf("active continuation reservation was lost: %#v", session)
	}
}

func writeAdapterWithMarker(t *testing.T, dir, mode, id, marker string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nIR_ADAPTER_HELPER=1 IR_ADAPTER_MODE=" + shellQuote(mode) + " IR_ADAPTER_ID=" + shellQuote(id) + " IR_ADAPTER_MARKER=" + shellQuote(marker) + " exec " + shellQuote(binary) + "\n"
	if err = os.WriteFile(filepath.Join(dir, binaryPrefix+id), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
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

func writeRestartingAdapter(t *testing.T, dir, id, firstMode, laterID, laterMode string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "started")
	script := "#!/bin/sh\nif [ ! -f " + shellQuote(marker) + " ]; then : > " + shellQuote(marker) + "; IR_ADAPTER_HELPER=1 IR_ADAPTER_MODE=" + shellQuote(firstMode) + " IR_ADAPTER_ID=" + shellQuote(id) + " exec " + shellQuote(binary) + "; fi\nIR_ADAPTER_HELPER=1 IR_ADAPTER_MODE=" + shellQuote(laterMode) + " IR_ADAPTER_ID=" + shellQuote(laterID) + " exec " + shellQuote(binary) + "\n"
	if err = os.WriteFile(filepath.Join(dir, binaryPrefix+id), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
