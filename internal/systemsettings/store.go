// Package systemsettings stores the small set of Core settings that can be
// applied by the running management plane.
package systemsettings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	settingsDirectory    = "management"
	settingsFilename     = "system-settings.json"
	maxSettingsBytes     = 16 * 1024
	defaultTheme         = "system"
	defaultConcurrency   = 2
	defaultRetentionDays = 30
)

var (
	ErrInvalidSettings = errors.New("invalid system settings")
	ErrUnsafePath      = errors.New("unsafe system settings path")
)

// Settings is the public, self-describing system settings document.
type Settings struct {
	UI        UISettings        `json:"ui"`
	Integrity IntegritySettings `json:"integrity"`
	Retention RetentionSettings `json:"retention"`
}

type UISettings struct {
	Theme string `json:"theme"`
}

type IntegritySettings struct {
	Concurrency int `json:"concurrency"`
}

// RetentionSettings controls the optional deletion of old completed
// recordings. Cleanup is disabled unless explicitly enabled.
type RetentionSettings struct {
	Enabled            bool `json:"enabled"`
	CompletedAfterDays int  `json:"completed_after_days"`
}

// Patch updates only the fields provided by the caller.
type Patch struct {
	UITheme              *string `json:"ui_theme,omitempty"`
	IntegrityConcurrency *int    `json:"integrity_concurrency,omitempty"`
	RetentionEnabled     *bool   `json:"retention_enabled,omitempty"`
	RetentionAfterDays   *int    `json:"retention_completed_after_days,omitempty"`
}

// Store serializes updates and publishes a new in-memory snapshot only after
// the corresponding file has been durably replaced.
type Store struct {
	mu        sync.RWMutex
	path      string
	directory string
	settings  Settings
}

// Open loads the system settings from <dataRoot>/management. Missing settings
// are initialized with the supported defaults.
func Open(dataRoot string) (*Store, error) {
	if strings.TrimSpace(dataRoot) == "" {
		return nil, fmt.Errorf("%w: empty data root", ErrUnsafePath)
	}
	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve data root", ErrUnsafePath)
	}
	if err := ensureDataRoot(root); err != nil {
		return nil, err
	}
	dir := filepath.Join(root, settingsDirectory)
	if err := ensurePrivateDirectory(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, settingsFilename)
	store := &Store{
		path:      path,
		directory: dir,
		settings: Settings{
			UI:        UISettings{Theme: defaultTheme},
			Integrity: IntegritySettings{Concurrency: defaultConcurrency},
			Retention: RetentionSettings{Enabled: false, CompletedAfterDays: defaultRetentionDays},
		},
	}

	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := store.write(store.settings); err != nil {
			return nil, fmt.Errorf("initialize system settings: %w", err)
		}
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect system settings: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnsafePath
	}
	if info.Size() < 0 || info.Size() > maxSettingsBytes {
		return nil, fmt.Errorf("%w: settings file exceeds size limit", ErrInvalidSettings)
	}
	loaded, err := readSettings(path)
	if err != nil {
		return nil, err
	}
	if err := validate(loaded); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		return nil, fmt.Errorf("secure system settings file: %w", err)
	}
	store.settings = loaded
	return store, nil
}

// Current returns a value copy of the current settings.
func (s *Store) Current() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// IntegrityConcurrency returns the currently configured worker count.
func (s *Store) IntegrityConcurrency() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings.Integrity.Concurrency
}

// Update validates and persists a partial settings update atomically.
func (s *Store) Update(patch Patch) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	candidate := s.settings
	if patch.UITheme != nil {
		candidate.UI.Theme = *patch.UITheme
	}
	if patch.IntegrityConcurrency != nil {
		candidate.Integrity.Concurrency = *patch.IntegrityConcurrency
	}
	if patch.RetentionEnabled != nil {
		candidate.Retention.Enabled = *patch.RetentionEnabled
	}
	if patch.RetentionAfterDays != nil {
		candidate.Retention.CompletedAfterDays = *patch.RetentionAfterDays
	}
	if err := validate(candidate); err != nil {
		return s.settings, err
	}
	if err := s.write(candidate); err != nil {
		return s.settings, fmt.Errorf("persist system settings: %w", err)
	}
	s.settings = candidate
	return candidate, nil
}

func validate(settings Settings) error {
	switch settings.UI.Theme {
	case "system", "light", "dark":
	default:
		return fmt.Errorf("%w: unsupported UI theme", ErrInvalidSettings)
	}
	if settings.Integrity.Concurrency < 1 || settings.Integrity.Concurrency > 4 {
		return fmt.Errorf("%w: integrity concurrency is out of range", ErrInvalidSettings)
	}
	if settings.Retention.CompletedAfterDays < 1 || settings.Retention.CompletedAfterDays > 3650 {
		return fmt.Errorf("%w: retention age is out of range", ErrInvalidSettings)
	}
	return nil
}

func readSettings(path string) (Settings, error) {
	settings := Settings{
		UI:        UISettings{Theme: defaultTheme},
		Integrity: IntegritySettings{Concurrency: defaultConcurrency},
		Retention: RetentionSettings{Enabled: false, CompletedAfterDays: defaultRetentionDays},
	}
	f, err := os.Open(path)
	if err != nil {
		return settings, fmt.Errorf("open system settings: %w", err)
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil {
		return settings, fmt.Errorf("inspect system settings: %w", err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, pathInfo) {
		return settings, ErrUnsafePath
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSettingsBytes+1))
	if err != nil {
		return settings, fmt.Errorf("read system settings: %w", err)
	}
	if len(data) == 0 || len(data) > maxSettingsBytes {
		return settings, fmt.Errorf("%w: invalid settings size", ErrInvalidSettings)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return settings, fmt.Errorf("%w: malformed settings document", ErrInvalidSettings)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return settings, fmt.Errorf("%w: trailing data", ErrInvalidSettings)
	}
	return settings, nil
}

func (s *Store) write(settings Settings) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxSettingsBytes {
		return fmt.Errorf("%w: settings document exceeds size limit", ErrInvalidSettings)
	}
	if err := checkRegularOrMissing(s.path); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.directory, ".system-settings-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
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
	if err := checkRegularOrMissing(s.path); err != nil {
		return err
	}
	if err = os.Rename(tmpPath, s.path); err != nil {
		return err
	}
	return syncDirectory(s.directory)
}

func checkRegularOrMissing(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	}
	return nil
}

func ensureDataRoot(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("create data root: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create settings directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	}
	if err := os.Chmod(path, 0700); err != nil {
		return fmt.Errorf("secure settings directory: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
