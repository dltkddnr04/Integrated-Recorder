//go:build runtime_e2e

package supervisor

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const runtimeE2EChildDiagnosticLimit = 32 << 10

func runtimeE2EChildOutput(spec ProcessSpec) (io.Writer, func(), error) {
	markerDir := ""
	enabled := false
	for _, item := range spec.Env {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		switch key {
		case "IR_RUNTIME_E2E_FAILPOINT":
			enabled = value != ""
		case "IR_RUNTIME_E2E_MARKER_DIR":
			markerDir = value
		}
	}
	if !enabled || markerDir == "" || !filepath.IsAbs(markerDir) || filepath.Clean(markerDir) != markerDir {
		return nil, func() {}, nil
	}
	directory, err := os.Lstat(markerDir)
	if err != nil || !directory.IsDir() || directory.Mode()&os.ModeSymlink != 0 || directory.Mode().Perm() != 0700 {
		return nil, func() {}, errors.New("runtime E2E child diagnostic directory is invalid")
	}
	file, err := os.CreateTemp(markerDir, ".child-diagnostic-*.log")
	if err != nil {
		return nil, func() {}, errors.New("runtime E2E child diagnostics are unavailable")
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, func() {}, errors.New("runtime E2E child diagnostics are unavailable")
	}
	writer := &boundedRuntimeE2EDiagnosticWriter{file: file}
	close := func() { _ = file.Close() }
	return writer, close, nil
}

type boundedRuntimeE2EDiagnosticWriter struct {
	mu      sync.Mutex
	file    *os.File
	written int
}

func (w *boundedRuntimeE2EDiagnosticWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	accepted := len(data)
	remaining := runtimeE2EChildDiagnosticLimit - w.written
	if remaining <= 0 {
		return accepted, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
	}
	n, err := w.file.Write(data)
	w.written += n
	if err != nil {
		return accepted, err
	}
	return accepted, nil
}
