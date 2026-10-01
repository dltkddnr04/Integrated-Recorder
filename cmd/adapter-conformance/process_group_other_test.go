//go:build !darwin && !linux

package main

import (
	"errors"
	"os/exec"
)

func startSleepDescendant(bool) (*exec.Cmd, error) {
	return nil, errors.New("sleep descendant fixture is unsupported on this platform")
}

func startMarkerDescendant(string) (*exec.Cmd, error) {
	return nil, errors.New("marker descendant fixture is unsupported on this platform")
}
