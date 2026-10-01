//go:build !unix

package adaptercatalog

import "os"

// Non-Unix platforms do not expose O_NOFOLLOW through the standard library.
// Callers still compare Lstat/opened-file/path identities before accepting a
// source or stored object.
func openSourceNoFollow(path string) (*os.File, error) { return os.Open(path) }

func syncDirectory(string) error { return nil }
