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
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/domain"
)

var recordingIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// ErrPayloadSizeMismatch marks a complete source response whose byte count
// did not match an exact range request. Callers may retry the source fetch;
// ordinary local I/O errors remain distinguishable and are not retried.
var ErrPayloadSizeMismatch = errors.New("payload size mismatch")

type Store struct {
	root string

	issuesMu sync.RWMutex
	issues   []RecoveryIssue
}

// RecoveryIssue describes preserved data that could not be safely attached to
// a recording. It intentionally contains no filesystem path, URI, or payload
// content.
type RecoveryIssue struct {
	ID      string `json:"id,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

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
	if err = os.Chmod(filepath.Join(abs, "recordings"), 0700); err != nil {
		return nil, err
	}
	return &Store{root: abs}, nil
}

func (s *Store) Root() string { return s.root }

// RecoveryIssues returns a snapshot of the most recent LoadAll recovery
// findings. Issues contain only safe identifiers and fixed messages.
func (s *Store) RecoveryIssues() []RecoveryIssue {
	s.issuesMu.RLock()
	defer s.issuesMu.RUnlock()
	return append([]RecoveryIssue(nil), s.issues...)
}

// HasCanonicalPayloadIssue reports whether startup recovery found a missing or
// integrity-mismatched payload referenced by this recording's canonical media
// timeline. It performs no filesystem reads and is intended for fail-closed
// playback projection checks.
func (s *Store) HasCanonicalPayloadIssue(recordingID string) bool {
	s.issuesMu.RLock()
	defer s.issuesMu.RUnlock()
	for _, issue := range s.issues {
		if issue.ID == recordingID && strings.HasPrefix(issue.Code, "canonical_payload_") {
			return true
		}
	}
	return false
}

func (s *Store) setRecoveryIssues(issues []RecoveryIssue) {
	s.issuesMu.Lock()
	s.issues = append([]RecoveryIssue(nil), issues...)
	s.issuesMu.Unlock()
}

func (s *Store) addRecoveryIssue(issue RecoveryIssue) {
	s.issuesMu.Lock()
	s.issues = append(s.issues, issue)
	s.issuesMu.Unlock()
}

func (s *Store) NewRecordingDir(id string) error {
	if !recordingIDPattern.MatchString(id) {
		return fmt.Errorf("invalid recording id")
	}
	for _, dir := range []string{"manifests", "tracks/main"} {
		if err := os.MkdirAll(filepath.Join(s.recordingDir(id), dir), 0700); err != nil {
			return err
		}
	}
	for _, dir := range []string{s.recordingDir(id), filepath.Join(s.recordingDir(id), "manifests"), filepath.Join(s.recordingDir(id), "tracks"), filepath.Join(s.recordingDir(id), "tracks", "main")} {
		if err := os.Chmod(dir, 0700); err != nil {
			return err
		}
	}
	return nil
}

// CreateRecording durably publishes a recording directory only after its
// initial self-describing root document exists. A crash before the final
// rename leaves an identifiable hidden staging directory for LoadAll to
// report; it never exposes a half-created final recording directory.
func (s *Store) CreateRecording(recording *domain.Recording) error {
	if recording == nil || !recordingIDPattern.MatchString(recording.ID) {
		return fmt.Errorf("invalid recording")
	}
	base := filepath.Join(s.root, "recordings")
	final := s.recordingDir(recording.ID)
	if _, err := os.Lstat(final); err == nil {
		return fmt.Errorf("recording already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stage, err := os.MkdirTemp(base, ".incomplete-"+recording.ID+"-")
	if err != nil {
		return err
	}
	if err = os.Chmod(stage, 0700); err != nil {
		return err
	}
	for _, dir := range []string{"manifests", "tracks", "tracks/main"} {
		path := filepath.Join(stage, dir)
		if err = os.MkdirAll(path, 0700); err != nil {
			return err
		}
		if err = os.Chmod(path, 0700); err != nil {
			return err
		}
	}
	if recording.Tracks == nil {
		recording.Tracks = map[string]*domain.Track{}
	}
	data, err := json.MarshalIndent(recording, "", "  ")
	if err != nil {
		return err
	}
	rootPath := filepath.Join(stage, "recording.json")
	if err = atomicWrite(rootPath, append(data, '\n'), 0600); err != nil {
		return err
	}
	// Persist all directory entries below the staging root before publishing
	// the root directory's name in the parent.
	for _, dir := range []string{filepath.Join(stage, "tracks", "main"), filepath.Join(stage, "tracks"), filepath.Join(stage, "manifests"), stage} {
		if err = syncDirectory(dir); err != nil {
			return err
		}
	}
	if err = os.Rename(stage, final); err != nil {
		return err
	}
	return syncDirectory(base)
}

func (s *Store) SaveRecording(recording *domain.Recording) error {
	if recording == nil || !recordingIDPattern.MatchString(recording.ID) {
		return fmt.Errorf("invalid recording")
	}
	data, err := json.MarshalIndent(recording, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.recordingDir(recording.ID), "recording.json"), append(data, '\n'), 0600)
}

func (s *Store) LoadAll() ([]*domain.Recording, error) {
	s.setRecoveryIssues(nil)
	base := filepath.Join(s.root, "recordings")
	if err := os.Chmod(base, 0700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	var recordings []*domain.Recording
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".incomplete-") {
			id := strings.TrimPrefix(entry.Name(), ".incomplete-")
			if i := strings.IndexByte(id, '-'); i >= 0 {
				id = id[:i]
			}
			if !recordingIDPattern.MatchString(id) {
				id = ""
			}
			if err = tightenRecordingTree(filepath.Join(base, entry.Name())); err != nil {
				s.addRecoveryIssue(RecoveryIssue{ID: id, Code: "incomplete_permissions_unavailable", Message: "incomplete recording data could not be restricted safely"})
				continue
			}
			s.addRecoveryIssue(RecoveryIssue{ID: id, Code: "incomplete_creation", Message: "incomplete recording creation data was preserved"})
			continue
		}
		if !recordingIDPattern.MatchString(entry.Name()) {
			continue
		}
		id := entry.Name()
		recordingDir := filepath.Join(base, id)
		if err = tightenRecordingTree(recordingDir); err != nil {
			s.addRecoveryIssue(RecoveryIssue{ID: id, Code: "permissions_unavailable", Message: "recording could not be restricted safely"})
			continue
		}
		path := filepath.Join(recordingDir, "recording.json")
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			code := "metadata_unavailable"
			if errors.Is(readErr, os.ErrNotExist) {
				code = "metadata_missing"
			}
			s.addRecoveryIssue(RecoveryIssue{ID: id, Code: code, Message: "recording metadata is unavailable; directory data was preserved"})
			continue
		}
		var recording domain.Recording
		if err = json.Unmarshal(data, &recording); err != nil {
			s.addRecoveryIssue(RecoveryIssue{ID: id, Code: "metadata_invalid", Message: "recording metadata is invalid; directory data was preserved"})
			continue
		}
		if recording.ID != id {
			s.addRecoveryIssue(RecoveryIssue{ID: id, Code: "metadata_id_mismatch", Message: "recording metadata identity does not match its directory"})
			continue
		}
		if recording.Tracks == nil {
			recording.Tracks = map[string]*domain.Track{}
		}
		changed, reconcileErr := s.reconcileRecording(&recording)
		if reconcileErr != nil {
			s.addRecoveryIssue(RecoveryIssue{ID: id, Code: "reconciliation_incomplete", Message: "recording payload reconciliation was incomplete"})
		}
		invalidTrack := false
		for _, track := range recording.Tracks {
			if track == nil {
				s.addRecoveryIssue(RecoveryIssue{ID: id, Code: "track_invalid", Message: "recording contains an invalid track and was preserved"})
				invalidTrack = true
			} else {
				sortTrack(track)
			}
		}
		if invalidTrack {
			continue
		}
		if recording.State == domain.StateRecording {
			now := time.Now().UTC()
			recording.State = domain.StateInterrupted
			recording.StoppedAt = &now
			if recording.LastError == "" {
				recording.LastError = "server restarted while recording was active"
			}
			for _, track := range recording.Tracks {
				addPendingGap := func(epoch, sequence uint64) {
					covered := false
					for _, gap := range recording.Gaps {
						if gap.TrackID == track.ID && gap.SourceEpoch == epoch && sequence >= gap.FromSequence && sequence <= gap.ToSequence {
							covered = true
							break
						}
					}
					if !covered {
						recording.Gaps = append(recording.Gaps, domain.Gap{TrackID: track.ID, SourceEpoch: epoch, FromSequence: sequence, ToSequence: sequence, DetectedAt: now, Reason: "server restarted before pending segment could be captured"})
					}
				}
				for _, sequence := range track.PendingSequences {
					// Older recordings did not persist epochs on pending sequences.
					addPendingGap(0, sequence)
				}
				for _, pending := range track.PendingSegments {
					addPendingGap(pending.SourceEpoch, pending.Sequence)
				}
				track.PendingSequences = nil
				track.PendingSegments = nil
			}
			changed = true
		}
		if changed {
			if err = s.SaveRecording(&recording); err != nil {
				s.addRecoveryIssue(RecoveryIssue{ID: id, Code: "metadata_recovery_write_failed", Message: "recovered metadata could not be durably saved"})
			}
		}
		recordings = append(recordings, &recording)
	}
	return recordings, nil
}

func (s *Store) reconcileRecording(recording *domain.Recording) (bool, error) {
	root := s.recordingDir(recording.ID)
	knownPaths := make(map[string]struct{})
	for _, snapshot := range recording.Snapshots {
		knownPaths[filepath.ToSlash(snapshot.StoragePath)] = struct{}{}
	}
	for _, track := range recording.Tracks {
		if track == nil {
			continue
		}
		for _, segment := range track.Segments {
			knownPaths[filepath.ToSlash(segment.StoragePath)] = struct{}{}
		}
		for _, segment := range track.InitSegments {
			knownPaths[filepath.ToSlash(segment.StoragePath)] = struct{}{}
		}
	}

	changed := false
	var walkErr error
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			walkErr = err
			return filepath.SkipAll
		}
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			walkErr = infoErr
			return filepath.SkipAll
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			walkErr = relErr
			return filepath.SkipAll
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "manifests/") {
			if strings.HasSuffix(rel, ".m3u8.json") {
				payloadRel := strings.TrimSuffix(rel, ".json")
				s.reconcileManifestSidecar(recording, rel, payloadRel, &knownPaths, &changed)
			} else if strings.HasSuffix(rel, ".m3u8") {
				if _, known := knownPaths[rel]; !known {
					if _, sidecarErr := os.Lstat(filepath.Join(root, filepath.FromSlash(rel+".json"))); errors.Is(sidecarErr, os.ErrNotExist) {
						s.addRecoveryIssue(RecoveryIssue{ID: recording.ID, Code: "orphan_manifest", Message: "manifest payload without committed metadata was preserved"})
					}
				}
			}
			return nil
		}
		if !strings.HasPrefix(rel, "tracks/") || rel == "tracks/" {
			return nil
		}
		if strings.HasSuffix(rel, ".json") {
			payloadRel := strings.TrimSuffix(rel, ".json")
			s.reconcileSidecar(recording, rel, payloadRel, &knownPaths, &changed)
			return nil
		}
		if strings.HasPrefix(filepath.Base(rel), ".") && strings.HasSuffix(rel, ".tmp") {
			return nil
		}
		if _, ok := knownPaths[rel]; ok {
			return nil
		}
		if _, sidecarErr := os.Lstat(filepath.Join(root, filepath.FromSlash(rel+".json"))); errors.Is(sidecarErr, os.ErrNotExist) {
			s.addRecoveryIssue(RecoveryIssue{ID: recording.ID, Code: "orphan_payload", Message: "track payload without committed metadata was preserved"})
		}
		return nil
	})
	// recording.json is canonical, so sidecar reconciliation alone cannot
	// detect a payload that disappeared after both metadata writes completed.
	// Verify every canonical reference during recovery and preserve the record
	// for inspection; playback will fail closed when it cannot open the file.
	seen := map[string]bool{}
	verify := func(relative string, expectedSize int64, expectedHash string, unavailableCode, mismatchCode string) {
		// The same object may legitimately back multiple references, but each
		// canonical expectation still needs validation. Include the expected
		// integrity tuple in the deduplication key so a conflicting reference
		// to the same path cannot bypass verification.
		seenKey := fmt.Sprintf("%s\x00%d\x00%s", relative, expectedSize, expectedHash)
		if seen[seenKey] {
			return
		}
		seen[seenKey] = true
		issue := func(code, message string) {
			s.addRecoveryIssue(RecoveryIssue{ID: recording.ID, Code: code, Message: message})
		}
		if _, err := s.safePath(recording.ID, relative); err != nil || relative == "" {
			issue(unavailableCode, "canonical payload reference is invalid or unavailable; metadata was preserved")
			return
		}
		file, err := s.OpenPayload(recording.ID, relative)
		if err != nil {
			issue(unavailableCode, "canonical payload is unavailable; metadata was preserved")
			return
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		digest, decodeErr := hex.DecodeString(expectedHash)
		if copyErr != nil || closeErr != nil || decodeErr != nil || len(digest) != sha256.Size || size != expectedSize || !bytes.Equal(hash.Sum(nil), digest) {
			issue(mismatchCode, "canonical payload failed size or integrity verification; metadata was preserved")
		}
	}
	for _, snapshot := range recording.Snapshots {
		verify(snapshot.StoragePath, snapshot.Size, snapshot.SHA256, "manifest_payload_unavailable", "manifest_payload_mismatch")
	}
	for _, track := range recording.Tracks {
		if track == nil {
			continue
		}
		for _, segment := range track.Segments {
			verify(segment.StoragePath, segment.PayloadSize, segment.SHA256, "canonical_payload_unavailable", "canonical_payload_mismatch")
		}
		for _, segment := range track.InitSegments {
			verify(segment.StoragePath, segment.PayloadSize, segment.SHA256, "canonical_payload_unavailable", "canonical_payload_mismatch")
		}
	}
	return changed, walkErr
}

func (s *Store) reconcileManifestSidecar(recording *domain.Recording, sidecarRel, payloadRel string, knownPaths *map[string]struct{}, changed *bool) {
	issue := func(code, message string) {
		s.addRecoveryIssue(RecoveryIssue{ID: recording.ID, Code: code, Message: message})
	}
	data, err := os.ReadFile(filepath.Join(s.recordingDir(recording.ID), filepath.FromSlash(sidecarRel)))
	if err != nil {
		issue("manifest_sidecar_unavailable", "manifest metadata sidecar could not be read")
		return
	}
	var snapshot domain.ManifestSnapshot
	if err = json.Unmarshal(data, &snapshot); err != nil || snapshot.TrackID == "" || snapshot.StoragePath != payloadRel || snapshot.Size < 0 || len(snapshot.SHA256) != sha256.Size*2 {
		issue("manifest_sidecar_invalid", "manifest metadata sidecar is invalid")
		return
	}
	if _, ok := recording.Tracks[snapshot.TrackID]; !ok {
		issue("manifest_track_unknown", "manifest metadata refers to an undeclared track")
		return
	}
	digestBytes, err := hex.DecodeString(snapshot.SHA256)
	if err != nil || len(digestBytes) != sha256.Size {
		issue("manifest_sidecar_invalid", "manifest metadata sidecar has an invalid integrity digest")
		return
	}
	if _, err = s.safePath(recording.ID, payloadRel); err != nil {
		issue("manifest_path_invalid", "manifest storage path is invalid")
		return
	}
	file, err := s.OpenPayload(recording.ID, payloadRel)
	if err != nil {
		issue("manifest_payload_unavailable", "manifest payload for committed metadata is unavailable")
		return
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || size != snapshot.Size || !bytes.Equal(hash.Sum(nil), digestBytes) {
		issue("manifest_payload_mismatch", "manifest payload failed size or integrity verification")
		return
	}
	for _, existing := range recording.Snapshots {
		if existing.StoragePath == payloadRel {
			if existing.SHA256 != snapshot.SHA256 || existing.Size != snapshot.Size || existing.TrackID != snapshot.TrackID {
				issue("manifest_sidecar_conflict", "manifest metadata conflicts with the canonical recording document")
			}
			return
		}
	}
	if _, found := (*knownPaths)[payloadRel]; found {
		issue("manifest_sidecar_conflict", "manifest storage path is already assigned in the canonical recording document")
		return
	}
	recording.Snapshots = append(recording.Snapshots, snapshot)
	(*knownPaths)[payloadRel] = struct{}{}
	*changed = true
}

func (s *Store) reconcileSidecar(recording *domain.Recording, sidecarRel, payloadRel string, knownPaths *map[string]struct{}, changed *bool) {
	issue := func(code, message string) {
		s.addRecoveryIssue(RecoveryIssue{ID: recording.ID, Code: code, Message: message})
	}
	data, err := os.ReadFile(filepath.Join(s.recordingDir(recording.ID), filepath.FromSlash(sidecarRel)))
	if err != nil {
		issue("sidecar_unavailable", "segment metadata sidecar could not be read")
		return
	}
	var segment domain.Segment
	if err = json.Unmarshal(data, &segment); err != nil {
		issue("sidecar_invalid", "segment metadata sidecar is invalid")
		return
	}
	if segment.ID == "" || segment.TrackID == "" || segment.StoragePath != payloadRel || segment.PayloadSize < 0 || len(segment.SHA256) != sha256.Size*2 {
		issue("sidecar_invalid", "segment metadata sidecar does not describe its stored payload")
		return
	}
	digestBytes, err := hex.DecodeString(segment.SHA256)
	if err != nil || len(digestBytes) != sha256.Size {
		issue("sidecar_invalid", "segment metadata sidecar has an invalid integrity digest")
		return
	}
	track, ok := recording.Tracks[segment.TrackID]
	if !ok || track == nil || track.ID != segment.TrackID {
		issue("sidecar_track_unknown", "segment metadata refers to an undeclared track")
		return
	}
	if _, err = s.safePath(recording.ID, payloadRel); err != nil {
		issue("sidecar_path_invalid", "segment metadata storage path is invalid")
		return
	}
	file, err := s.OpenPayload(recording.ID, payloadRel)
	if err != nil {
		issue("sidecar_payload_unavailable", "segment payload for committed metadata is unavailable")
		return
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || size != segment.PayloadSize || !bytes.Equal(hash.Sum(nil), digestBytes) {
		issue("sidecar_payload_mismatch", "segment payload failed size or integrity verification")
		return
	}

	if existing, found := findSegment(track, segment.ID, payloadRel); found {
		if existing.StoragePath != segment.StoragePath || existing.SHA256 != segment.SHA256 || existing.PayloadSize != segment.PayloadSize || existing.IsInit != segment.IsInit {
			issue("sidecar_conflict", "segment metadata conflicts with the canonical recording document")
		}
		return
	}
	if _, found := (*knownPaths)[payloadRel]; found {
		issue("sidecar_conflict", "segment storage path is already assigned in the canonical recording document")
		return
	}
	segment.StoragePath = payloadRel
	if segment.IsInit {
		track.InitSegments = append(track.InitSegments, segment)
	} else {
		track.Segments = append(track.Segments, segment)
	}
	(*knownPaths)[payloadRel] = struct{}{}
	*changed = true
}

func findSegment(track *domain.Track, id, storagePath string) (domain.Segment, bool) {
	for _, segment := range track.Segments {
		if segment.ID == id || segment.StoragePath == storagePath {
			return segment, true
		}
	}
	for _, segment := range track.InitSegments {
		if segment.ID == id || segment.StoragePath == storagePath {
			return segment, true
		}
	}
	return domain.Segment{}, false
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
	if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return PayloadResult{}, err
	}
	if err = os.Chmod(filepath.Dir(destination), 0700); err != nil {
		return PayloadResult{}, err
	}
	if err = s.syncRecordingDirectories(id, filepath.Dir(destination)); err != nil {
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
		copyErr = fmt.Errorf("%w: got %d bytes; expected %d", ErrPayloadSizeMismatch, n, expectedSize)
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
	if err = syncDirectory(filepath.Dir(destination)); err != nil {
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
	return atomicWrite(path+".json", append(data, '\n'), 0600)
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
	snapshot := domain.ManifestSnapshot{TrackID: trackID, SourceURI: sourceURI, StoragePath: name, FetchedAt: at, SHA256: digest, Size: int64(len(data))}
	if err := s.SaveSidecar(id, name, snapshot); err != nil {
		return domain.ManifestSnapshot{}, err
	}
	return snapshot, nil
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
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
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
	return syncDirectory(filepath.Dir(path))
}

func (s *Store) syncRecordingDirectories(id, leaf string) error {
	root := s.recordingDir(id)
	for dir := leaf; ; dir = filepath.Dir(dir) {
		if !within(root, dir) {
			return fmt.Errorf("storage directory escapes recording")
		}
		if err := syncDirectory(dir); err != nil {
			return err
		}
		if dir == root {
			return nil
		}
	}
}

// syncDirectory makes rename and directory-entry updates durable where the
// platform/filesystem supports syncing directory descriptors. Windows does
// not expose this through os.File.Sync; POSIX EINVAL is the common unsupported
// fallback. Other I/O failures are returned to the caller.
func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		if errors.Is(syncErr, syscall.EINVAL) || errors.Is(syncErr, syscall.ENOTSUP) {
			return closeErr
		}
		return syncErr
	}
	return closeErr
}

func tightenRecordingTree(root string) error {
	if err := os.Chmod(root, 0700); err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.Chmod(path, 0700)
		}
		if info.Mode().IsRegular() {
			return os.Chmod(path, 0600)
		}
		return nil
	})
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
	sortSegments := func(segments []domain.Segment) {
		sort.SliceStable(segments, func(i, j int) bool {
			a, b := segments[i], segments[j]
			if a.ArchiveOrdinal != 0 || b.ArchiveOrdinal != 0 {
				if a.ArchiveOrdinal == 0 {
					return false
				}
				if b.ArchiveOrdinal == 0 {
					return true
				}
				return a.ArchiveOrdinal < b.ArchiveOrdinal
			}
			if a.SourceEpoch != b.SourceEpoch {
				return a.SourceEpoch < b.SourceEpoch
			}
			return a.Sequence < b.Sequence
		})
	}
	sortSegments(track.Segments)
	sortSegments(track.InitSegments)
}

var ErrNotFound = errors.New("recording not found")
