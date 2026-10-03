package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"time"
)

const (
	providerProbePrefix      = "_integrated-recorder/system-probes/v1/"
	providerProbePayloadSize = 521
	providerProbePageSize    = 32
	providerProbeMaxResidue  = 128
)

// ProbePhysicalObjectStore exercises the object operations Core needs before
// selecting an external provider as canonical storage. It uses a reserved
// non-recording namespace and never creates or mutates archive-domain objects.
// Every call is bounded, payload reads stay small, and failed probes attempt
// best-effort cleanup of their unique object.
func ProbePhysicalObjectStore(parent context.Context, objects PhysicalObjectStore) error {
	if parent == nil || objects == nil {
		return errors.New("storage provider probe input is invalid")
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	if err := cleanupProviderProbeResidue(ctx, objects); err != nil {
		return errors.New("storage provider probe namespace is unavailable")
	}

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return errors.New("storage provider probe could not start")
	}
	key := providerProbePrefix + hex.EncodeToString(nonce[:])
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = objects.Delete(cleanupCtx, key)
	}()

	want := make([]byte, providerProbePayloadSize)
	if _, err := rand.Read(want); err != nil {
		return errors.New("storage provider probe could not start")
	}
	wantDigest := sha256.Sum256(want)
	wantSHA := hex.EncodeToString(wantDigest[:])

	info, err := objects.Put(ctx, key, bytes.NewReader(want), int64(len(want)))
	if err != nil || info.Key != key || info.Size != int64(len(want)) || info.SHA256 != wantSHA {
		return errors.New("storage provider probe write failed")
	}

	info, err = objects.Stat(ctx, key)
	if err != nil || info.Key != key || info.Size != int64(len(want)) || info.SHA256 != wantSHA {
		return errors.New("storage provider probe stat failed")
	}

	page, err := objects.List(ctx, providerProbePrefix, "", providerProbePageSize)
	if err != nil || !validProbePage(page) {
		return errors.New("storage provider probe list failed")
	}
	found := false
	for _, item := range page.Items {
		if item.Key == key && item.Size == int64(len(want)) && (item.SHA256 == "" || item.SHA256 == wantSHA) {
			found = true
		}
	}
	if !found || page.NextCursor != "" {
		return errors.New("storage provider probe list failed")
	}

	reader, readInfo, err := objects.Open(ctx, key)
	if err != nil {
		return errors.New("storage provider probe read failed")
	}
	got, readErr := io.ReadAll(io.LimitReader(reader, int64(len(want))+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || readInfo.Key != key || readInfo.Size != int64(len(want)) || !bytes.Equal(got, want) {
		return errors.New("storage provider probe read failed")
	}

	const offset, length = int64(37), int64(113)
	rangeReader, rangeInfo, err := objects.OpenRange(ctx, key, offset, length)
	if err != nil {
		return errors.New("storage provider probe range read failed")
	}
	rangeBytes, rangeErr := io.ReadAll(io.LimitReader(rangeReader, length+1))
	rangeCloseErr := rangeReader.Close()
	if rangeErr != nil || rangeCloseErr != nil || rangeInfo.Key != key || rangeInfo.Size != int64(len(want)) || !bytes.Equal(rangeBytes, want[offset:offset+length]) {
		return errors.New("storage provider probe range read failed")
	}

	if err := objects.Delete(ctx, key); err != nil {
		return errors.New("storage provider probe delete failed")
	}
	if _, err := objects.Stat(ctx, key); !errors.Is(err, ErrObjectNotFound) {
		return errors.New("storage provider probe delete verification failed")
	}
	return nil
}

func cleanupProviderProbeResidue(ctx context.Context, objects PhysicalObjectStore) error {
	cursor := ""
	removed := 0
	for {
		page, err := objects.List(ctx, providerProbePrefix, cursor, providerProbePageSize)
		if err != nil || !validProbePage(page) {
			return errors.New("probe list failed")
		}
		for _, item := range page.Items {
			removed++
			if removed > providerProbeMaxResidue {
				return errors.New("probe residue exceeds cleanup bound")
			}
			if err := objects.Delete(ctx, item.Key); err != nil {
				return errors.New("probe residue cleanup failed")
			}
			if _, err := objects.Stat(ctx, item.Key); !errors.Is(err, ErrObjectNotFound) {
				return errors.New("probe residue cleanup could not be verified")
			}
		}
		if page.NextCursor == "" {
			return nil
		}
		if len(page.Items) == 0 || page.NextCursor <= cursor || page.NextCursor != page.Items[len(page.Items)-1].Key {
			return errors.New("probe list cursor is invalid")
		}
		cursor = page.NextCursor
	}
}

func validProbePage(page PhysicalObjectPage) bool {
	if len(page.Items) > providerProbePageSize {
		return false
	}
	previous := ""
	for _, item := range page.Items {
		if ValidateObjectKey(item.Key) != nil || len(item.Key) <= len(providerProbePrefix) || item.Key[:len(providerProbePrefix)] != providerProbePrefix || item.Key <= previous || item.Size < 0 || item.Size > MaxObjectBytes || item.SHA256 != "" && !validLowerDigest(item.SHA256) {
			return false
		}
		previous = item.Key
	}
	if page.NextCursor != "" && (len(page.Items) == 0 || page.NextCursor != previous) {
		return false
	}
	return true
}
