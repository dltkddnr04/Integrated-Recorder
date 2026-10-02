//go:build !runtime_e2e

package runtimehook

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalBuildHooksIgnoreEnvironment(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IR_RUNTIME_E2E_FAILPOINT", string(BeforeOwnerCAS))
	t.Setenv("IR_RUNTIME_E2E_MARKER_DIR", directory)
	if err := Pause(BeforeOwnerCAS, "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("normal build hook must be inert: %v", err)
	}
	if got := ChildEnvironment(); len(got) != 0 {
		t.Fatalf("normal build forwarded test environment: %v", got)
	}
	if _, err := os.Stat(ReadyMarkerPath(directory, BeforeOwnerCAS, "0123456789abcdef0123456789abcdef")); !os.IsNotExist(err) {
		t.Fatalf("normal build created failpoint marker: %v", err)
	}
	if err := Observe(StaleOwnerCommitRejected, "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("normal build observation must be inert: %v", err)
	}
	if _, err := os.Stat(ObservationMarkerPath(directory, StaleOwnerCommitRejected, "0123456789abcdef0123456789abcdef")); !os.IsNotExist(err) {
		t.Fatalf("normal build created observation marker: %v", err)
	}
}

func TestObservationMarkerPathUsesAllowlistedComponents(t *testing.T) {
	if got := filepath.Base(ObservationMarkerPath("/private/test", StaleOwnerCommitRejected, "0123456789abcdef0123456789abcdef")); got != "observed-stale_owner_commit_rejected-0123456789abcdef0123456789abcdef" {
		t.Fatalf("unexpected observation marker path: %q", got)
	}
}

func TestMarkerPathsUseOnlyValidatedComponents(t *testing.T) {
	if got := filepath.Base(ReadyMarkerPath("/private/test", BeforeOwnerCAS, "0123456789abcdef0123456789abcdef")); got != "ready-before_owner_cas-0123456789abcdef0123456789abcdef" {
		t.Fatalf("unexpected ready marker path: %q", got)
	}
}
