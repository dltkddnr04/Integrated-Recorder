package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/integrated-recorder/internal/domain"
)

func TestLoadAllMarksStaleRecordingInterrupted(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	const id = "0123456789abcdef0123456789abcdef"
	if err = store.NewRecordingDir(id); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Minute)
	recording := &domain.Recording{FormatVersion: 1, ID: id, SourceURL: "https://owncast.example", State: domain.StateRecording, CreatedAt: started, StartedAt: started, Tracks: map[string]*domain.Track{"main": {ID: "main", PendingSequences: []uint64{3}, Segments: []domain.Segment{{ID: "s2", Sequence: 2}, {ID: "s1", Sequence: 1}}}}}
	if err = store.SaveRecording(recording); err != nil {
		t.Fatal(err)
	}
	recordings, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recordings) != 1 || recordings[0].State != domain.StateInterrupted {
		t.Fatalf("loaded recordings %#v", recordings)
	}
	if recordings[0].Tracks["main"].Segments[0].Sequence != 1 {
		t.Fatal("segments were not ordered on reload")
	}
	if recordings[0].StoppedAt == nil {
		t.Fatal("stopped_at was not set")
	}
	if len(recordings[0].Gaps) != 1 || recordings[0].Gaps[0].FromSequence != 3 || len(recordings[0].Tracks["main"].PendingSequences) != 0 {
		t.Fatalf("pending segment was not converted to gap: %#v", recordings[0])
	}
	if _, err = os.Stat(filepath.Join(dir, "recordings", id, "recording.json")); err != nil {
		t.Fatal(err)
	}
}

func TestSavePayloadExactDoesNotPublishWrongLength(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "fedcba0987654321fedcba0987654321"
	if err = store.NewRecordingDir(id); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SavePayloadExact(id, "tracks/main/range.m4s", bytes.NewReader([]byte{1, 2, 3}), 100, 4); err == nil {
		t.Fatal("short payload was accepted")
	}
	if _, err = store.OpenPayload(id, "tracks/main/range.m4s"); err == nil {
		t.Fatal("wrong-length payload was published")
	}
}
