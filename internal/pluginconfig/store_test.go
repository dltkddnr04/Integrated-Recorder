package pluginconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
)

func TestConfigAndSecretStorageAreSeparateMaskedAndResourceScoped(t *testing.T) {
	root := t.TempDir()
	configs, secrets, err := NewTypedFileStores(root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(configs, secrets)
	if err != nil {
		t.Fatal(err)
	}
	schema := adapterproto.Schema{Fields: []adapterproto.Field{{Key: "mode", Control: "select", Label: "Mode", Required: true, Options: []adapterproto.Option{{Value: "fast", Label: "Fast"}}}, {Key: "opaque_value", Control: "text", Label: "Value"}, {Key: "opaque_secret", Control: "secret", Label: "Secret"}}}
	resource := &adapterproto.ResourceRef{Type: "arbitrary/type", ID: "id/with/slashes", Parent: &adapterproto.ResourceRef{Type: "parent.kind", ID: "parent id"}}
	scope := Scope{PluginID: "plugin.example", Resource: resource}
	values := map[string]json.RawMessage{"mode": json.RawMessage(`"fast"`), "opaque_value": json.RawMessage(`"visible"`)}
	if err = service.Put(scope, schema, values, map[string]string{"opaque_secret": "sensitive-value"}); err != nil {
		t.Fatal(err)
	}
	masked, configured, err := service.Masked(scope)
	if err != nil {
		t.Fatal(err)
	}
	if configured["opaque_secret"] != true || configured["unknown"] {
		t.Fatalf("masked secret flags = %#v", configured)
	}
	if string(masked["mode"]) != `"fast"` {
		t.Fatalf("ordinary config not round-tripped: %s", masked["mode"])
	}
	encoded, err := json.Marshal(masked)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sensitive-value") {
		t.Fatal("masked projection exposed secret plaintext")
	}
	other := Scope{PluginID: scope.PluginID, Resource: &adapterproto.ResourceRef{Type: "arbitrary/type", ID: "another"}}
	otherDoc, err := service.Get(other)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherDoc.Values) != 0 || len(otherDoc.Secrets) != 0 {
		t.Fatalf("scoped values crossed resource boundary: %#v", otherDoc)
	}
	configFiles, err := os.ReadDir(filepath.Join(root, "plugin-config"))
	if err != nil {
		t.Fatal(err)
	}
	secretFiles, err := os.ReadDir(filepath.Join(root, "plugin-secrets"))
	if err != nil {
		t.Fatal(err)
	}
	if len(configFiles) != 1 || len(secretFiles) != 1 {
		t.Fatalf("config/secret files = %d/%d", len(configFiles), len(secretFiles))
	}
	configBytes, err := os.ReadFile(filepath.Join(root, "plugin-config", configFiles[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	secretBytes, err := os.ReadFile(filepath.Join(root, "plugin-secrets", secretFiles[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configBytes), "sensitive-value") || !strings.Contains(string(secretBytes), "sensitive-value") {
		t.Fatal("secret was not kept in its separate store")
	}
	if info, err := os.Stat(filepath.Join(root, "plugin-secrets", secretFiles[0].Name())); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("secret file permissions = %o", info.Mode().Perm())
	}
}

func TestPutValidatesPluginDefinedSchemaAndOpaqueKeys(t *testing.T) {
	configs, secrets, err := NewTypedFileStores(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(configs, secrets)
	if err != nil {
		t.Fatal(err)
	}
	schema := adapterproto.Schema{Fields: []adapterproto.Field{{Key: "count", Control: "number", Label: "Count", Required: true, Constraints: &adapterproto.Constraints{Min: floatPtr(1), Max: floatPtr(4)}}, {Key: "password-ish", Control: "secret", Label: "Opaque secret"}}}
	scope := Scope{PluginID: "plugin", Resource: &adapterproto.ResourceRef{Type: "opaque.kind", ID: "123"}}
	if err = service.Put(scope, schema, map[string]json.RawMessage{"count": json.RawMessage(`5`)}, nil); err == nil {
		t.Fatal("out of range number accepted")
	}
	if err = service.Put(scope, schema, map[string]json.RawMessage{"count": json.RawMessage(`3`)}, map[string]string{"not-declared": "value"}); err == nil {
		t.Fatal("unknown plugin field accepted")
	}
	if err = service.Put(scope, schema, map[string]json.RawMessage{"count": json.RawMessage(`3`)}, map[string]string{"password-ish": "secret"}); err != nil {
		t.Fatal(err)
	}
}

func floatPtr(v float64) *float64 { return &v }
