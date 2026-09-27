package derivative

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/domain"
)

const maxThumbnailBytes int64 = 8 << 20

var (
	ErrThumbnailNotFound = errors.New("thumbnail not found")
	ErrThumbnailFailed   = errors.New("thumbnail generation failed")
)

// Thumbnail is a safe summary of a generated JPEG projection. It contains no
// local path and is not part of canonical recording metadata.
type Thumbnail struct {
	RecordingID string    `json:"recording_id"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type thumbnailFlight struct {
	done   chan struct{}
	result Thumbnail
	err    error
}

// GenerateThumbnail extracts the first video frame from a local, verified
// projection of canonical segment bytes. Concurrent calls for the same
// recording share one generation; no source URL is fetched.
func (s *Service) GenerateThumbnail(ctx context.Context, recording *domain.Recording) (Thumbnail, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Thumbnail{}, err
	}
	if !s.Available() {
		return Thumbnail{}, ErrUnavailable
	}
	if recording == nil || !recordingIDPattern.MatchString(recording.ID) {
		return Thumbnail{}, ErrInvalid
	}
	if recording.State == domain.StateRecording {
		return Thumbnail{}, ErrActive
	}
	if recording.State != domain.StateStopped && recording.State != domain.StateCompleted && recording.State != domain.StateInterrupted {
		return Thumbnail{}, ErrUnsupported
	}
	copyRecording, err := cloneRecording(recording)
	if err != nil {
		return Thumbnail{}, ErrInvalid
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Thumbnail{}, ErrUnavailable
	}
	s.thumbnailWG.Add(1)
	s.mu.Unlock()
	defer s.thumbnailWG.Done()

	s.thumbnailMu.Lock()
	if flight := s.thumbnailFlights[copyRecording.ID]; flight != nil {
		s.thumbnailMu.Unlock()
		select {
		case <-ctx.Done():
			return Thumbnail{}, ctx.Err()
		case <-s.ctx.Done():
			return Thumbnail{}, context.Canceled
		case <-flight.done:
			return flight.result, flight.err
		}
	}
	flight := &thumbnailFlight{done: make(chan struct{})}
	s.thumbnailFlights[copyRecording.ID] = flight
	s.thumbnailMu.Unlock()

	result, generateErr := s.generateThumbnail(ctx, copyRecording)
	s.thumbnailMu.Lock()
	flight.result, flight.err = result, generateErr
	delete(s.thumbnailFlights, copyRecording.ID)
	close(flight.done)
	s.thumbnailMu.Unlock()
	return result, generateErr
}

// OpenThumbnail opens the safe, current JPEG projection for a validated
// recording ID. A missing file, symlink, or invalid JPEG is reported as not
// found, never as a filesystem path error.
func (s *Service) OpenThumbnail(recordingID string) (*os.File, Thumbnail, error) {
	if s == nil || !recordingIDPattern.MatchString(recordingID) {
		return nil, Thumbnail{}, ErrThumbnailNotFound
	}
	path := filepath.Join(s.thumbnailDir, recordingID+".jpg")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxThumbnailBytes {
		return nil, Thumbnail{}, ErrThumbnailNotFound
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, Thumbnail{}, ErrThumbnailNotFound
	}
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Size() != info.Size() || !validJPEG(file, openedInfo.Size()) {
		_ = file.Close()
		return nil, Thumbnail{}, ErrThumbnailNotFound
	}
	return file, Thumbnail{RecordingID: recordingID, ContentType: "image/jpeg", Size: openedInfo.Size(), UpdatedAt: openedInfo.ModTime().UTC()}, nil
}

func (s *Service) generateThumbnail(ctx context.Context, recording *domain.Recording) (Thumbnail, error) {
	select {
	case s.thumbnailSlots <- struct{}{}:
		defer func() { <-s.thumbnailSlots }()
	case <-ctx.Done():
		return Thumbnail{}, ctx.Err()
	case <-s.ctx.Done():
		return Thumbnail{}, context.Canceled
	}
	workCtx, cancel := context.WithTimeout(ctx, thumbnailTimeout)
	stopClose := context.AfterFunc(s.ctx, cancel)
	defer func() { stopClose(); cancel() }()

	inputRecording, err := thumbnailInput(recording)
	if err != nil {
		return Thumbnail{}, err
	}
	objects, err := s.sourceObjects(inputRecording)
	if err != nil {
		return Thumbnail{}, fmt.Errorf("%w: canonical source unavailable", ErrUnsupported)
	}
	tempDir, err := os.MkdirTemp(s.thumbnailDir, ".thumbnail-"+recording.ID+"-")
	if err != nil {
		return Thumbnail{}, ErrThumbnailFailed
	}
	if err = os.Chmod(tempDir, 0700); err != nil {
		_ = os.RemoveAll(tempDir)
		return Thumbnail{}, ErrThumbnailFailed
	}
	defer os.RemoveAll(tempDir)
	localNames, err := s.prepareInputs(workCtx, inputRecording, objects, tempDir)
	if err != nil {
		if workCtx.Err() != nil {
			return Thumbnail{}, workCtx.Err()
		}
		return Thumbnail{}, ErrThumbnailFailed
	}
	playlist, err := buildPlaylist(inputRecording, localNames)
	if err != nil {
		return Thumbnail{}, ErrUnsupported
	}
	if err := writePrivateFile(filepath.Join(tempDir, "input.m3u8"), []byte(playlist)); err != nil {
		return Thumbnail{}, ErrThumbnailFailed
	}
	command := exec.CommandContext(workCtx, s.ffmpegPath,
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-protocol_whitelist", "file", "-i", "input.m3u8",
		"-map", "0:v:0", "-frames:v", "1",
		"-vf", "scale=1280:720:force_original_aspect_ratio=decrease:force_divisible_by=2",
		"-an", "-sn", "-dn", "-c:v", "mjpeg", "-f", "image2", "poster.jpg")
	command.Dir = tempDir
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		if workCtx.Err() != nil {
			return Thumbnail{}, workCtx.Err()
		}
		return Thumbnail{}, ErrThumbnailFailed
	}
	if err := workCtx.Err(); err != nil {
		return Thumbnail{}, err
	}
	poster := filepath.Join(tempDir, "poster.jpg")
	info, err := os.Lstat(poster)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxThumbnailBytes {
		return Thumbnail{}, ErrThumbnailFailed
	}
	f, err := os.OpenFile(poster, os.O_RDWR, 0600)
	if err != nil {
		return Thumbnail{}, ErrThumbnailFailed
	}
	if err = f.Chmod(0600); err == nil && !validJPEG(f, info.Size()) {
		err = errors.New("invalid JPEG")
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return Thumbnail{}, ErrThumbnailFailed
	}
	if err := workCtx.Err(); err != nil {
		return Thumbnail{}, err
	}
	destination := filepath.Join(s.thumbnailDir, recording.ID+".jpg")
	if err := os.Rename(poster, destination); err != nil {
		return Thumbnail{}, ErrThumbnailFailed
	}
	if err := syncDir(s.thumbnailDir); err != nil {
		return Thumbnail{}, ErrThumbnailFailed
	}
	finalInfo, err := os.Stat(destination)
	if err != nil || !finalInfo.Mode().IsRegular() || finalInfo.Size() <= 0 || finalInfo.Size() > maxThumbnailBytes {
		return Thumbnail{}, ErrThumbnailFailed
	}
	return Thumbnail{RecordingID: recording.ID, ContentType: "image/jpeg", Size: finalInfo.Size(), UpdatedAt: finalInfo.ModTime().UTC()}, nil
}

func thumbnailInput(recording *domain.Recording) (*domain.Recording, error) {
	clone, err := cloneRecording(recording)
	if err != nil {
		return nil, ErrInvalid
	}
	track, err := primaryTrack(clone)
	if err != nil {
		return nil, err
	}
	first, err := firstArchiveSegment(track.Segments)
	if err != nil {
		return nil, ErrUnsupported
	}
	originalInits := append([]domain.Segment(nil), track.InitSegments...)
	track.InitSegments = nil
	if first.InitSegmentID != "" {
		for _, init := range originalInits {
			if init.ID == first.InitSegmentID {
				track.InitSegments = append(track.InitSegments, init)
			}
		}
		if len(track.InitSegments) == 0 {
			return nil, ErrUnsupported
		}
	}
	track.Segments = []domain.Segment{first}
	key := ""
	keys := make([]string, 0, len(clone.Tracks))
	for name := range clone.Tracks {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if clone.Tracks[name] == track {
			key = name
			break
		}
	}
	if key == "" {
		return nil, ErrUnsupported
	}
	clone.Tracks = map[string]*domain.Track{key: track}
	return clone, nil
}

func firstArchiveSegment(input []domain.Segment) (domain.Segment, error) {
	segments := append([]domain.Segment(nil), input...)
	if len(segments) == 0 {
		return domain.Segment{}, ErrUnsupported
	}
	seen := make(map[uint64]bool, len(segments))
	for _, segment := range segments {
		if segment.ArchiveOrdinal == 0 {
			continue
		}
		if seen[segment.ArchiveOrdinal] {
			return domain.Segment{}, ErrUnsupported
		}
		seen[segment.ArchiveOrdinal] = true
	}
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
	return segments[0], nil
}

func validJPEG(file *os.File, size int64) bool {
	if file == nil || size < 5 || size > maxThumbnailBytes {
		return false
	}
	var start [3]byte
	var end [2]byte
	if _, err := file.ReadAt(start[:], 0); err != nil {
		return false
	}
	if _, err := file.ReadAt(end[:], size-2); err != nil {
		return false
	}
	return start[0] == 0xff && start[1] == 0xd8 && start[2] == 0xff && end[0] == 0xff && end[1] == 0xd9
}
