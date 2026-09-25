package acquire_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/acquire"
	"github.com/dltkddnr04/integrated-recorder/internal/domain"
	"github.com/dltkddnr04/integrated-recorder/internal/platform/owncast"
	"github.com/dltkddnr04/integrated-recorder/internal/server"
	"github.com/dltkddnr04/integrated-recorder/internal/storage"
)

func TestLocalHTTPAcquireStopReloadAndVOD(t *testing.T) {
	initBytes := []byte{0, 1, 2, 3, 4, 5, 6, 7}
	blobBytes := []byte{0xff, 0x00, 0x10, 0x20, 0x30, 0x40, 0x50}
	var mu sync.Mutex
	mediaRequests := 0
	segmentRequests := map[string]int{}
	secondCaptured := make(chan struct{})
	thirdMedia := make(chan struct{})
	var secondOnce, thirdOnce sync.Once

	source := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hls/stream.m3u8":
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=900000\nrendition.m3u8\n")
		case "/hls/rendition.m3u8":
			mu.Lock()
			mediaRequests++
			requestNum := mediaRequests
			mu.Unlock()
			if requestNum >= 3 {
				thirdOnce.Do(func() { close(thirdMedia) })
			}
			playlist := "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1.25,\n#EXT-X-BYTERANGE:4@0\nblob.m4s\n"
			if requestNum >= 2 {
				playlist += "#EXTINF:1.5,\n#EXT-X-BYTERANGE:3@4\nblob.m4s\n"
			}
			_, _ = io.WriteString(w, playlist)
		case "/hls/init.mp4":
			_, _ = w.Write(initBytes)
		case "/hls/blob.m4s":
			rangeValue := r.Header.Get("Range")
			var start, end int
			if _, err := fmt.Sscanf(rangeValue, "bytes=%d-%d", &start, &end); err != nil || start < 0 || end >= len(blobBytes) || end < start {
				http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			mu.Lock()
			segmentRequests[rangeValue]++
			mu.Unlock()
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(blobBytes)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(blobBytes[start : end+1])
			if start == 4 {
				secondOnce.Do(func() { close(secondCaptured) })
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer source.Close()

	dataDir := t.TempDir()
	store, err := storage.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	validate := func(context.Context, string) error { return nil }
	manager, err := acquire.NewManager(store, source.Client(), owncast.Resolver{}, validate)
	if err != nil {
		t.Fatal(err)
	}
	recording, err := manager.Start(context.Background(), source.URL, "local fixture")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondCaptured:
	case <-time.After(5 * time.Second):
		t.Fatal("second original byte range was not captured")
	}
	select {
	case <-thirdMedia:
	case <-time.After(5 * time.Second):
		t.Fatal("manifest was not polled repeatedly")
	}
	time.Sleep(100 * time.Millisecond)
	stopped, err := manager.Stop(recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != domain.StateStopped {
		t.Fatalf("state = %q", stopped.State)
	}
	mu.Lock()
	gotMediaRequests := mediaRequests
	firstCount, secondCount := segmentRequests["bytes=0-3"], segmentRequests["bytes=4-6"]
	mu.Unlock()
	if gotMediaRequests < 3 {
		t.Fatalf("media polls = %d, want repeated polls", gotMediaRequests)
	}
	if firstCount != 1 || secondCount != 1 {
		t.Fatalf("range fetch counts = %d, %d; duplicate suppression failed", firstCount, secondCount)
	}
	track := stopped.Tracks["main"]
	if len(track.Segments) != 2 || track.Segments[0].Sequence != 10 || track.Segments[1].Sequence != 11 {
		t.Fatalf("stored segments = %#v", track.Segments)
	}
	if track.Segments[0].InitSegmentID == "" || len(track.InitSegments) != 1 {
		t.Fatalf("init segment was not linked: %#v", track)
	}
	for i, expected := range [][]byte{blobBytes[0:4], blobBytes[4:7]} {
		segment := track.Segments[i]
		stored, readErr := os.ReadFile(filepath.Join(store.Root(), "recordings", recording.ID, filepath.FromSlash(segment.StoragePath)))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(stored) != string(expected) {
			t.Fatalf("segment %d bytes = %v, want %v", i, stored, expected)
		}
		hash := sha256.Sum256(expected)
		if segment.SHA256 != fmt.Sprintf("%x", hash[:]) || segment.PayloadSize != int64(len(expected)) {
			t.Fatalf("segment integrity metadata incorrect: %#v", segment)
		}
	}

	// A new manager simulates a full process restart and reads only directory metadata.
	reloaded, err := acquire.NewManager(store, source.Client(), owncast.Resolver{}, validate)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := reloaded.Get(recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	if previous.State != domain.StateStopped || previous.SegmentCount() != 2 {
		t.Fatalf("reloaded recording %#v", previous)
	}
	api := newIPv4TestServer(t, server.New(reloaded))
	defer api.Close()
	response, err := api.Client().Get(api.URL + "/api/recordings/" + recording.ID + "/play/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	master, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(master), "playlist.m3u8") {
		t.Fatalf("master status/body = %d %s", response.StatusCode, master)
	}
	response, err = api.Client().Get(api.URL + "/api/recordings/" + recording.ID + "/play/tracks/main/playlist.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	playlistBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	playlist := string(playlistBody)
	if response.StatusCode != http.StatusOK || !strings.Contains(playlist, "#EXT-X-PLAYLIST-TYPE:VOD") || !strings.Contains(playlist, "#EXT-X-MAP:") || !strings.Contains(playlist, "#EXT-X-ENDLIST") || strings.Index(playlist, "seg-00000000000000000010") > strings.Index(playlist, "seg-00000000000000000011") {
		t.Fatalf("VOD playlist invalid:\n%s", playlist)
	}
	response, err = api.Client().Get(api.URL + "/api/recordings/" + recording.ID + "/play/segments/seg-00000000000000000010")
	if err != nil {
		t.Fatal(err)
	}
	played, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(played) != string(blobBytes[:4]) {
		t.Fatalf("served media bytes = %v, status %d", played, response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); got != "video/iso.segment" {
		t.Fatalf(".m4s Content-Type = %q", got)
	}
	if strings.Contains(playlist, "#EXT-X-DISCONTINUITY") {
		t.Fatalf("unexpected discontinuity: %s", playlist)
	}
}

func newIPv4TestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(handler)
	s.Listener = listener
	s.Start()
	return s
}
