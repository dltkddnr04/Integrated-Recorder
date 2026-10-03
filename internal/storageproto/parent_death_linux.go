//go:build linux

package storageproto

import (
	"os/exec"
	"syscall"
)

// Linux delivers SIGTERM to the provider if its direct parent exits. The
// reserved stdin liveness pipe remains the portable fallback and also covers
// the small fork-to-exec race around PDEATHSIG setup.
func configureParentDeathSignal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
