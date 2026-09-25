// Package pluginconfig stores opaque plugin configuration and secret values
// independently. Scope identifiers are hashed before they become paths.
package pluginconfig

import (
	"crypto/sha256"
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
	if err := adapterproto.ValidateValues(ordinarySchema, values); err != nil {
		return err
	}
	secretRaw := map[string]json.RawMessage{}
	for key, value := range secrets {
		b, _ := json.Marshal(value)
		secretRaw[key] = b
	}
	if err := adapterproto.ValidateValues(secretSchema, secretRaw); err != nil {
		return err
	}
	current, err := s.Get(scope)
	if err != nil {
		return err
	}
	mergedSecrets := current.Secrets
	for key, value := range secrets {
		if value == "" {
			delete(mergedSecrets, key)
		} else {
			mergedSecrets[key] = value
		}
	}
	// Required secrets are checked after applying updates. Existing secret
	// values are never returned from this service's API projection.
	for _, field := range secretSchema.Fields {
		if field.Required {
			if _, ok := mergedSecrets[field.Key]; !ok && len(field.Default) == 0 {
				return fmt.Errorf("required secret %q is not configured", field.Key)
			}
		}
	}
	if err = s.configs.Save(scope, values); err != nil {
		return err
	}
	return s.secrets.Save(scope, mergedSecrets)
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
