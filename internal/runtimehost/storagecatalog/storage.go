package storagecatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func ensurePrivateDirectory(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafeStore
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return nil
}

func requireDirectory(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode.Perm() {
		return ErrUnsafeStore
	}
	return nil
}

func readPrivateRegular(path string, max int64, mode os.FileMode) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm() != mode.Perm() || before.Size() < 0 || before.Size() > max {
		return nil, ErrUnsafeStore
	}
	file, err := openSourceNoFollow(path)
	if err != nil {
		return nil, ErrUnsafeStore
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || opened.Size() != before.Size() || opened.Mode().Perm() != mode.Perm() {
		return nil, ErrUnsafeStore
	}
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil || int64(len(data)) != opened.Size() || int64(len(data)) > max {
		return nil, ErrUnsafeStore
	}
	after, err := os.Lstat(path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || after.Mode().Perm() != mode.Perm() || !os.SameFile(opened, after) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return nil, ErrUnsafeStore
	}
	return data, nil
}

func verifyExecutable(path string, expectedSize int64, expectedDigest string) error {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm() != 0500 || before.Size() != expectedSize || expectedSize <= 0 || expectedSize > MaxArtifactBytes {
		return ErrInvalidArtifact
	}
	file, err := openSourceNoFollow(path)
	if err != nil {
		return ErrInvalidArtifact
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || opened.Size() != expectedSize || opened.Mode().Perm() != 0500 {
		return ErrInvalidArtifact
	}
	h := sha256.New()
	read, err := io.Copy(h, io.LimitReader(file, MaxArtifactBytes+1))
	if err != nil || read != expectedSize || hex.EncodeToString(h.Sum(nil)) != expectedDigest {
		return ErrInvalidArtifact
	}
	after, err := os.Lstat(path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || after.Mode().Perm() != 0500 || !os.SameFile(opened, after) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return ErrInvalidArtifact
	}
	return nil
}

func writePrivateFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return writeErr
	}
	return os.Chmod(path, mode)
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
		info, err := os.Lstat(filepath.Join(path, item.Name()))
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
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
