//go:build darwin || linux

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func startSleepDescendant(separateGroup bool) (*exec.Cmd, error) {
	path, err := exec.LookPath("sleep")
	if err != nil {
		return nil, err
	}
	child := exec.Command(path, "60")
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if separateGroup {
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := child.Start(); err != nil {
		return nil, err
	}
	return child, nil
}

func startMarkerDescendant(path string) (*exec.Cmd, error) {
	command := "sleep 1; printf survived > " + shellQuote(path)
	child := exec.Command("/bin/sh", "-c", command)
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		return nil, err
	}
	return child, nil
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func killTestProcess(pid int) {
	if processExists(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
