package domain

import "time"

type RecordingState string

const (
	StateRecording   RecordingState = "recording"
	StateStopped     RecordingState = "stopped"
	StateCompleted   RecordingState = "completed"
	StateInterrupted RecordingState = "interrupted"
)

// Recording is the self-describing root document for one captured stream.
type Recording struct {
	FormatVersion           int                `json:"format_version"`
	ID                      string             `json:"id"`
	Title                   string             `json:"title,omitempty"`
	AdapterID               string             `json:"adapter_id,omitempty"`
	Adapter                 *AdapterProvenance `json:"adapter,omitempty"`
	Resource                *ResourceReference `json:"resource,omitempty"`
	SourceURIClassification string             `json:"source_uri_classification,omitempty"`
	// SourceURL is retained only to read and report pre-adapter recordings.
	SourceURL string             `json:"source_url,omitempty"`
	State     RecordingState     `json:"state"`
	CreatedAt time.Time          `json:"created_at"`
	StartedAt time.Time          `json:"started_at"`
	StoppedAt *time.Time         `json:"stopped_at,omitempty"`
	Tracks    map[string]*Track  `json:"tracks"`
	Gaps      []Gap              `json:"gaps,omitempty"`
	Snapshots []ManifestSnapshot `json:"manifest_snapshots,omitempty"`
	LastError string             `json:"last_error,omitempty"`
}

type Track struct {
	ID                      string `json:"id"`
	SourcePlaylistURL       string `json:"source_playlist_url"`
	Bandwidth               int64  `json:"bandwidth,omitempty"`
	SourceEpoch             uint64 `json:"source_epoch,omitempty"`
	NextArchiveOrdinal      uint64 `json:"next_archive_ordinal,omitempty"`
	HasLastObservedSequence bool   `json:"has_last_observed_sequence,omitempty"`
	LastObservedSequence    uint64 `json:"last_observed_sequence,omitempty"`
	// PendingSequences is retained for legacy recordings where source epoch was
	// implicitly zero. Newer recordings use PendingSegments.
	PendingSequences []uint64          `json:"pending_sequences,omitempty"`
	PendingSegments  []PendingSequence `json:"pending_segments,omitempty"`
	InitSegments     []Segment         `json:"init_segments,omitempty"`
	Segments         []Segment         `json:"segments"`
}

type PendingSequence struct {
	SourceEpoch uint64 `json:"source_epoch,omitempty"`
	Sequence    uint64 `json:"sequence"`
}

// ResourceReference is the stable archive representation of an opaque
// adapter-defined resource reference. It deliberately mirrors the existing
// protocol JSON shape without depending on protocol wire types.
type ResourceReference struct {
	Type   string             `json:"resource_type"`
	ID     string             `json:"resource_id"`
	Parent *ResourceReference `json:"parent,omitempty"`
}

// AdapterProvenance records which adapter implementation resolved a source.
// The JSON tags retain the original recording format representation.
type AdapterProvenance struct {
	ID              string `json:"id"`
	Name            string `json:"name,omitempty"`
	Version         string `json:"version"`
	ProtocolVersion int    `json:"protocol_version"`
	Fingerprint     string `json:"descriptor_fingerprint,omitempty"`
}

// Segment describes an original source object. Payload bytes are never decoded
// or transformed; IsInit distinguishes an HLS initialization section.
type Segment struct {
	ID                    string     `json:"id"`
	TrackID               string     `json:"track_id"`
	Sequence              uint64     `json:"sequence"`
	SourceEpoch           uint64     `json:"source_epoch,omitempty"`
	DiscontinuitySequence uint64     `json:"discontinuity_sequence,omitempty"`
	ArchiveOrdinal        uint64     `json:"archive_ordinal,omitempty"`
	SourceURI             string     `json:"source_uri"`
	Duration              float64    `json:"duration,omitempty"`
	ProgramDateTime       *time.Time `json:"program_date_time,omitempty"`
	InitSegmentID         string     `json:"init_segment_id,omitempty"`
	ByteRange             *ByteRange `json:"byte_range,omitempty"`
	Discontinuity         bool       `json:"discontinuity,omitempty"`
	StoragePath           string     `json:"storage_path"`
	PayloadSize           int64      `json:"payload_size"`
	SHA256                string     `json:"sha256"`
	IsInit                bool       `json:"is_init,omitempty"`
}

type ByteRange struct {
	Length uint64 `json:"length"`
	Offset uint64 `json:"offset"`
}

type Gap struct {
	TrackID      string    `json:"track_id"`
	SourceEpoch  uint64    `json:"source_epoch,omitempty"`
	FromSequence uint64    `json:"from_sequence"`
	ToSequence   uint64    `json:"to_sequence"`
	DetectedAt   time.Time `json:"detected_at"`
	Reason       string    `json:"reason"`
}

type ManifestSnapshot struct {
	TrackID     string    `json:"track_id"`
	SourceURI   string    `json:"source_uri"`
	StoragePath string    `json:"storage_path"`
	FetchedAt   time.Time `json:"fetched_at"`
	SHA256      string    `json:"sha256"`
	Size        int64     `json:"size"`
}

func (r *Recording) SegmentCount() int {
	count := 0
	for _, track := range r.Tracks {
		count += len(track.Segments)
	}
	return count
}

func (r *Recording) Duration() float64 {
	var duration float64
	for _, track := range r.Tracks {
		for _, segment := range track.Segments {
			duration += segment.Duration
		}
	}
	return duration
}
