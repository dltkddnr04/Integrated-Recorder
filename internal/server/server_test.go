package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
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
	"github.com/dltkddnr04/integrated-recorder/internal/storage"
)

func TestMain(m *testing.M) {
	if os.Getenv("IR_SERVER_ADAPTER_HELPER") == "1" {
		os.Exit(runServerTestAdapter())
	}
	os.Exit(m.Run())
}

func runServerTestAdapter() int {
	if os.Getenv("IR_SERVER_ADAPTER_MODE") == "workflow" {
		return runWorkflowTestAdapter()
	}
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
			descriptor := adapterproto.Descriptor{
				ID: "schema-test", Name: "Schema Test", Version: "1", ProtocolVersion: adapterproto.Version,
				InputSchema:         adapterproto.Schema{Fields: []adapterproto.Field{}},
				ConfigurationSchema: adapterproto.Schema{Fields: []adapterproto.Field{{Key: "legacy_secret", Control: "secret", Label: "Secret"}, {Key: "label", Control: "text", Label: "Label"}}},
				MediaTypes:          []string{},
			}
			response, _ = adapterproto.Success(request.ID, descriptor)
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

func runWorkflowTestAdapter() int {
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
			descriptor := adapterproto.Descriptor{ID: "workflow-test", Name: "Workflow Test", Version: "1", ProtocolVersion: adapterproto.Version, Capabilities: []string{adapterproto.CapabilityResolveWorkflow}, InputSchema: adapterproto.Schema{Fields: []adapterproto.Field{{Key: "opaque_input", Control: "text", Label: "Input", Required: true}}}, ConfigurationSchema: adapterproto.Schema{Fields: []adapterproto.Field{}}, ResourceTypes: []adapterproto.ResourceType{{Type: "alpha", ConfigurationSchema: adapterproto.Schema{Fields: []adapterproto.Field{{Key: "setting", Control: "text", Label: "Setting", Inherit: true}, {Key: "credential", Control: "secret", Label: "Credential"}}}}}, MediaTypes: []string{"hls"}}
			response, _ = adapterproto.Success(request.ID, descriptor)
		case adapterproto.MethodResolveBegin:
			var params adapterproto.ResolveBeginParams
			_ = json.Unmarshal(request.Params, &params)
			var input struct {
				ID string `json:"opaque_input"`
			}
			_ = json.Unmarshal(params.Input, &input)
			response, _ = adapterproto.Success(request.ID, adapterproto.ResolveWorkflowResult{State: "resource_discovered", WorkflowID: params.WorkflowID, Resource: &adapterproto.ResourceRef{Type: "alpha", ID: input.ID}})
		case adapterproto.MethodResolveContinue:
			var params adapterproto.ResolveContinueParams
			_ = json.Unmarshal(request.Params, &params)
			if len(params.Answers) == 0 && len(params.AnswerSecrets) == 0 {
				challenge := &adapterproto.WorkflowChallenge{Persistable: true, Schema: adapterproto.Schema{Fields: []adapterproto.Field{{Key: "setting", Control: "text", Label: "Setting", Required: true, Inherit: true}, {Key: "credential", Control: "secret", Label: "Credential", Required: true}}}}
				response, _ = adapterproto.Success(request.ID, adapterproto.ResolveWorkflowResult{State: "configuration_required", WorkflowID: params.WorkflowID, Resource: params.Resource, Challenge: challenge})
			} else {
				if marker := os.Getenv("IR_WORKFLOW_RESUME_MARKER"); marker != "" {
					file, openErr := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
					if openErr != nil {
						return 4
					}
					_, _ = file.WriteString("x")
					_ = file.Close()
					for {
						if _, statErr := os.Stat(os.Getenv("IR_WORKFLOW_RESUME_RELEASE")); statErr == nil {
							break
						}
						time.Sleep(5 * time.Millisecond)
					}
				}
				answerSecret := params.AnswerSecrets["credential"]
				if answerSecret == "" {
					answerSecret = params.Secrets["credential"]
				}
				if answerSecret == "" {
					response = adapterproto.Failure(request.ID, "missing_answer", "required answer was not forwarded", nil)
					break
				}
				response, _ = adapterproto.Success(request.ID, adapterproto.ResolveWorkflowResult{State: "resolved", WorkflowID: params.WorkflowID, Resource: params.Resource, Media: &adapterproto.MediaSource{Type: "hls", ManifestURL: "http://127.0.0.1:9/live.m3u8?token=credential-url-sentinel", ArchivePolicy: &adapterproto.ArchivePolicy{SourceURI: "sensitive"}}})
			}
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

func TestConfigAPIAlwaysMasksValuesDeclaredSecretByCurrentSchema(t *testing.T) {
	const sentinel = "legacy-secret-sentinel"
	configStore := &preservingConfigStore{values: map[string]map[string]json.RawMessage{
		"schema-test": {
			"legacy_secret": json.RawMessage(`"` + sentinel + `"`),
			"label":         json.RawMessage(`"old-value"`),
		},
	}}
	configs, err := pluginconfig.NewService(configStore, &memorySecretStore{values: map[string]map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	adapterDir := t.TempDir()
	writeServerTestAdapter(t, adapterDir)
	host, err := adapterhost.Discover(context.Background(), adapterDir, configs)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	handler := New(nil, host, configs)

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/adapters/schema-test/config", nil))
	assertMaskedLegacyConfig(t, get, sentinel, "old-value")

	put := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/adapters/schema-test/config", strings.NewReader(`{"values":{"label":"new-value"}}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(put, request)
	assertMaskedLegacyConfig(t, put, sentinel, "new-value")
}

func TestWorkflowAPIResourceChallengePersistenceAndEphemeralAnswers(t *testing.T) {
	configFiles, secretFiles, err := pluginconfig.NewTypedFileStores(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configs, err := pluginconfig.NewService(configFiles, secretFiles)
	if err != nil {
		t.Fatal(err)
	}
	adapterDir := t.TempDir()
	writeWorkflowServerTestAdapter(t, adapterDir)
	host, err := adapterhost.Discover(context.Background(), adapterDir, configs)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := acquire.NewManager(store, nil, nil, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	handler := New(manager, host, configs)
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodPost, "/api/recordings", strings.NewReader(`{"adapter_id":"workflow-test","input":{}}`)))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid input accepted: %d %s", invalid.Code, invalid.Body.String())
	}
	workflowID, resource := startWorkflowChallenge(t, handler, "persisted-resource")
	getWorkflow := httptest.NewRecorder()
	handler.ServeHTTP(getWorkflow, httptest.NewRequest(http.MethodGet, "/api/resolve-workflows/"+workflowID, nil))
	if getWorkflow.Code != http.StatusOK || !strings.Contains(getWorkflow.Body.String(), `"resource_type":"alpha"`) || strings.Contains(getWorkflow.Body.String(), "private-secret-value") {
		t.Fatalf("workflow projection=%d %s", getWorkflow.Code, getWorkflow.Body.String())
	}
	secret := "private-secret-value"
	continueBody := `{"values":{"setting":"saved"},"secrets":{"credential":"` + secret + `"},"persist":true}`
	continued := httptest.NewRecorder()
	handler.ServeHTTP(continued, httptest.NewRequest(http.MethodPost, "/api/resolve-workflows/"+workflowID+"/continue", strings.NewReader(continueBody)))
	if continued.Code != http.StatusCreated || strings.Contains(continued.Body.String(), secret) || !strings.Contains(continued.Body.String(), `"protocol_version":1`) {
		t.Fatalf("persistent continuation=%d %s", continued.Code, continued.Body.String())
	}
	recordingID := extractJSONID(t, continued.Body.Bytes())
	encodedResource := encodeResource(t, resource)
	configURL := "/api/adapters/workflow-test/config?resource=" + encodedResource
	configGet := httptest.NewRecorder()
	handler.ServeHTTP(configGet, httptest.NewRequest(http.MethodGet, configURL, nil))
	if configGet.Code != http.StatusOK || strings.Contains(configGet.Body.String(), secret) || !strings.Contains(configGet.Body.String(), `"credential":{"configured":true}`) || !strings.Contains(configGet.Body.String(), `"setting":"saved"`) {
		t.Fatalf("masked resource config=%d %s", configGet.Code, configGet.Body.String())
	}
	blank := httptest.NewRecorder()
	handler.ServeHTTP(blank, httptest.NewRequest(http.MethodPut, "/api/adapters/workflow-test/config", strings.NewReader(`{"resource":{"resource_type":"alpha","resource_id":"persisted-resource"},"secrets":{"credential":""}}`)))
	if blank.Code != http.StatusOK || !strings.Contains(blank.Body.String(), `"credential":{"configured":true}`) || strings.Contains(blank.Body.String(), secret) {
		t.Fatalf("blank secret update=%d %s", blank.Code, blank.Body.String())
	}
	clear := httptest.NewRecorder()
	handler.ServeHTTP(clear, httptest.NewRequest(http.MethodPut, "/api/adapters/workflow-test/config", strings.NewReader(`{"resource":{"resource_type":"alpha","resource_id":"persisted-resource"},"clear_secrets":["credential"]}`)))
	if clear.Code != http.StatusOK || !strings.Contains(clear.Body.String(), `"credential":{"configured":false}`) {
		t.Fatalf("explicit clear=%d %s", clear.Code, clear.Body.String())
	}
	getRecording := httptest.NewRecorder()
	handler.ServeHTTP(getRecording, httptest.NewRequest(http.MethodGet, "/api/recordings/"+recordingID, nil))
	if strings.Contains(getRecording.Body.String(), "127.0.0.1:9") || strings.Contains(getRecording.Body.String(), "credential-url-sentinel") || !strings.Contains(getRecording.Body.String(), `"source_uri_classification":"sensitive"`) {
		t.Fatalf("recording API URI policy=%s", getRecording.Body.String())
	}
	storedRecording, err := manager.Get(recordingID)
	if err != nil {
		t.Fatal(err)
	}
	if storedRecording.Tracks["main"].SourcePlaylistURL != "http://127.0.0.1:9/live.m3u8?token=credential-url-sentinel" {
		t.Fatalf("runtime fetch URI was rewritten: %q", storedRecording.Tracks["main"].SourcePlaylistURL)
	}
	loaded, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	var reloaded *domain.Recording
	for _, item := range loaded {
		if item.ID == recordingID {
			reloaded = item
		}
	}
	if reloaded == nil || reloaded.Adapter == nil || reloaded.Adapter.ID != "workflow-test" || reloaded.SourceURIClassification != "sensitive" {
		t.Fatalf("recording provenance reload=%#v", reloaded)
	}
	if !strings.Contains(getRecording.Body.String(), `"adapter":{"id":"workflow-test","version":"1","protocol_version":1`) {
		t.Fatalf("recording provenance missing: %s", getRecording.Body.String())
	}
	ephemeralID, _ := startWorkflowChallenge(t, handler, "ephemeral-resource")
	ephemeralSecret := "ephemeral-secret-value"
	eph := httptest.NewRecorder()
	handler.ServeHTTP(eph, httptest.NewRequest(http.MethodPost, "/api/resolve-workflows/"+ephemeralID+"/continue", strings.NewReader(`{"values":{"setting":"temporary"},"secrets":{"credential":"`+ephemeralSecret+`"},"persist":false}`)))
	if eph.Code != http.StatusCreated || strings.Contains(eph.Body.String(), ephemeralSecret) {
		t.Fatalf("ephemeral continuation=%d %s", eph.Code, eph.Body.String())
	}
	ephResource := &adapterproto.ResourceRef{Type: "alpha", ID: "ephemeral-resource"}
	ephemeralConfig := httptest.NewRecorder()
	handler.ServeHTTP(ephemeralConfig, httptest.NewRequest(http.MethodGet, "/api/adapters/workflow-test/config?resource="+encodeResource(t, ephResource), nil))
	if strings.Contains(ephemeralConfig.Body.String(), `"setting":"temporary"`) || !strings.Contains(ephemeralConfig.Body.String(), `"credential":{"configured":false}`) {
		t.Fatalf("ephemeral values persisted: %s", ephemeralConfig.Body.String())
	}
}

func TestWorkflowContinuationCannotBeSubmittedTwice(t *testing.T) {
	root := t.TempDir()
	configFiles, secretFiles, err := pluginconfig.NewTypedFileStores(root)
	if err != nil {
		t.Fatal(err)
	}
	configs, err := pluginconfig.NewService(configFiles, secretFiles)
	if err != nil {
		t.Fatal(err)
	}
	adapterDir := t.TempDir()
	resumeMarker := filepath.Join(root, "resume-called")
	releaseResume := filepath.Join(root, "release-resume")
	writeBlockingWorkflowTestAdapter(t, adapterDir, resumeMarker, releaseResume)
	host, err := adapterhost.Discover(context.Background(), adapterDir, configs)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := acquire.NewManager(store, nil, nil, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	handler := New(manager, host, configs)
	workflowID, _ := startWorkflowChallenge(t, handler, "single-continuation")
	body := `{"values":{"setting":"temporary"},"secrets":{"credential":"one-time-secret"},"persist":false}`
	firstResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/resolve-workflows/"+workflowID+"/continue", strings.NewReader(body)))
		firstResult <- response
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(resumeMarker); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err = os.Stat(resumeMarker); err != nil {
		t.Fatal("first continuation did not enter adapter")
	}
	second := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/api/resolve-workflows/"+workflowID+"/continue", strings.NewReader(body)))
		close(done)
	}()
	select {
	case <-done:
		if second.Code != http.StatusBadRequest || !strings.Contains(second.Body.String(), "already continuing") {
			t.Fatalf("duplicate continuation response=%d %s", second.Code, second.Body.String())
		}
	case <-time.After(500 * time.Millisecond):
		_ = os.WriteFile(releaseResume, nil, 0600)
		t.Fatal("duplicate continuation waited for the first adapter call instead of being rejected")
	}
	if err = os.WriteFile(releaseResume, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var first *httptest.ResponseRecorder
	select {
	case first = <-firstResult:
	case <-time.After(3 * time.Second):
		t.Fatal("first continuation did not finish")
	}
	if first.Code != http.StatusCreated {
		t.Fatalf("first continuation response=%d %s", first.Code, first.Body.String())
	}
	if data, readErr := os.ReadFile(resumeMarker); readErr != nil || string(data) != "x" {
		t.Fatalf("adapter continuation count=%q, err=%v", data, readErr)
	}
	recordings := manager.List()
	if len(recordings) != 1 {
		t.Fatalf("duplicate workflow created %d recordings", len(recordings))
	}
	if _, err = manager.Stop(recordings[0].ID); err != nil {
		t.Fatal(err)
	}
}

func startWorkflowChallenge(t *testing.T, handler http.Handler, input string) (string, *adapterproto.ResourceRef) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/recordings", strings.NewReader(`{"adapter_id":"workflow-test","input":{"opaque_input":"`+input+`"}}`)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("workflow begin=%d %s", response.Code, response.Body.String())
	}
	var progress adapterhost.WorkflowProgress
	if err := json.Unmarshal(response.Body.Bytes(), &progress); err != nil {
		t.Fatal(err)
	}
	if progress.State != "configuration_required" || progress.WorkflowID == "" || progress.Resource == nil || progress.Challenge == nil {
		t.Fatalf("progress=%#v", progress)
	}
	return progress.WorkflowID, progress.Resource
}

func encodeResource(t *testing.T, resource *adapterproto.ResourceRef) string {
	t.Helper()
	data, err := json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(data)
}
func extractJSONID(t *testing.T, data []byte) string {
	t.Helper()
	var value struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if value.ID == "" {
		t.Fatalf("missing id in %s", data)
	}
	return value.ID
}

func writeWorkflowServerTestAdapter(t *testing.T, dir string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nIR_SERVER_ADAPTER_HELPER=1 IR_SERVER_ADAPTER_MODE=workflow exec '" + strings.ReplaceAll(binary, "'", "'\\''") + "'\n"
	if err = os.WriteFile(filepath.Join(dir, "integrated-recorder-adapter-workflow-test"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func writeBlockingWorkflowTestAdapter(t *testing.T, dir, resumeMarker, releaseResume string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nIR_SERVER_ADAPTER_HELPER=1 IR_SERVER_ADAPTER_MODE=workflow IR_WORKFLOW_RESUME_MARKER='" + strings.ReplaceAll(resumeMarker, "'", "'\\''") + "' IR_WORKFLOW_RESUME_RELEASE='" + strings.ReplaceAll(releaseResume, "'", "'\\''") + "' exec '" + strings.ReplaceAll(binary, "'", "'\\''") + "'\n"
	if err = os.WriteFile(filepath.Join(dir, "integrated-recorder-adapter-workflow-test"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func assertMaskedLegacyConfig(t *testing.T, response *httptest.ResponseRecorder, sentinel, value string) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("config response status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, sentinel) || !strings.Contains(body, value) || !strings.Contains(body, `"legacy_secret":{"configured":true}`) || strings.Contains(body, `"legacy_secret":"`+sentinel) {
		t.Fatalf("config response did not safely mask legacy secret: %s", body)
	}
}

func writeServerTestAdapter(t *testing.T, dir string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nIR_SERVER_ADAPTER_HELPER=1 exec '" + strings.ReplaceAll(binary, "'", "'\\''") + "'\n"
	if err = os.WriteFile(filepath.Join(dir, "integrated-recorder-adapter-schema-test"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

// This test store retains unmentioned keys to model ordinary config data left
// behind after a field is reclassified as secret by a newer schema.
type preservingConfigStore struct {
	mu     sync.Mutex
	values map[string]map[string]json.RawMessage
}

func (s *preservingConfigStore) Load(scope pluginconfig.Scope) (map[string]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneRawValues(s.values[scope.PluginID]), nil
}

func (s *preservingConfigStore) Save(scope pluginconfig.Scope, values map[string]json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneRawValues(values)
	for key, value := range s.values[scope.PluginID] {
		if _, exists := next[key]; !exists {
			next[key] = append(json.RawMessage(nil), value...)
		}
	}
	s.values[scope.PluginID] = next
	return nil
}

func cloneRawValues(values map[string]json.RawMessage) map[string]json.RawMessage {
	copy := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		copy[key] = append(json.RawMessage(nil), value...)
	}
	return copy
}

type memorySecretStore struct {
	mu     sync.Mutex
	values map[string]map[string]string
}

func (s *memorySecretStore) Load(scope pluginconfig.Scope) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := map[string]string{}
	for key, value := range s.values[scope.PluginID] {
		copy[key] = value
	}
	return copy, nil
}

func (s *memorySecretStore) Save(scope pluginconfig.Scope, values map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := map[string]string{}
	for key, value := range values {
		copy[key] = value
	}
	s.values[scope.PluginID] = copy
	return nil
}
