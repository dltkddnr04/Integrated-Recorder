//go:build !darwin && !linux

package main

import "os/exec"

func configureProcess(_ *exec.Cmd) {}

func killProcessTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
