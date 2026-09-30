//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package release

import (
	"os"
	"syscall"
)

func openRegularNoFollow(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "release-artifact"), nil
}
