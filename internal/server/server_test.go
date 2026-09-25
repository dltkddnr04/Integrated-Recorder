package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterhost"
	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
	"github.com/dltkddnr04/integrated-recorder/internal/pluginconfig"
)

func TestMain(m *testing.M) {
	if os.Getenv("IR_SERVER_ADAPTER_HELPER") == "1" {
		os.Exit(runServerTestAdapter())
	}
	os.Exit(m.Run())
}

func runServerTestAdapter() int {
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

func assertMaskedLegacyConfig(t *testing.T, response *httptest.ResponseRecorder, sentinel, value string) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("config response status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, sentinel) || !strings.Contains(body, value) || !strings.Contains(body, `"legacy_secret":{"configured":true}`) || strings.Contains(body, `"legacy_secret":"`) {
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
