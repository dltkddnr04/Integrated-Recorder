//go:build !unix

package storagecatalog

import "os"

// Non-Unix platforms do not expose O_NOFOLLOW through the standard library.
// The catalog still compares path and opened-file identities before accepting
// stored data or a source snapshot.
func openSourceNoFollow(path string) (*os.File, error) { return os.Open(path) }

func syncDirectory(string) error { return nil }
