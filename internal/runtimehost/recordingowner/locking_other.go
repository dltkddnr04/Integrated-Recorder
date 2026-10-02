//go:build !darwin && !linux

package recordingowner

import "os"

func lockingSupported() bool { return false }

func openLockShard(string) (*os.File, error) { return nil, ErrLockUnsupported }
func openOwnerRead(string) (*os.File, error) { return nil, ErrLockUnsupported }
func lockFile(*os.File) error                { return ErrLockUnsupported }
func unlockFile(*os.File)                    {}
