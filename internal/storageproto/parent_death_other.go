//go:build !linux

package storageproto

import "os/exec"

// Other platforms use the provider's reserved stdin liveness pipe. EOF means
// the parent process exited and the provider must abort active requests.
func configureParentDeathSignal(*exec.Cmd) {}
