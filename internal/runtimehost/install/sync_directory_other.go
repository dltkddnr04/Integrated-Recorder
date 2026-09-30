//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package install

// The standard library does not expose portable directory syncing on all
// supported operating systems. File syncs and atomic rename are still used.
func syncDirectory(string) error { return nil }
