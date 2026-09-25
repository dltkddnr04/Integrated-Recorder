// Package pluginconfig stores opaque plugin configuration and secret values
// independently. Scope identifiers are hashed before they become paths.
package pluginconfig

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
)

type Scope struct {
	PluginID string                    `json:"plugin_id"`
	Resource *adapterproto.ResourceRef `json:"resource,omitempty"`
}

type Document struct {
	Values  map[string]json.RawMessage `json:"values"`
	Secrets map[string]string          `json:"-"`
}

type ScopeSchema struct {
	Scope  Scope
	Schema adapterproto.Schema
}

// EffectiveDocument keeps plaintext secrets internal to Core. API callers use
// Snapshot, which contains configured flags and opaque source provenance only.
type EffectiveDocument struct {
	Stored                    Document
	StoredSecretConfigured    map[string]bool
	EffectiveValues           map[string]json.RawMessage
	EffectiveSecrets          map[string]string
	EffectiveSecretConfigured map[string]bool
	ValueSources              map[string]string
	SecretSources             map[string]string
}

type Snapshot struct {
	StoredValues    map[string]json.RawMessage `json:"stored_values"`
	StoredSecrets   map[string]bool            `json:"stored_secrets"`
	EffectiveValues map[string]json.RawMessage `json:"effective_values"`
	EffectiveSecret map[string]bool            `json:"effective_secrets"`
	ValueSources    map[string]string          `json:"value_sources"`
	SecretSources   map[string]string          `json:"secret_sources"`
}

type ConfigStore interface {
	Load(Scope) (map[string]json.RawMessage, error)
	Save(Scope, map[string]json.RawMessage) error
}
type SecretStore interface {
	Load(Scope) (map[string]string, error)
	Save(Scope, map[string]string) error
}

type Service struct {
	configs ConfigStore
	secrets SecretStore
	mu      sync.Mutex
}

func NewService(configs ConfigStore, secrets SecretStore) (*Service, error) {
	if configs == nil || secrets == nil {
		return nil, fmt.Errorf("configuration and secret stores are required")
	}
	return &Service{configs: configs, secrets: secrets}, nil
}

func (s *Service) Get(scope Scope) (Document, error) {
	if err := validateScope(scope); err != nil {
		return Document{}, err
	}
	values, err := s.configs.Load(scope)
	if err != nil {
		return Document{}, err
	}
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	secrets, err := s.secrets.Load(scope)
	if err != nil {
		return Document{}, err
	}
	if secrets == nil {
		secrets = map[string]string{}
	}
	return Document{Values: values, Secrets: secrets}, nil
}

func (s *Service) Put(scope Scope, schema adapterproto.Schema, values map[string]json.RawMessage, secrets map[string]string) error {
	return s.PutPartial(scope, schema, values, secrets, nil)
}

// PutPartial applies a partial scope update. Blank secrets are unchanged;
// deletion requires an explicit clear key.
func (s *Service) PutPartial(scope Scope, schema adapterproto.Schema, values map[string]json.RawMessage, secrets map[string]string, clearSecrets []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateScope(scope); err != nil {
		return err
	}
	if err := schema.Validate(); err != nil {
		return err
	}
	ordinarySchema := adapterproto.Schema{}
	secretSchema := adapterproto.Schema{}
	for _, f := range schema.Fields {
		if f.Control == "secret" {
			secretSchema.Fields = append(secretSchema.Fields, f)
		} else {
			ordinarySchema.Fields = append(ordinarySchema.Fields, f)
		}
	}
	if err := adapterproto.ValidateProvidedValues(ordinarySchema, values); err != nil {
		return err
	}
	secretRaw := map[string]json.RawMessage{}
	for key, value := range secrets {
		b, _ := json.Marshal(value)
		secretRaw[key] = b
	}
	if err := adapterproto.ValidateProvidedValues(secretSchema, secretRaw); err != nil {
		return err
	}
	secretKeys := map[string]bool{}
	for _, field := range secretSchema.Fields {
		secretKeys[field.Key] = true
	}
	for _, key := range clearSecrets {
		if !secretKeys[key] {
			return fmt.Errorf("unknown secret key %q", key)
		}
	}
	current, err := s.Get(scope)
	if err != nil {
		return err
	}
	mergedValues := cloneRaw(current.Values)
	for key, value := range values {
		mergedValues[key] = append(json.RawMessage(nil), value...)
	}
	mergedSecrets := cloneSecrets(current.Secrets)
	for key, value := range secrets {
		if value != "" {
			mergedSecrets[key] = value
		}
	}
	for _, key := range clearSecrets {
		delete(mergedSecrets, key)
	}
	// Scope writes are partial. Required effective values may be inherited or
	// requested by a workflow challenge, so they are not required locally.
	if err = s.configs.Save(scope, mergedValues); err != nil {
		return err
	}
	return s.secrets.Save(scope, mergedSecrets)
}

func cloneRaw(values map[string]json.RawMessage) map[string]json.RawMessage {
	copy := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		copy[key] = append(json.RawMessage(nil), value...)
	}
	return copy
}
func cloneSecrets(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

// ResourceScopes returns plugin scope followed by parent-most resource scope
// through current. Each resource scope identity includes its full prefix.
func ResourceScopes(pluginID string, resource *adapterproto.ResourceRef) ([]Scope, error) {
	if err := validateScope(Scope{PluginID: pluginID, Resource: resource}); err != nil {
		return nil, err
	}
	scopes := []Scope{{PluginID: pluginID}}
	var chain []*adapterproto.ResourceRef
	for current := resource; current != nil; current = current.Parent {
		chain = append(chain, current)
	}
	for i := len(chain) - 1; i >= 0; i-- {
		scopes = append(scopes, Scope{PluginID: pluginID, Resource: cloneRef(chain[i])})
	}
	return scopes, nil
}

func cloneRef(ref *adapterproto.ResourceRef) *adapterproto.ResourceRef {
	if ref == nil {
		return nil
	}
	copy := *ref
	copy.Parent = cloneRef(ref.Parent)
	return &copy
}

// ScopeIdentity is an opaque, stable API label; resource strings remain
// uninterpreted and the serialized reference is URL-safe base64.
func ScopeIdentity(scope Scope) string {
	if scope.Resource == nil {
		return "plugin"
	}
	data, _ := json.Marshal(scope.Resource)
	return "resource:" + base64.RawURLEncoding.EncodeToString(data)
}

func (s *Service) Effective(scopes []ScopeSchema) (EffectiveDocument, error) {
	if len(scopes) == 0 {
		return EffectiveDocument{}, fmt.Errorf("configuration scope chain is empty")
	}
	target := scopes[len(scopes)-1].Scope
	stored, err := s.Get(target)
	if err != nil {
		return EffectiveDocument{}, err
	}
	effective := EffectiveDocument{Stored: stored, StoredSecretConfigured: map[string]bool{}, EffectiveValues: map[string]json.RawMessage{}, EffectiveSecrets: map[string]string{}, EffectiveSecretConfigured: map[string]bool{}, ValueSources: map[string]string{}, SecretSources: map[string]string{}}
	for index, item := range scopes {
		doc, loadErr := s.Get(item.Scope)
		if loadErr != nil {
			return EffectiveDocument{}, loadErr
		}
		fields := map[string]adapterproto.Field{}
		for _, field := range item.Schema.Fields {
			fields[field.Key] = field
		}
		for key, raw := range doc.Values {
			field, declared := fields[key]
			if !declared || index != len(scopes)-1 && !field.Inherit {
				continue
			}
			if declared && field.Control == "secret" {
				effective.EffectiveSecretConfigured[key] = true
				effective.SecretSources[key] = ScopeIdentity(item.Scope)
				if index == len(scopes)-1 {
					effective.StoredSecretConfigured[key] = true
				}
				continue
			}
			effective.EffectiveValues[key] = append(json.RawMessage(nil), raw...)
			effective.ValueSources[key] = ScopeIdentity(item.Scope)
		}
		for key, secret := range doc.Secrets {
			field, declared := fields[key]
			if !declared || field.Control != "secret" || index != len(scopes)-1 && !field.Inherit {
				continue
			}
			effective.EffectiveSecrets[key] = secret
			effective.EffectiveSecretConfigured[key] = secret != ""
			effective.SecretSources[key] = ScopeIdentity(item.Scope)
			if index == len(scopes)-1 {
				effective.StoredSecretConfigured[key] = effective.StoredSecretConfigured[key] || secret != ""
			}
		}
	}
	return effective, nil
}

func (d EffectiveDocument) Snapshot() Snapshot {
	storedSecrets := map[string]bool{}
	for key, value := range d.StoredSecretConfigured {
		storedSecrets[key] = value
	}
	effectiveSecrets := map[string]bool{}
	for key, value := range d.EffectiveSecretConfigured {
		effectiveSecrets[key] = value
	}
	return Snapshot{StoredValues: cloneRaw(d.Stored.Values), StoredSecrets: storedSecrets, EffectiveValues: cloneRaw(d.EffectiveValues), EffectiveSecret: effectiveSecrets, ValueSources: cloneSources(d.ValueSources), SecretSources: cloneSources(d.SecretSources)}
}
func cloneSources(input map[string]string) map[string]string {
	copy := make(map[string]string, len(input))
	for k, v := range input {
		copy[k] = v
	}
	return copy
}

func (s *Service) Masked(scope Scope) (map[string]json.RawMessage, map[string]bool, error) {
	doc, err := s.Get(scope)
	if err != nil {
		return nil, nil, err
	}
	configured := map[string]bool{}
	for key, value := range doc.Secrets {
		configured[key] = value != ""
	}
	return doc.Values, configured, nil
}

func validateScope(scope Scope) error {
	if strings.TrimSpace(scope.PluginID) == "" || strings.ContainsAny(scope.PluginID, "/\\") {
		return fmt.Errorf("invalid plugin id")
	}
	if err := adapterproto.ValidateResourceRef(scope.Resource); err != nil {
		return fmt.Errorf("invalid resource reference")
	}
	return nil
}

type fileStore struct {
	root string
}

func (f fileStore) path(scope Scope) (string, error) {
	if err := validateScope(scope); err != nil {
		return "", err
	}
	b, err := json.Marshal(scope)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return filepath.Join(f.root, hex.EncodeToString(h[:])+".json"), nil
}
func (f fileStore) loadBytes(scope Scope) ([]byte, error) {
	path, err := f.path(scope)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []byte("{}"), nil
	}
	return data, err
}
func (f fileStore) save(scope Scope, values any) error {
	path, err := f.path(scope)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	if len(data) == 0 || string(data) == "{}" {
		if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err = os.MkdirAll(f.root, 0700); err != nil {
		return err
	}
	if err = os.Chmod(f.root, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(f.root, ".config-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(append(data, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// SecretFileStore is separate from ConfigFileStore so deployments can replace
// it with an encrypted or operating-system-backed SecretStore.
type ConfigFileStore struct{ fileStore }
type SecretFileStore struct{ fileStore }

func (f ConfigFileStore) Load(scope Scope) (map[string]json.RawMessage, error) {
	data, err := f.fileStore.loadBytes(scope)
	if err != nil {
		return nil, err
	}
	v := map[string]json.RawMessage{}
	err = json.Unmarshal(data, &v)
	return v, err
}
func (f ConfigFileStore) Save(scope Scope, v map[string]json.RawMessage) error {
	return f.fileStore.save(scope, v)
}
func (f SecretFileStore) Load(scope Scope) (map[string]string, error) {
	data, err := f.fileStore.loadBytes(scope)
	if err != nil {
		return nil, err
	}
	v := map[string]string{}
	err = json.Unmarshal(data, &v)
	return v, err
}
func (f SecretFileStore) Save(scope Scope, v map[string]string) error {
	return f.fileStore.save(scope, v)
}

func NewTypedFileStores(root string) (ConfigStore, SecretStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, nil, fmt.Errorf("plugin configuration directory is empty")
	}
	configs := ConfigFileStore{fileStore{root: filepath.Join(root, "plugin-config")}}
	secrets := SecretFileStore{fileStore{root: filepath.Join(root, "plugin-secrets")}}
	if err := os.MkdirAll(configs.root, 0700); err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(configs.root, 0700); err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(secrets.root, 0700); err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(secrets.root, 0700); err != nil {
		return nil, nil, err
	}
	return configs, secrets, nil
}
