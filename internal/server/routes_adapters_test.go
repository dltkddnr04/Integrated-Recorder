package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterhost"
	"github.com/dltkddnr04/integrated-recorder/internal/management"
)

func TestAdapterControlRoutesPersistAndReturnRuntimeState(t *testing.T) {
	dir := t.TempDir()
	writeServerTestAdapter(t, dir)
	host, err := adapterhost.Discover(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()

	productRoot := t.TempDir()
	products, err := management.Open(productRoot)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{adapters: host, products: products, mux: http.NewServeMux()}
	s.registerAdapterControlRoutes()
	handler := s.mux

	disabled := httptest.NewRecorder()
	handler.ServeHTTP(disabled, httptest.NewRequest(http.MethodPost, "/api/adapters/schema-test/disable", strings.NewReader("")))
	if disabled.Code != http.StatusOK || disabled.Header().Get("X-Adapter-Audit") != "recorded" || !strings.Contains(disabled.Body.String(), `"state":"disabled"`) || products.AdapterEnabled("schema-test") {
		t.Fatalf("disable response=%d %s, stored_enabled=%v", disabled.Code, disabled.Body.String(), products.AdapterEnabled("schema-test"))
	}

	enabled := httptest.NewRecorder()
	handler.ServeHTTP(enabled, httptest.NewRequest(http.MethodPost, "/api/adapters/schema-test/enable", strings.NewReader("")))
	if enabled.Code != http.StatusOK || enabled.Header().Get("X-Adapter-Audit") != "recorded" || !strings.Contains(enabled.Body.String(), `"state":"ready"`) || !products.AdapterEnabled("schema-test") {
		t.Fatalf("enable response=%d %s, stored_enabled=%v", enabled.Code, enabled.Body.String(), products.AdapterEnabled("schema-test"))
	}

	restarted := httptest.NewRecorder()
	handler.ServeHTTP(restarted, httptest.NewRequest(http.MethodPost, "/api/adapters/schema-test/restart", strings.NewReader("")))
	if restarted.Code != http.StatusOK || restarted.Header().Get("X-Adapter-Audit") != "recorded" || !strings.Contains(restarted.Body.String(), `"state":"ready"`) || !strings.Contains(restarted.Body.String(), `"generation":3`) {
		t.Fatalf("restart response=%d %s", restarted.Code, restarted.Body.String())
	}

	audit := products.Audit(10)
	if len(audit) != 3 || audit[0].Type != "adapter_restarted" || audit[1].Type != "adapter_enabled" || audit[2].Type != "adapter_disabled" {
		t.Fatalf("adapter audit=%#v", audit)
	}
}

func TestAdapterControlReturnsAppliedStateWhenAuditWriteFails(t *testing.T) {
	dir := t.TempDir()
	writeServerTestAdapter(t, dir)
	host, err := adapterhost.Discover(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()

	productRoot := t.TempDir()
	products, err := management.Open(productRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(productRoot, "management", "audit.json"), 0700); err != nil {
		t.Fatal(err)
	}
	s := &Server{adapters: host, products: products, mux: http.NewServeMux()}
	s.registerAdapterControlRoutes()
	response := httptest.NewRecorder()
	s.mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/adapters/schema-test/disable", strings.NewReader("")))
	if response.Code != http.StatusOK || response.Header().Get("X-Adapter-Audit") != "failed" || !strings.Contains(response.Body.String(), `"state":"disabled"`) {
		t.Fatalf("applied state response=%d audit=%q body=%s", response.Code, response.Header().Get("X-Adapter-Audit"), response.Body.String())
	}
	if products.AdapterEnabled("schema-test") {
		t.Fatal("durable adapter preference did not reflect applied runtime state")
	}
}

func TestAdapterControlRoutesRejectUnknownAdapter(t *testing.T) {
	products, err := management.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{products: products, mux: http.NewServeMux()}
	s.registerAdapterControlRoutes()
	response := httptest.NewRecorder()
	s.mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/adapters/unknown/disable", strings.NewReader("")))
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown adapter status=%d body=%s", response.Code, response.Body.String())
	}
}
