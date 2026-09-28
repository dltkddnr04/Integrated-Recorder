// Package preview builds disposable, segment-based frame projections from the
// canonical recording archive. It is deliberately independent of acquisition.
package preview

import (
	"errors"
	"time"
)

type Mode string

const (
	ModeDisabled Mode = "disabled"
	ModeSegment  Mode = "segment"
)

type State string

const (
	StateDisabled    State = "disabled"
	StateUnavailable State = "unavailable"
	StateQueued      State = "queued"
	StateProcessing  State = "processing"
	StatePartial     State = "partial"
	StateReady       State = "ready"
	StateFailed      State = "failed"
)

const (
	ProfileVersion     = 1
	AlgorithmVersion   = 1
	maxFrameBytes      = 512 << 10
	maxIndexBytes      = 64 << 20
	maxPreviewLimit    = 100
	maxPrevious        = 3
	maxContextDuration = 30.0
	maxContextBytes    = int64(64 << 20)
)

var (
	ErrInvalid       = errors.New("invalid preview request")
	ErrNotFound      = errors.New("preview frame not found")
	ErrUnavailable   = errors.New("preview generator unavailable")
	ErrActive        = errors.New("recording is active")
	ErrInvalidSample = errors.New("invalid preview sampling request")
)

// Policy is product-management state; it is never stored in recording.json.
type Policy struct {
	Version int  `json:"version"`
	Mode    Mode `json:"mode"`
}

type Profile struct {
	Format           string `json:"format"`
	MaxWidth         int    `json:"max_width"`
	MaxHeight        int    `json:"max_height"`
	AlgorithmVersion int    `json:"algorithm_version"`
}

// Frame is one immutable JPEG projection associated with a canonical segment.
// No source URI or local path is exposed.
type Frame struct {
	ArchiveOrdinal         uint64    `json:"archive_ordinal"`
	TrackID                string    `json:"track_id"`
	SourceEpoch            uint64    `json:"source_epoch"`
	Sequence               uint64    `json:"sequence"`
	SegmentStartSeconds    float64   `json:"segment_start_seconds"`
	SegmentDurationSeconds float64   `json:"segment_duration_seconds"`
	FrameTimeSeconds       float64   `json:"frame_time_seconds"`
	SegmentSHA256          string    `json:"segment_sha256"`
	Width                  int       `json:"width"`
	Height                 int       `json:"height"`
	Size                   int64     `json:"size"`
	GeneratedAt            time.Time `json:"generated_at"`
	ImageSHA256            string    `json:"image_sha256"`
	imageData              []byte    `json:"-"`
}

type Failure struct {
	ArchiveOrdinal uint64    `json:"archive_ordinal"`
	Code           string    `json:"code"`
	Attempts       int       `json:"attempts"`
	Permanent      bool      `json:"permanent"`
	RetryAfter     time.Time `json:"retry_after,omitempty"`
}

type Index struct {
	Version   int       `json:"version"`
	Profile   Profile   `json:"profile"`
	Items     []Frame   `json:"items"`
	Failures  []Failure `json:"failures,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Summary struct {
	Mode                 Mode       `json:"mode"`
	State                State      `json:"state"`
	Available            bool       `json:"available"`
	FrameCount           int        `json:"frame_count"`
	ImageArchiveOrdinal  *uint64    `json:"image_archive_ordinal,omitempty"`
	LatestArchiveOrdinal *uint64    `json:"latest_archive_ordinal,omitempty"`
	UpdatedAt            *time.Time `json:"updated_at,omitempty"`
}

type Response struct {
	RecordingID string  `json:"recording_id"`
	Mode        Mode    `json:"mode"`
	State       State   `json:"state"`
	Available   bool    `json:"available"`
	FrameCount  int     `json:"frame_count"`
	Items       []Frame `json:"items"`
}

type Sampling string

const (
	SamplingUniform Sampling = "uniform"
	SamplingRecent  Sampling = "recent"
	SamplingNearest Sampling = "nearest"
)

func defaultProfile() Profile {
	return Profile{Format: "jpeg", MaxWidth: 480, MaxHeight: 270, AlgorithmVersion: AlgorithmVersion}
}
