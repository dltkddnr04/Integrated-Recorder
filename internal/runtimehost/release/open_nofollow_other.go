//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package release

import (
	"os"
)

// Platforms without a standard-library no-follow open primitive use the
// surrounding Lstat/open/SameFile checks in VerifyArtifactFile.
func openRegularNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
