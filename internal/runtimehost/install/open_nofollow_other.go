//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package install

import "os"

// On platforms without a portable no-follow open flag, callers perform
// Lstat/open/SameFile checks around this open.
func openNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
