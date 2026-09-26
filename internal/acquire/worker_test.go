package acquire

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
	"github.com/dltkddnr04/integrated-recorder/internal/domain"
	"github.com/dltkddnr04/integrated-recorder/internal/hls"
	"github.com/dltkddnr04/integrated-recorder/internal/storage"
)

func TestObservePlaylistRecordsWindowAndManifestGaps(t *testing.T) {
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, &http.Client{}, emptyResolver{}, func(context.Context, string) error { return nil })
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
	if len(e.recording.Gaps) != 2 || e.recording.Gaps[0].FromSequence != 10 || e.recording.Gaps[0].ToSequence != 10 || e.recording.Gaps[1].FromSequence != 12 {
		t.Fatalf("gaps = %#v", e.recording.Gaps)
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
	if len(zeroEntry.recording.Gaps) != 1 || zeroEntry.recording.Gaps[0].FromSequence != 1 || zeroEntry.recording.Gaps[0].ToSequence != 1 {
		t.Fatalf("sequence zero gap = %#v", zeroEntry.recording.Gaps)
	}
}

func TestProcessRecordsExplicitManifestGapWithoutFetchingIt(t *testing.T) {
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, &http.Client{}, emptyResolver{}, func(context.Context, string) error { return nil })
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
	if len(e.recording.Tracks["main"].Segments) != 0 || len(e.recording.Gaps) != 1 || e.recording.Gaps[0].FromSequence != 7 || e.recording.Gaps[0].ToSequence != 7 {
		t.Fatalf("manifest gap was not recorded without capture: %#v", e.recording)
	}
}

func TestProcessDoesNotArchiveLaterSequenceAheadOfPendingHead(t *testing.T) {
	var failed atomic.Bool
	failed.Store(true)
	var sequenceTenRequests atomic.Int32
	var sequenceElevenRequests atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/10.ts":
			sequenceTenRequests.Add(1)
			if failed.Load() {
				http.Error(w, "temporary", http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, "segment-ten")
		case "/11.ts":
			sequenceElevenRequests.Add(1)
			_, _ = io.WriteString(w, "segment-eleven")
		default:
			http.NotFound(w, r)
		}
	}))
	defer source.Close()
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err = store.CreateRecording(&domain.Recording{FormatVersion: 1, ID: id, State: domain.StateRecording, Tracks: map[string]*domain.Track{"main": {ID: "main", Segments: []domain.Segment{}}}}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, &http.Client{}, emptyResolver{}, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	e := &entry{recording: &domain.Recording{FormatVersion: 1, ID: id, State: domain.StateRecording, Tracks: map[string]*domain.Track{"main": {ID: "main", Segments: []domain.Segment{}}}}, media: adapterproto.MediaSource{ManifestURL: source.URL + "/manifest"}}
	playlist := hls.MediaPlaylist{TargetDuration: 1, Segments: []hls.MediaSegment{{Sequence: 10, URI: source.URL + "/10.ts", Duration: 1}, {Sequence: 11, URI: source.URL + "/11.ts", Duration: 1}}}
	if done, processErr := manager.process(e, context.Background(), playlist); processErr != nil || done {
		t.Fatalf("first process = done %v, err %v", done, processErr)
	}
	if got := sequenceElevenRequests.Load(); got != 0 {
		t.Fatalf("later sequence was fetched while head was pending (%d requests)", got)
	}
	if len(e.recording.Tracks["main"].Segments) != 0 || len(e.recording.Tracks["main"].PendingSegments) != 1 || e.recording.Tracks["main"].PendingSegments[0].Sequence != 10 {
		t.Fatalf("pending head was not retained: %#v", e.recording.Tracks["main"])
	}
	failed.Store(false)
	if done, processErr := manager.process(e, context.Background(), playlist); processErr != nil || done {
		t.Fatalf("retry process = done %v, err %v", done, processErr)
	}
	segments := e.recording.Tracks["main"].Segments
	if len(segments) != 2 || segments[0].Sequence != 10 || segments[1].Sequence != 11 || segments[0].ArchiveOrdinal >= segments[1].ArchiveOrdinal {
		t.Fatalf("archive order = %#v", segments)
	}
}

func TestCatchUpSequenceResetCreatesStableEpochAndReloadsInArchiveOrder(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "new-source-segment")
	}))
	defer source.Close()
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "cccccccccccccccccccccccccccccccc"
	old := &domain.Recording{FormatVersion: 1, ID: id, State: domain.StateRecording, Tracks: map[string]*domain.Track{"main": {
		ID: "main", SourceEpoch: 0, NextArchiveOrdinal: 3, HasLastObservedSequence: true, LastObservedSequence: 1,
		Segments: []domain.Segment{
			{ID: "old-0", TrackID: "main", Sequence: 0, SourceEpoch: 0, DiscontinuitySequence: 0, ArchiveOrdinal: 1, SourceURI: source.URL + "/old-0.ts"},
			{ID: "old-1", TrackID: "main", Sequence: 1, SourceEpoch: 0, DiscontinuitySequence: 0, ArchiveOrdinal: 2, SourceURI: source.URL + "/old-1.ts"},
		},
	}}}
	if err = store.CreateRecording(old); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, &http.Client{}, emptyResolver{}, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	playlist, err := hls.ParseMedia([]byte(`#EXTM3U
#EXT-X-TARGETDURATION:1
#EXT-X-DISCONTINUITY-SEQUENCE:1
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:1,
new-0.ts
#EXTINF:1,
new-1.ts
#EXTINF:1,
new-2.ts
`), source.URL+"/list.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	e := &entry{recording: old, media: adapterproto.MediaSource{ManifestURL: source.URL + "/list.m3u8"}}
	if err = manager.observePlaylist(e, playlist); err != nil {
		t.Fatal(err)
	}
	if got := e.recording.Tracks["main"].SourceEpoch; got != 1 {
		t.Fatalf("catch-up reset epoch = %d, want 1", got)
	}
	if err = manager.observePlaylist(e, playlist); err != nil {
		t.Fatal(err)
	}
	if got := e.recording.Tracks["main"].SourceEpoch; got != 1 {
		t.Fatalf("repeated playlist changed epoch to %d", got)
	}
	if done, processErr := manager.process(e, context.Background(), playlist); processErr != nil || done {
		t.Fatalf("process reset playlist = done %v, err %v", done, processErr)
	}
	segments := e.recording.Tracks["main"].Segments
	if len(segments) != 5 {
		t.Fatalf("captured segments = %#v", segments)
	}
	for index, want := range []struct{ epoch, sequence, ordinal uint64 }{{0, 0, 1}, {0, 1, 2}, {1, 0, 3}, {1, 1, 4}, {1, 2, 5}} {
		segment := segments[index]
		if segment.SourceEpoch != want.epoch || segment.Sequence != want.sequence || segment.ArchiveOrdinal != want.ordinal {
			t.Fatalf("segment[%d] = %#v, want epoch=%d sequence=%d ordinal=%d", index, segment, want.epoch, want.sequence, want.ordinal)
		}
	}
	loaded, err := store.LoadAll()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("reload = %d recordings, %v", len(loaded), err)
	}
	segments = loaded[0].Tracks["main"].Segments
	if len(segments) != 5 || segments[2].SourceEpoch != 1 || segments[2].Sequence != 0 || segments[4].Sequence != 2 {
		t.Fatalf("reloaded archive order = %#v", segments)
	}
}

func TestObservePlaylistDetectsSameSequenceIdentityChangesButNotSlidingOverlap(t *testing.T) {
	baseTime := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		incoming  hls.MediaSegment
		wantEpoch uint64
	}{
		{
			name:      "source URI changed",
			incoming:  hls.MediaSegment{Sequence: 1, DiscontinuitySequence: 0, URI: "https://edge.example/new.ts", Duration: 1},
			wantEpoch: 1,
		},
		{
			name:      "signed query rotated for same segment",
			incoming:  hls.MediaSegment{Sequence: 1, DiscontinuitySequence: 0, URI: "https://edge.example/old-1.ts?token=rotated", Duration: 1},
			wantEpoch: 0,
		},
		{
			name:      "program date time changed",
			incoming:  hls.MediaSegment{Sequence: 1, DiscontinuitySequence: 0, URI: "https://edge.example/old-1.ts", ProgramTime: timePtr(baseTime.Add(5 * time.Second)), Duration: 1},
			wantEpoch: 1,
		},
		{
			name:      "normal sliding overlap",
			incoming:  hls.MediaSegment{Sequence: 1, DiscontinuitySequence: 0, URI: "https://edge.example/old-1.ts", ProgramTime: timePtr(baseTime), Duration: 1},
			wantEpoch: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := storage.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			const id = "dddddddddddddddddddddddddddddddd"
			old := &domain.Recording{FormatVersion: 1, ID: id, State: domain.StateRecording, Tracks: map[string]*domain.Track{"main": {
				ID: "main", SourceEpoch: 0, HasLastObservedSequence: true, LastObservedSequence: 2,
				Segments: []domain.Segment{
					{ID: "old-1", TrackID: "main", Sequence: 1, SourceEpoch: 0, DiscontinuitySequence: 0, SourceURI: "https://edge.example/old-1.ts", ProgramDateTime: timePtr(baseTime)},
					{ID: "old-2", TrackID: "main", Sequence: 2, SourceEpoch: 0, DiscontinuitySequence: 0, SourceURI: "https://edge.example/old-2.ts", ProgramDateTime: timePtr(baseTime.Add(time.Second))},
				},
			}}}
			if err = store.CreateRecording(old); err != nil {
				t.Fatal(err)
			}
			manager, err := NewManager(store, &http.Client{}, emptyResolver{}, func(context.Context, string) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			incoming := tc.incoming
			if tc.name == "normal sliding overlap" {
				incoming.Sequence = 1
			}
			incomingSegments := []hls.MediaSegment{incoming, {Sequence: 2, DiscontinuitySequence: 0, URI: "https://edge.example/old-2.ts", ProgramTime: timePtr(baseTime.Add(time.Second)), Duration: 1}, {Sequence: 3, DiscontinuitySequence: 0, URI: "https://edge.example/new-3.ts", ProgramTime: timePtr(baseTime.Add(2 * time.Second)), Duration: 1}}
			e := &entry{recording: old}
			if err = manager.observePlaylist(e, hls.MediaPlaylist{TargetDuration: 1, Segments: incomingSegments}); err != nil {
				t.Fatal(err)
			}
			if got := e.recording.Tracks["main"].SourceEpoch; got != tc.wantEpoch {
				t.Fatalf("source epoch=%d want=%d", got, tc.wantEpoch)
			}
		})
	}
}

func TestDownloadObjectRetriesTruncatedResponseBody(t *testing.T) {
	var requests atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Length", "32")
			_, _ = io.WriteString(w, "truncated")
			return
		}
		_, _ = io.WriteString(w, "complete-original-payload")
	}))
	defer source.Close()
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err = store.NewRecordingDir(id); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, &http.Client{}, emptyResolver{}, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("complete-original-payload")
	result, err := manager.downloadObject(context.Background(), source.URL+"/segment.ts", nil, id, "tracks/main/segment.ts", adapterproto.MediaSource{ManifestURL: source.URL + "/manifest"})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() < 2 {
		t.Fatalf("truncated source body was not retried: requests=%d", requests.Load())
	}
	file, err := store.OpenPayload(id, "tracks/main/segment.ts")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(file)
	file.Close()
	if err != nil || !bytes.Equal(got, want) || result.Size != int64(len(want)) {
		t.Fatalf("saved payload = %q (%d), err=%v", got, result.Size, err)
	}
}

type emptyResolver struct{}

func (emptyResolver) Resolve(context.Context, string, json.RawMessage, *adapterproto.ResourceRef) (adapterproto.MediaSource, error) {
	return adapterproto.MediaSource{}, nil
}

func TestMediaHeadersStayWithinSourceOrigin(t *testing.T) {
	const headerName = "X-Generic-Session"
	const headerValue = "opaque-test-value"
	var sourceHeader string
	var cdnHeader string
	var mu sync.Mutex
	cdn := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cdnHeader = r.Header.Get(headerName)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer cdn.Close()
	source := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sourceHeader = r.Header.Get(headerName)
		mu.Unlock()
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, cdn.URL+"/segment", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer source.Close()
	client := &http.Client{}
	response, err := doMediaRequest(client, mustRequest(t, source.URL+"/manifest"), map[string]string{headerName: headerValue}, source.URL+"/manifest", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response, err = doMediaRequest(client, mustRequest(t, source.URL+"/redirect"), map[string]string{headerName: headerValue}, source.URL+"/redirect", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if sourceHeader != headerValue {
		t.Fatalf("same-origin header = %q", sourceHeader)
	}
	if cdnHeader != "" {
		t.Fatal("adapter-provided header was forwarded to a different origin")
	}
}

func TestCompressedHTTPContentEncodingIsRejectedWithoutStoringPayload(t *testing.T) {
	acceptEncoding := make(chan string, 2)
	source := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptEncoding <- r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		_, _ = writer.Write([]byte("compressed representation of media bytes"))
		_ = writer.Close()
	}))
	defer source.Close()

	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const recordingID = "abcdef0123456789abcdef0123456789"
	if err = store.NewRecordingDir(recordingID); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, &http.Client{}, emptyResolver{}, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	manifestURL := source.URL + "/manifest.m3u8"
	if _, err = fetchManifest(context.Background(), manager.client, manifestURL, nil, manifestURL, nil); err == nil {
		t.Fatal("compressed manifest was accepted")
	}
	media := adapterproto.MediaSource{Type: "hls", ManifestURL: manifestURL}
	if _, err = manager.downloadObject(context.Background(), source.URL+"/segment.ts", nil, recordingID, "tracks/main/segment.ts", media); err == nil {
		t.Fatal("compressed media segment was accepted")
	}
	segmentPath := filepath.Join(store.Root(), "recordings", recordingID, "tracks", "main", "segment.ts")
	if _, err = os.Stat(segmentPath); !os.IsNotExist(err) {
		t.Fatalf("rejected encoded response was stored: stat error=%v", err)
	}
	close(acceptEncoding)
	for value := range acceptEncoding {
		if value != "identity" {
			t.Errorf("Accept-Encoding = %q, want explicit identity", value)
		}
	}
}

func mustRequest(t *testing.T, raw string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func newIPv4Server(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	return server
}
