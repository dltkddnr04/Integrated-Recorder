package systemsettings

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestOpenDefaultsAndPermissions(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{UI: UISettings{Theme: "system"}, Integrity: IntegritySettings{Concurrency: 2}, Retention: RetentionSettings{Enabled: false, CompletedAfterDays: 30}}
	if got := store.Current(); got != want {
		t.Fatalf("Current() = %#v, want %#v", got, want)
	}
	if got := store.IntegrityConcurrency(); got != 2 {
		t.Fatalf("IntegrityConcurrency() = %d, want 2", got)
	}
	if mode := modeOf(t, filepath.Join(root, "management")); mode.Perm() != 0700 {
		t.Fatalf("management mode = %04o, want 0700", mode.Perm())
	}
	if mode := modeOf(t, store.path); mode.Perm() != 0600 {
		t.Fatalf("settings mode = %04o, want 0600", mode.Perm())
	}
}

func TestUpdatePersistsAndReloads(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	theme := "dark"
	concurrency := 4
	got, err := store.Update(Patch{UITheme: &theme, IntegrityConcurrency: &concurrency})
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{UI: UISettings{Theme: "dark"}, Integrity: IntegritySettings{Concurrency: 4}, Retention: RetentionSettings{Enabled: false, CompletedAfterDays: 30}}
	if got != want {
		t.Fatalf("Update() = %#v, want %#v", got, want)
	}
	reloaded, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Current(); got != want {
		t.Fatalf("reloaded Current() = %#v, want %#v", got, want)
	}
}

func TestUpdateRejectsInvalidValuesWithoutEchoingThem(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		patch Patch
	}{
		{name: "theme", patch: Patch{UITheme: ptr("neon-secret-value")}},
		{name: "low concurrency", patch: Patch{IntegrityConcurrency: ptr(0)}},
		{name: "high concurrency", patch: Patch{IntegrityConcurrency: ptr(5)}},
		{name: "retention below range", patch: Patch{RetentionAfterDays: ptr(0)}},
		{name: "retention above range", patch: Patch{RetentionAfterDays: ptr(3651)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := store.Current()
			_, err := store.Update(test.patch)
			if !errors.Is(err, ErrInvalidSettings) {
				t.Fatalf("Update() error = %v, want ErrInvalidSettings", err)
			}
			if got := store.Current(); got != before {
				t.Fatalf("invalid update changed settings: %#v", got)
			}
			if test.patch.UITheme != nil && contains(err.Error(), *test.patch.UITheme) {
				t.Fatalf("error echoed input value: %v", err)
			}
		})
	}
}

func TestOpenBackfillsRetentionDefaultsForExistingSettings(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "management"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "management", settingsFilename)
	if err := os.WriteFile(path, []byte(`{"ui":{"theme":"dark"},"integrity":{"concurrency":3}}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	want := RetentionSettings{Enabled: false, CompletedAfterDays: 30}
	if got := store.Current().Retention; got != want {
		t.Fatalf("existing settings retention = %#v, want %#v", got, want)
	}
	if _, err := store.Update(Patch{RetentionEnabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Current().Retention; got != (RetentionSettings{Enabled: true, CompletedAfterDays: 30}) {
		t.Fatalf("retention after partial update/reload = %#v", got)
	}
}

func TestOpenRejectsUnknownAndTrailingJSON(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
	}{
		{name: "unknown field", data: `{"ui":{"theme":"system"},"integrity":{"concurrency":2},"extra":"x"}`},
		{name: "trailing object", data: `{"ui":{"theme":"system"},"integrity":{"concurrency":2}} {}`},
		{name: "trailing garbage", data: `{"ui":{"theme":"system"},"integrity":{"concurrency":2}}x`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "management"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "management", settingsFilename)
			if err := os.WriteFile(path, []byte(test.data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(root); !errors.Is(err, ErrInvalidSettings) {
				t.Fatalf("Open() error = %v, want ErrInvalidSettings", err)
			}
		})
	}
}

func TestUpdatePersistenceFailureLeavesMemoryUnchanged(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	before := store.Current()
	if err := os.Remove(store.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(store.path, 0700); err != nil {
		t.Fatal(err)
	}
	theme := "light"
	if _, err := store.Update(Patch{UITheme: &theme}); err == nil {
		t.Fatal("Update() succeeded with a directory at the settings file path")
	}
	if got := store.Current(); got != before {
		t.Fatalf("failed update changed in-memory state: %#v", got)
	}
}

func TestOpenRejectsSymlinkPaths(t *testing.T) {
	t.Run("management directory", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "management")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := Open(root); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Open() error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("settings file", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(outside, []byte(`{"ui":{"theme":"system"},"integrity":{"concurrency":2}}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "management"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "management", settingsFilename)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := Open(root); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Open() error = %v, want ErrUnsafePath", err)
		}
	})
}

func TestConcurrentUpdateAndCurrent(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	themes := []string{"system", "light", "dark"}
	var wg sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				theme := themes[(worker+i)%len(themes)]
				concurrency := (worker+i)%4 + 1
				if _, err := store.Update(Patch{UITheme: &theme, IntegrityConcurrency: &concurrency}); err != nil {
					t.Errorf("Update() error = %v", err)
					return
				}
				got := store.Current()
				if err := validate(got); err != nil {
					t.Errorf("Current() returned invalid settings: %#v: %v", got, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if _, err := Open(filepath.Dir(store.directory)); err != nil {
		t.Fatalf("reload after concurrent writes: %v", err)
	}
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}

func ptr[T any](value T) *T { return &value }

func contains(s, part string) bool {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
