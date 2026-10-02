//go:build !runtime_e2e

package supervisor

import "io"

// runtimeE2EChildOutput is inert in normal builds. The runtime_e2e build may
// opt into bounded child diagnostics in a private test directory.
func runtimeE2EChildOutput(ProcessSpec) (io.Writer, func(), error) {
	return nil, func() {}, nil
}
