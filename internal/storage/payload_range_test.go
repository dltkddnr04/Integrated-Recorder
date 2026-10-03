package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalFilesystemPayloadRangeReaderReturnsOnlyRequestedBytes(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "0123456789abcdef0123456789abcdef"
	const path = "tracks/main/segment.m4s"
	if _, err := store.SavePayload(id, path, bytes.NewBufferString("0123456789"), 32); err != nil {
		t.Fatal(err)
	}

	reader, err := store.OpenPayloadRangeReaderContext(context.Background(), id, path, 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "3456" {
		t.Fatalf("range body=%q, want %q", got, "3456")
	}
	if len(got) != 4 {
		t.Fatalf("range length=%d, want 4", len(got))
	}
}

func TestLocalFilesystemPayloadRangeReaderRejectsInvalidRangesAndPaths(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	const id = "0123456789abcdef0123456789abcdef"
	const path = "tracks/main/segment.m4s"
	if _, err := store.SavePayload(id, path, bytes.NewBufferString("0123456789"), 32); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, id, path string
		offset, length int64
	}{
		{name: "negative offset", id: id, path: path, offset: -1, length: 1},
		{name: "zero length", id: id, path: path, offset: 0, length: 0},
		{name: "out of bounds", id: id, path: path, offset: 9, length: 2},
		{name: "path traversal", id: id, path: "../escape", offset: 0, length: 1},
		{name: "invalid recording id", id: "../escape", path: path, offset: 0, length: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, openErr := store.OpenPayloadRangeReaderContext(context.Background(), test.id, test.path, test.offset, test.length)
			if openErr == nil {
				_ = reader.Close()
				t.Fatal("invalid range/path was opened")
			}
		})
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if reader, openErr := store.OpenPayloadRangeReaderContext(canceled, id, path, 0, 1); openErr == nil {
		_ = reader.Close()
		t.Fatal("canceled range was opened")
	}

	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "recordings", id, "escape")); err != nil {
		t.Fatal(err)
	}
	if reader, openErr := store.OpenPayloadRangeReaderContext(context.Background(), id, "escape", 0, 1); openErr == nil {
		_ = reader.Close()
		t.Fatal("symlink escape was opened")
	}
}
