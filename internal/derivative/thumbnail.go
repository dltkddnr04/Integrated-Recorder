package derivative

import (
	"errors"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	maxThumbnailBytes  int64 = 8 << 20
	maxThumbnailPixels int64 = 4 << 20
)

var (
	ErrThumbnailNotFound = errors.New("thumbnail not found")
	// ErrThumbnailFailed is retained for the deprecated HTTP compatibility
	// endpoint's response mapping. New thumbnail generation uses Preview Frame
	// Index and does not return this error from derivative.Service.
	ErrThumbnailFailed = errors.New("thumbnail generation failed")
)

// Thumbnail is a safe summary of a legacy JPEG projection. New preview assets
// are served by the Preview Frame Index service. This type remains for the
// existing thumbnail HTTP endpoint's read compatibility.
type Thumbnail struct {
	RecordingID string    `json:"recording_id"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// OpenThumbnail opens a legacy JPEG projection for a validated recording ID.
// New projections are served by Preview Frame Index; this method only reads
// old files from <data-root>/thumbnails. Missing, unsafe, or invalid files are
// deliberately indistinguishable to callers.
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
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) || openedInfo.Size() != info.Size() || !validJPEG(file, openedInfo.Size()) {
		_ = file.Close()
		return nil, Thumbnail{}, ErrThumbnailNotFound
	}
	return file, Thumbnail{RecordingID: recordingID, ContentType: "image/jpeg", Size: openedInfo.Size(), UpdatedAt: openedInfo.ModTime().UTC()}, nil
}

func validJPEG(file *os.File, size int64) bool {
	if file == nil || size <= 0 || size > maxThumbnailBytes {
		return false
	}
	config, err := jpeg.DecodeConfig(io.LimitReader(file, maxThumbnailBytes+1))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return false
	}
	width, height := int64(config.Width), int64(config.Height)
	if width > maxThumbnailPixels || height > maxThumbnailPixels || width*height > maxThumbnailPixels {
		return false
	}
	if _, err := file.Seek(0, 0); err != nil {
		return false
	}
	if _, err := jpeg.Decode(io.LimitReader(file, maxThumbnailBytes+1)); err != nil {
		return false
	}
	_, err = file.Seek(0, 0)
	return err == nil
}
