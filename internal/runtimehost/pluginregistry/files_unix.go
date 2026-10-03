//go:build unix

package pluginregistry

import (
	"os"
	"syscall"
)

func openRegularNoFollow(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "plugin-registry-file"), nil
}
