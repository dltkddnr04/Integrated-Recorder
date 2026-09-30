//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package install

import (
	"errors"
	"os"
	"syscall"
)

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		if errors.Is(syncErr, syscall.EINVAL) || errors.Is(syncErr, syscall.ENOTSUP) {
			return closeErr
		}
		return syncErr
	}
	return closeErr
}
