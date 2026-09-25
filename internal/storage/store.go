// Package storage persists recordings as self-describing directories. The JSON
// documents and original payload files are the source of truth; no DB is used.
package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/domain"
)

var recordingIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Store struct{ root string }

func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("data directory is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Join(abs, "recordings"), 0755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	return &Store{root: abs}, nil
}

func (s *Store) Root() string { return s.root }

func (s *Store) NewRecordingDir(id string) error {
	if !recordingIDPattern.MatchString(id) {
		return fmt.Errorf("invalid recording id")
	}
	for _, dir := range []string{"manifests", "tracks/main"} {
		if err := os.MkdirAll(filepath.Join(s.recordingDir(id), dir), 0755); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveRecording(recording *domain.Recording) error {
	if recording == nil || !recordingIDPattern.MatchString(recording.ID) {
		return fmt.Errorf("invalid recording")
	}
	data, err := json.MarshalIndent(recording, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.recordingDir(recording.ID), "recording.json"), append(data, '\n'), 0644)
}

func (s *Store) LoadAll() ([]*domain.Recording, error) {
	base := filepath.Join(s.root, "recordings")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	var recordings []*domain.Recording
	for _, entry := range entries {
		if !entry.IsDir() || !recordingIDPattern.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(base, entry.Name(), "recording.json")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", entry.Name(), err)
		}
		var recording domain.Recording
		if err = json.Unmarshal(data, &recording); err != nil {
			return nil, fmt.Errorf("decode %s: %w", entry.Name(), err)
		}
		if recording.ID != entry.Name() {
			return nil, fmt.Errorf("recording id mismatch in %s", path)
		}
		if recording.Tracks == nil {
			recording.Tracks = map[string]*domain.Track{}
		}
		for key, track := range recording.Tracks {
			if track == nil {
				return nil, fmt.Errorf("recording %s has empty track %s", recording.ID, key)
			}
			sortTrack(track)
		}
		if recording.State == domain.StateRecording {
			now := time.Now().UTC()
			recording.State = domain.StateInterrupted
			recording.StoppedAt = &now
			if recording.LastError == "" {
				recording.LastError = "server restarted while recording was active"
			}
			for _, track := range recording.Tracks {
				for _, sequence := range track.PendingSequences {
					covered := false
					for _, gap := range recording.Gaps {
						if gap.TrackID == track.ID && sequence >= gap.FromSequence && sequence <= gap.ToSequence {
							covered = true
							break
						}
					}
					if !covered {
						recording.Gaps = append(recording.Gaps, domain.Gap{TrackID: track.ID, FromSequence: sequence, ToSequence: sequence, DetectedAt: now, Reason: "server restarted before pending segment could be captured"})
					}
				}
				track.PendingSequences = nil
			}
			if err = s.SaveRecording(&recording); err != nil {
				return nil, fmt.Errorf("mark stale recording %s interrupted: %w", recording.ID, err)
			}
		}
		recordings = append(recordings, &recording)
	}
	return recordings, nil
}

type PayloadResult struct {
	Size   int64
	SHA256 string
}

// SavePayload writes exactly the bytes read from src and publishes them only
// after a complete write. The hash is calculated over those same bytes.
func (s *Store) SavePayload(id, relativePath string, src io.Reader, limit int64) (PayloadResult, error) {
	return s.savePayload(id, relativePath, src, limit, -1)
}

// SavePayloadExact also requires a specific byte count before publishing the
// file, which is used for HLS byte-range responses.
func (s *Store) SavePayloadExact(id, relativePath string, src io.Reader, limit, expectedSize int64) (PayloadResult, error) {
	return s.savePayload(id, relativePath, src, limit, expectedSize)
}

func (s *Store) savePayload(id, relativePath string, src io.Reader, limit, expectedSize int64) (PayloadResult, error) {
	destination, err := s.safePath(id, relativePath)
	if err != nil {
		return PayloadResult{}, err
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return PayloadResult{}, err
	}
	f, err := os.CreateTemp(filepath.Dir(destination), ".payload-*.tmp")
	if err != nil {
		return PayloadResult{}, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	hash := sha256.New()
	reader := io.Reader(src)
	if limit > 0 {
		reader = io.LimitReader(src, limit+1)
	}
	n, copyErr := io.Copy(io.MultiWriter(f, hash), reader)
	if copyErr == nil && limit > 0 && n > limit {
		copyErr = fmt.Errorf("payload exceeds %d bytes", limit)
	}
	if copyErr == nil && expectedSize >= 0 && n != expectedSize {
		copyErr = fmt.Errorf("payload has %d bytes; expected %d", n, expectedSize)
	}
	if copyErr == nil {
		copyErr = f.Sync()
	}
	closeErr := f.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return PayloadResult{}, copyErr
	}
	if err = os.Rename(tmp, destination); err != nil {
		return PayloadResult{}, err
	}
	return PayloadResult{Size: n, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func (s *Store) SaveSidecar(id, relativePath string, v any) error {
	path, err := s.safePath(id, relativePath)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path+".json", append(data, '\n'), 0644)
}

func (s *Store) SaveSnapshot(id, trackID, sourceURI string, data []byte, at time.Time) (domain.ManifestSnapshot, error) {
	if len(data) > 4<<20 {
		return domain.ManifestSnapshot{}, fmt.Errorf("manifest exceeds size limit")
	}
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	name := fmt.Sprintf("manifests/%s-%d-%s.m3u8", safeName(trackID), at.UnixNano(), digest[:12])
	if _, err := s.SavePayload(id, name, bytes.NewReader(data), 4<<20); err != nil {
		return domain.ManifestSnapshot{}, err
	}
	return domain.ManifestSnapshot{TrackID: trackID, SourceURI: sourceURI, StoragePath: name, FetchedAt: at, SHA256: digest, Size: int64(len(data))}, nil
}

func (s *Store) OpenPayload(id, relativePath string) (*os.File, error) {
	path, err := s.safePath(id, relativePath)
	if err != nil {
		return nil, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(s.recordingDir(id))
	if err != nil {
		return nil, err
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if !within(resolvedRoot, resolvedPath) {
		return nil, fmt.Errorf("payload path escapes recording directory")
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("payload is not a regular file")
	}
	return os.Open(resolvedPath)
}

func (s *Store) recordingDir(id string) string { return filepath.Join(s.root, "recordings", id) }

func (s *Store) safePath(id, relative string) (string, error) {
	if !recordingIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid recording id")
	}
	if filepath.IsAbs(relative) || relative == "" {
		return "", fmt.Errorf("invalid relative storage path")
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("storage path escapes recording directory")
	}
	root := s.recordingDir(id)
	full := filepath.Join(root, clean)
	if !within(root, full) {
		return "", fmt.Errorf("storage path escapes recording directory")
	}
	return full, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".metadata-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func safeName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "main"
	}
	return b.String()
}
func sortTrack(track *domain.Track) {
	for i := 1; i < len(track.Segments); i++ {
		for j := i; j > 0 && track.Segments[j].Sequence < track.Segments[j-1].Sequence; j-- {
			track.Segments[j], track.Segments[j-1] = track.Segments[j-1], track.Segments[j]
		}
	}
}

var ErrNotFound = errors.New("recording not found")
