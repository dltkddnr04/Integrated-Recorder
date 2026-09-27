package management

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAdapterPreferencesDefaultAndReload(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if !store.AdapterEnabled("alpha") || len(store.DisabledAdapters()) != 0 {
		t.Fatal("unknown adapters must default to enabled")
	}
	if err := store.SetAdapterEnabled("beta", false); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAdapterEnabled("alpha", false); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAdapterEnabled("beta", true); err != nil {
		t.Fatal(err)
	}
	if got := store.DisabledAdapters(); !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("disabled adapters=%v", got)
	}

	path := filepath.Join(root, "management", adapterPreferenceFile)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("preference file mode=%v err=%v", info, err)
	}
	reloaded, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.AdapterEnabled("alpha") || !reloaded.AdapterEnabled("beta") || !reflect.DeepEqual(reloaded.DisabledAdapters(), []string{"alpha"}) {
		t.Fatalf("reloaded state disabled=%v", reloaded.DisabledAdapters())
	}
}

func TestAdapterPreferencesRejectInvalidIDsAndKeepMemoryOnWriteFailure(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../alpha", "bad/id", strings.Repeat("x", 65)} {
		if err := store.SetAdapterEnabled(id, false); err == nil {
			t.Fatalf("invalid adapter identifier %q was accepted", id)
		}
	}
	path := filepath.Join(root, "management", adapterPreferenceFile)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAdapterEnabled("alpha", false); err == nil {
		t.Fatal("preference write over a directory unexpectedly succeeded")
	}
	if !store.AdapterEnabled("alpha") || len(store.DisabledAdapters()) != 0 {
		t.Fatal("failed write changed in-memory adapter preference")
	}
}
