package acquire

import (
	"context"
	"net/http"
	"testing"

	"github.com/dltkddnr04/integrated-recorder/internal/domain"
	"github.com/dltkddnr04/integrated-recorder/internal/hls"
	"github.com/dltkddnr04/integrated-recorder/internal/platform/owncast"
	"github.com/dltkddnr04/integrated-recorder/internal/storage"
)

func TestObservePlaylistRecordsWindowAndManifestGaps(t *testing.T) {
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, &http.Client{}, owncast.Resolver{}, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	const id = "abcdef0123456789abcdef0123456789"
	if err = store.NewRecordingDir(id); err != nil {
		t.Fatal(err)
	}
	recording := &domain.Recording{ID: id, State: domain.StateRecording, Tracks: map[string]*domain.Track{"main": {ID: "main", HasLastObservedSequence: true, LastObservedSequence: 9, Segments: []domain.Segment{{ID: "old", TrackID: "main", Sequence: 9}}}}}
	if err = store.SaveRecording(recording); err != nil {
		t.Fatal(err)
	}
	e := &entry{recording: recording}
	playlist := hls.MediaPlaylist{TargetDuration: 1, Segments: []hls.MediaSegment{{Sequence: 11}, {Sequence: 13}}}
	if err = manager.observePlaylist(e, playlist); err != nil {
		t.Fatal(err)
	}
	if len(recording.Gaps) != 2 || recording.Gaps[0].FromSequence != 10 || recording.Gaps[0].ToSequence != 10 || recording.Gaps[1].FromSequence != 12 {
		t.Fatalf("gaps = %#v", recording.Gaps)
	}

	zeroID := "1234567890abcdef1234567890abcdef"
	if err = store.NewRecordingDir(zeroID); err != nil {
		t.Fatal(err)
	}
	zeroRecording := &domain.Recording{ID: zeroID, State: domain.StateRecording, Tracks: map[string]*domain.Track{"main": {ID: "main", HasLastObservedSequence: true, LastObservedSequence: 0, Segments: []domain.Segment{{ID: "zero", TrackID: "main", Sequence: 0}}}}}
	if err = store.SaveRecording(zeroRecording); err != nil {
		t.Fatal(err)
	}
	zeroEntry := &entry{recording: zeroRecording}
	if err = manager.observePlaylist(zeroEntry, hls.MediaPlaylist{TargetDuration: 1, Segments: []hls.MediaSegment{{Sequence: 2}}}); err != nil {
		t.Fatal(err)
	}
	if len(zeroRecording.Gaps) != 1 || zeroRecording.Gaps[0].FromSequence != 1 || zeroRecording.Gaps[0].ToSequence != 1 {
		t.Fatalf("sequence zero gap = %#v", zeroRecording.Gaps)
	}
}

func TestProcessRecordsExplicitManifestGapWithoutFetchingIt(t *testing.T) {
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, &http.Client{}, owncast.Resolver{}, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	const id = "0987654321abcdef0987654321abcdef"
	if err = store.NewRecordingDir(id); err != nil {
		t.Fatal(err)
	}
	recording := &domain.Recording{ID: id, State: domain.StateRecording, Tracks: map[string]*domain.Track{"main": {ID: "main", Segments: []domain.Segment{}}}}
	if err = store.SaveRecording(recording); err != nil {
		t.Fatal(err)
	}
	e := &entry{recording: recording}
	done, err := manager.process(e, context.Background(), hls.MediaPlaylist{TargetDuration: 1, Segments: []hls.MediaSegment{{Sequence: 7, URI: "http://example.invalid/missing.ts", Duration: 1, Gap: true}}})
	if err != nil || done {
		t.Fatalf("process() = done %v, err %v", done, err)
	}
	if len(recording.Tracks["main"].Segments) != 0 || len(recording.Gaps) != 1 || recording.Gaps[0].FromSequence != 7 || recording.Gaps[0].ToSequence != 7 {
		t.Fatalf("manifest gap was not recorded without capture: %#v", recording)
	}
}
