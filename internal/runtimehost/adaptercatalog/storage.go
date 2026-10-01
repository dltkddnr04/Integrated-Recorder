package adaptercatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func (c *Catalog) ensureLayout() error {
	if err := ensurePrivateDirectory(c.root); err != nil {
		return ErrUnsafeStore
	}
	for _, name := range []string{"artifacts", "sets", "staging"} {
		if err := ensurePrivateDirectory(filepath.Join(c.root, name)); err != nil {
			return ErrUnsafeStore
		}
	}
	return nil
}

func (c *Catalog) checkLayout() error {
	for _, path := range []string{c.root, filepath.Join(c.root, "artifacts"), filepath.Join(c.root, "sets"), filepath.Join(c.root, "staging")} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return ErrUnsafeStore
		}
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafeStore
	}
	if err := os.Chmod(path, 0700); err != nil {
		return err
	}
	return nil
}

func requireDirectory(path string, chmod bool) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return ErrUnsafeStore
	}
	if chmod {
		if err := os.Chmod(path, 0700); err != nil {
			return err
		}
	}
	return nil
}

func readPrivateRegular(path string, max int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm()&0077 != 0 || before.Size() < 0 || before.Size() > max {
		return nil, ErrUnsafeStore
	}
	f, err := openSourceNoFollow(path)
	if err != nil {
		return nil, ErrUnsafeStore
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || opened.Size() != before.Size() {
		return nil, ErrUnsafeStore
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(data)) != opened.Size() || int64(len(data)) > max {
		return nil, ErrUnsafeStore
	}
	after, err := os.Lstat(path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(opened, after) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return nil, ErrUnsafeStore
	}
	return data, nil
}

func verifyExecutable(path string, expectedSize int64, expectedSHA string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0077 != 0 || info.Size() != expectedSize || expectedSize <= 0 || expectedSize > MaxArtifactBytes {
		return ErrInvalidSet
	}
	f, err := openSourceNoFollow(path)
	if err != nil {
		return ErrInvalidSet
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() != expectedSize {
		return ErrInvalidSet
	}
	h := sha256.New()
	read, err := io.Copy(h, io.LimitReader(f, MaxArtifactBytes+1))
	if err != nil || read != expectedSize || hex.EncodeToString(h.Sum(nil)) != expectedSHA {
		return ErrInvalidSet
	}
	after, err := os.Lstat(path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(opened, after) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return ErrInvalidSet
	}
	return nil
}

func verifyArtifactDirectory(path, id string) error {
	if !validDigest(id) {
		return ErrInvalidSet
	}
	if err := requireDirectory(path, false); err != nil {
		return ErrInvalidSet
	}
	items, err := os.ReadDir(path)
	if err != nil || len(items) != 1 || items[0].Name() != "adapter" || items[0].IsDir() {
		return ErrInvalidSet
	}
	info, err := os.Lstat(filepath.Join(path, "adapter"))
	if err != nil || info.Size() <= 0 || info.Size() > MaxArtifactBytes {
		return ErrInvalidSet
	}
	return verifyExecutable(filepath.Join(path, "adapter"), info.Size(), id)
}

func directoryHasNames(path string, expected []string) bool {
	items, err := os.ReadDir(path)
	if err != nil || len(items) != len(expected) {
		return false
	}
	for i, item := range items {
		if item.Name() != expected[i] || item.Type()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

func copyFile(source, target string, mode os.FileMode) error {
	input, err := openSourceNoFollow(source)
	if err != nil {
		return ErrUnsafeStore
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return ErrUnsafeStore
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return ErrUnsafeStore
	}
	_, copyErr := io.Copy(output, input)
	if copyErr == nil {
		copyErr = output.Sync()
	}
	closeErr := output.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return ErrUnsafeStore
	}
	if err := os.Chmod(target, mode); err != nil {
		return ErrUnsafeStore
	}
	return nil
}

func removeTreeOwned(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafeStore
	}
	if !info.IsDir() {
		return os.Remove(path)
	}
	if err := os.Chmod(path, 0700); err != nil {
		return ErrUnsafeStore
	}
	items, err := os.ReadDir(path)
	if err != nil {
		return ErrUnsafeStore
	}
	for _, item := range items {
		if err := removeTreeOwned(filepath.Join(path, item.Name())); err != nil {
			return err
		}
	}
	return os.Remove(path)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
