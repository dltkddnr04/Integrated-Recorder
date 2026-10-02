//go:build darwin || linux

package recordingowner

import (
	"errors"
	"os"
	"syscall"
)

func lockingSupported() bool { return true }

func openLockShard(path string) (*os.File, error) {
	f, err := openNoFollow(path, syscall.O_RDWR|syscall.O_CREAT, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, ErrInvalidState
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() || !os.SameFile(info, pathInfo) {
		_ = f.Close()
		return nil, ErrInvalidState
	}
	return f, nil
}

func openOwnerRead(path string) (*os.File, error) {
	return openNoFollow(path, syscall.O_RDONLY, 0)
}

func openNoFollow(path string, flags int, mode uint32) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func lockFile(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			// Any failure to acquire the OS lock must fail closed. Returning the
			// sentinel makes unsupported filesystems/platform behavior explicit.
			return errors.Join(ErrLockUnsupported, err)
		}
		return nil
	}
}

func unlockFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
