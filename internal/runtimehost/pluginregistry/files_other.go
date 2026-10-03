//go:build !unix

package pluginregistry

import "os"

func openRegularNoFollow(path string) (*os.File, error) { return os.Open(path) }
