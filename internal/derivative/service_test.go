package derivative

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/domain"
	"github.com/dltkddnr04/integrated-recorder/internal/storage"
)

// TestFakeFFmpegHelper is invoked only by the fake executable generated in
// tests. The production service itself always starts the configured binary
// directly and never invokes a shell.
func TestFakeFFmpegHelper(t *testing.T) {
	mode := os.Getenv("DERIVATIVE_FAKE_FFMPEG")
	if mode == "" {
		return
	}
	args := os.Args
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		os.Exit(41)
	}
	ffmpegArgs := args[separator+1:]
	thumbnail := containsPair(ffmpegArgs, "-f", "image2")
	validArgs := contains(ffmpegArgs, "-protocol_whitelist")
	if thumbnail {
		validArgs = validArgs && containsPair(ffmpegArgs, "-map", "0:v:0") && containsPair(ffmpegArgs, "-frames:v", "1")
	} else {
		validArgs = validArgs && containsPair(ffmpegArgs, "-c", "copy") && containsPair(ffmpegArgs, "-f", "matroska")
	}
	if !validArgs {
		os.Exit(42)
	}
	if mode == "wait" {
		marker := os.Getenv("DERIVATIVE_FAKE_STARTED")
		if marker != "" {
			_ = os.WriteFile(marker, []byte("started"), 0600)
		}
		for {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if mode == "fail" {
		os.Exit(43)
	}
	if len(ffmpegArgs) == 0 {
		os.Exit(44)
	}
	output := ffmpegArgs[len(ffmpegArgs)-1]
	contents := []byte("matroska-fixture")
	if thumbnail {
		contents = []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x04, 0x00, 0x00, 0xff, 0xd9}
	}
	if err := os.WriteFile(output, contents, 0600); err != nil {
		os.Exit(45)
	}
	os.Exit(0)
}

func TestExportHappyPathIsProjectionAndDownloadable(t *testing.T) {
	root, store, recording := fixtureRecording(t)
	ffmpeg := fakeFFmpeg(t)
	t.Setenv("DERIVATIVE_FAKE_FFMPEG", "success")
	service := openService(t, root, store, ffmpeg, 1)
	job, err := service.Start(context.Background(), recording)
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	if job.State != StateCompleted || job.Size != int64(len("matroska-fixture")) || job.OutputName != "recording-"+recording.ID+".mkv" {
		t.Fatalf("unexpected completed job: %+v", job)
	}
	f, downloaded, err := service.OpenDownload(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(f)
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || string(data) != "matroska-fixture" || downloaded.ID != job.ID {
		t.Fatalf("download failed: %q %v %v", data, readErr, closeErr)
	}

	loaded, err := store.LoadAll()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load archive: count=%d err=%v", len(loaded), err)
	}
	payload, err := store.OpenPayload(recording.ID, loaded[0].Tracks["main"].Segments[0].StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := io.ReadAll(payload)
	_ = payload.Close()
	if err != nil || string(canonical) != "original-segment-bytes" {
		t.Fatalf("canonical payload changed: %q err=%v", canonical, err)
	}
}

func TestRealFFmpegRemuxWhenAvailable(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	fixture := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=black:s=64x64:r=10:d=1", "-an", "-threads", "1", "-c:v", "mpeg2video", "-f", "mpegts", "pipe:1")
	input, err := fixture.Output()
	if err != nil || len(input) == 0 {
		t.Skip("ffmpeg lacks a minimal MPEG-TS fixture encoder")
	}
	root, store, recording := fixtureRecordingWithData(t, input)
	service := openService(t, root, store, ffmpeg, 1)
	job, err := service.Start(context.Background(), recording)
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	if job.State != StateCompleted {
		t.Fatalf("real remux failed: %+v", job)
	}
	file, _, err := service.OpenDownload(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(file, magic); err != nil || string(magic) != string([]byte{0x1a, 0x45, 0xdf, 0xa3}) {
		t.Fatalf("output is not Matroska: %x err=%v", magic, err)
	}
}

func TestRealFFmpegThumbnailWhenAvailable(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	fixture := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=black:s=64x64:r=10:d=1", "-an", "-threads", "1", "-c:v", "mpeg2video", "-f", "mpegts", "pipe:1")
	input, err := fixture.Output()
	if err != nil || len(input) == 0 {
		t.Skip("ffmpeg lacks a minimal MPEG-TS fixture encoder")
	}
	root, store, recording := fixtureRecordingWithData(t, input)
	service := openService(t, root, store, ffmpeg, 1)
	thumbnail, err := service.GenerateThumbnail(context.Background(), recording)
	if err != nil {
		t.Fatal(err)
	}
	if thumbnail.ContentType != "image/jpeg" || thumbnail.Size > maxThumbnailBytes {
		t.Fatalf("thumbnail=%+v", thumbnail)
	}
	file, _, err := service.OpenThumbnail(recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil || !validJPEGBytes(data) {
		t.Fatalf("invalid real thumbnail: size=%d err=%v", len(data), err)
	}
}

func TestMissingFFmpegIsExplicitlyUnavailable(t *testing.T) {
	root, store, recording := fixtureRecording(t)
	emptyPath := t.TempDir()
	t.Setenv("PATH", emptyPath)
	service := openService(t, root, store, "", 1)
	if service.Available() {
		t.Fatal("unexpected available backend")
	}
	if _, err := service.Start(context.Background(), recording); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Start err=%v", err)
	}
	if _, err := service.GenerateThumbnail(context.Background(), recording); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("GenerateThumbnail err=%v", err)
	}
}

func TestThumbnailGenerationUsesProjectionAndLeavesArchiveUnchanged(t *testing.T) {
	root, store, recording := fixtureRecording(t)
	ffmpeg := fakeFFmpeg(t)
	t.Setenv("DERIVATIVE_FAKE_FFMPEG", "thumbnail-success")
	service := openService(t, root, store, ffmpeg, 1)
	thumbnail, err := service.GenerateThumbnail(context.Background(), recording)
	if err != nil {
		t.Fatal(err)
	}
	if thumbnail.RecordingID != recording.ID || thumbnail.ContentType != "image/jpeg" || thumbnail.Size <= 0 || thumbnail.Size > maxThumbnailBytes {
		t.Fatalf("unexpected thumbnail summary: %+v", thumbnail)
	}
	file, opened, err := service.OpenThumbnail(recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(file)
	fileInfo, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil {
		t.Fatalf("thumbnail read errors: %v %v %v", readErr, statErr, closeErr)
	}
	if !validJPEGBytes(data) || opened.Size != int64(len(data)) || fileInfo.Mode().Perm() != 0600 {
		t.Fatalf("invalid thumbnail bytes or permissions: mode=%o size=%d", fileInfo.Mode().Perm(), len(data))
	}
	if _, err := os.Stat(filepath.Join(root, "recordings", recording.ID, "thumbnail.jpg")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("thumbnail entered canonical archive: %v", err)
	}
	payload, err := store.OpenPayload(recording.ID, recording.Tracks["main"].Segments[0].StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := io.ReadAll(payload)
	_ = payload.Close()
	if err != nil || string(canonical) != "original-segment-bytes" {
		t.Fatalf("canonical payload changed: %q err=%v", canonical, err)
	}
}

func TestThumbnailGenerationSupportsInterruptedRecordings(t *testing.T) {
	root, store, recording := fixtureRecording(t)
	recording.State = domain.StateInterrupted
	ffmpeg := fakeFFmpeg(t)
	t.Setenv("DERIVATIVE_FAKE_FFMPEG", "thumbnail-success")
	service := openService(t, root, store, ffmpeg, 1)
	thumbnail, err := service.GenerateThumbnail(context.Background(), recording)
	if err != nil {
		t.Fatalf("interrupted recording thumbnail: %v", err)
	}
	if thumbnail.RecordingID != recording.ID || thumbnail.ContentType != "image/jpeg" {
		t.Fatalf("unexpected interrupted recording thumbnail: %+v", thumbnail)
	}
}

func TestThumbnailCancellationAndFailureCleanTemporaryFiles(t *testing.T) {
	for _, mode := range []string{"wait", "fail"} {
		t.Run(mode, func(t *testing.T) {
			root, store, recording := fixtureRecording(t)
			ffmpeg := fakeFFmpeg(t)
			marker := filepath.Join(t.TempDir(), "started")
			t.Setenv("DERIVATIVE_FAKE_FFMPEG", mode)
			t.Setenv("DERIVATIVE_FAKE_STARTED", marker)
			service := openService(t, root, store, ffmpeg, 1)
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() { _, err := service.GenerateThumbnail(ctx, recording); result <- err }()
			if mode == "wait" {
				waitFile(t, marker)
				cancel()
				if err := <-result; !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation err=%v", err)
				}
			} else if err := <-result; !errors.Is(err, ErrThumbnailFailed) {
				t.Fatalf("failure err=%v", err)
			}
			cancel()
			entries, err := os.ReadDir(filepath.Join(root, "thumbnails"))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".thumbnail-") {
					t.Fatalf("temporary thumbnail remains: %s", entry.Name())
				}
			}
			if _, _, err := service.OpenThumbnail(recording.ID); !errors.Is(err, ErrThumbnailNotFound) {
				t.Fatalf("unexpected published thumbnail: %v", err)
			}
		})
	}
}

func TestOpenThumbnailRejectsMalformedIDAndSymlink(t *testing.T) {
	root, store, recording := fixtureRecording(t)
	service := openService(t, root, store, fakeFFmpeg(t), 1)
	if _, _, err := service.OpenThumbnail("../" + recording.ID); !errors.Is(err, ErrThumbnailNotFound) {
		t.Fatalf("malformed ID err=%v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.jpg")
	if err := os.WriteFile(outside, []byte{0xff, 0xd8, 0xff, 0, 0xff, 0xd9}, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "thumbnails", recording.ID+".jpg")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := service.OpenThumbnail(recording.ID); !errors.Is(err, ErrThumbnailNotFound) {
		t.Fatalf("symlink err=%v", err)
	}
}

func TestThumbnailRejectsActiveRecordingAndPropagatesCancellation(t *testing.T) {
	root, store, recording := fixtureRecording(t)
	service := openService(t, root, store, fakeFFmpeg(t), 1)
	recording.State = domain.StateRecording
	if _, err := service.GenerateThumbnail(context.Background(), recording); !errors.Is(err, ErrActive) {
		t.Fatalf("active err=%v", err)
	}
	recording.State = domain.StateStopped
	marker := filepath.Join(t.TempDir(), "started")
	t.Setenv("DERIVATIVE_FAKE_FFMPEG", "wait")
	t.Setenv("DERIVATIVE_FAKE_STARTED", marker)
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := service.GenerateThumbnail(ctx, recording); first <- err }()
	waitFile(t, marker)
	second := make(chan error, 1)
	go func() { _, err := service.GenerateThumbnail(ctx, recording); second <- err }()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first err=%v", err)
	}
	if err := <-second; !errors.Is(err, context.Canceled) {
		t.Fatalf("coalesced err=%v", err)
	}
}

func TestDuplicateExportCoalescesAndCancelCleansPartial(t *testing.T) {
	root, store, recording := fixtureRecording(t)
	ffmpeg := fakeFFmpeg(t)
	marker := filepath.Join(t.TempDir(), "started")
	t.Setenv("DERIVATIVE_FAKE_FFMPEG", "wait")
	t.Setenv("DERIVATIVE_FAKE_STARTED", marker)
	service := openService(t, root, store, ffmpeg, 1)
	first, err := service.Start(context.Background(), recording)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Start(context.Background(), recording)
	if err != nil || second.ID != first.ID {
		t.Fatalf("duplicate did not coalesce: %+v %v", second, err)
	}
	if !service.InProgress(recording.ID) {
		t.Fatal("job was not marked active")
	}
	waitFile(t, marker)
	if _, err := service.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	job := waitTerminal(t, service, first.ID)
	if job.State != StateCanceled {
		t.Fatalf("state=%s", job.State)
	}
	if _, err := os.Stat(filepath.Join(root, "exports", first.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial output remains: %v", err)
	}
	if service.InProgress(recording.ID) {
		t.Fatal("canceled export remains active")
	}
}

func TestFailedFFmpegDoesNotRetainPartialArtifact(t *testing.T) {
	root, store, recording := fixtureRecording(t)
	ffmpeg := fakeFFmpeg(t)
	t.Setenv("DERIVATIVE_FAKE_FFMPEG", "fail")
	service := openService(t, root, store, ffmpeg, 1)
	job, err := service.Start(context.Background(), recording)
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	if job.State != StateFailed || job.ErrorCode != "ffmpeg_failed" {
		t.Fatalf("job=%+v", job)
	}
	if _, err := os.Stat(filepath.Join(root, "exports", job.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failure staging remains: %v", err)
	}
}

func TestRestartMarksQueuedAndRunningJobsInterrupted(t *testing.T) {
	root, store, _ := fixtureRecording(t)
	id := strings.Repeat("a", 32)
	recordingID := strings.Repeat("b", 32)
	stateDir := filepath.Join(root, "management", "exports")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	state := persistedState{Version: 1, Jobs: []Job{
		{ID: id, RecordingID: recordingID, Kind: jobKindExport, State: StateQueued, CreatedAt: time.Now().UTC()},
		{ID: strings.Repeat("c", 32), RecordingID: recordingID, Kind: jobKindExport, State: StateRunning, CreatedAt: time.Now().UTC()},
	}}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(stateDir, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	service := openService(t, root, store, fakeFFmpeg(t), 1)
	for _, job := range service.List("") {
		if job.State != StateFailed || job.ErrorCode != "interrupted_by_restart" {
			t.Fatalf("job=%+v", job)
		}
	}
	if err := service.Delete("../" + id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unsafe delete err=%v", err)
	}
}

func TestUnavailableStateReturnsStableErrorsAndRejectsActiveRecording(t *testing.T) {
	root, store, recording := fixtureRecording(t)
	service := openService(t, root, store, fakeFFmpeg(t), 1)
	recording.State = domain.StateRecording
	if _, err := service.Start(context.Background(), recording); !errors.Is(err, ErrActive) {
		t.Fatalf("active err=%v", err)
	}
	if err := service.Delete(strings.Repeat("d", 32)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing delete err=%v", err)
	}
}

func TestBuildPlaylistUsesArchiveOrderAndInitMap(t *testing.T) {
	root, store, recording := fixtureRecording(t)
	init := domain.Segment{ID: "init-1", TrackID: "main", StoragePath: "tracks/main/init.m4s", SHA256: strings.Repeat("0", 64), PayloadSize: 1, IsInit: true}
	first := recording.Tracks["main"].Segments[0]
	first.ArchiveOrdinal, first.Sequence, first.InitSegmentID = 2, 11, "init-1"
	second := first
	second.ID, second.StoragePath, second.Sequence, second.ArchiveOrdinal = "seg-early", "tracks/main/earlier.m4s", 10, 1
	recording.Tracks["main"].Segments = []domain.Segment{first, second}
	recording.Tracks["main"].InitSegments = []domain.Segment{init}
	local := map[string]string{first.StoragePath: "object-000002.bin", second.StoragePath: "object-000003.bin", init.StoragePath: "object-000001.bin"}
	playlist, err := buildPlaylist(recording, local)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(playlist, "object-000003.bin") > strings.Index(playlist, "object-000002.bin") || !strings.Contains(playlist, `#EXT-X-MAP:URI="object-000001.bin"`) {
		t.Fatalf("playlist order/map incorrect:\n%s", playlist)
	}
	_ = root
	_ = store
}

func fixtureRecording(t *testing.T) (string, *storage.Store, *domain.Recording) {
	return fixtureRecordingWithData(t, []byte("original-segment-bytes"))
}

func fixtureRecordingWithData(t *testing.T, data []byte) (string, *storage.Store, *domain.Recording) {
	t.Helper()
	root := t.TempDir()
	store, err := storage.New(root)
	if err != nil {
		t.Fatal(err)
	}
	const recordingID = "0123456789abcdef0123456789abcdef"
	if err := store.NewRecordingDir(recordingID); err != nil {
		t.Fatal(err)
	}
	const relative = "tracks/main/00000001.ts"
	result, err := store.SavePayload(recordingID, relative, strings.NewReader(string(data)), 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	segment := domain.Segment{ID: "segment-1", TrackID: "main", Sequence: 1, ArchiveOrdinal: 1, StoragePath: relative, SourceURI: "https://source.example/private?token=must-not-escape", Duration: 2, PayloadSize: result.Size, SHA256: result.SHA256}
	if err := store.SaveSidecar(recordingID, relative, segment); err != nil {
		t.Fatal(err)
	}
	recording := &domain.Recording{FormatVersion: 1, ID: recordingID, Title: "Fixture", State: domain.StateStopped, Tracks: map[string]*domain.Track{"main": {ID: "main", Segments: []domain.Segment{segment}}}}
	if err := store.SaveRecording(recording); err != nil {
		t.Fatal(err)
	}
	return root, store, recording
}

func fakeFFmpeg(t *testing.T) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	path := filepath.Join(t.TempDir(), "ffmpeg-fake")
	script := "#!/bin/sh\nexec " + quote(binary) + " -test.run=^TestFakeFFmpegHelper$ -- \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func openService(t *testing.T, root string, store *storage.Store, ffmpeg string, workers int) *Service {
	t.Helper()
	service, err := Open(root, store, ffmpeg, workers)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := service.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return service
}

func waitTerminal(t *testing.T, service *Service, id string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := service.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if !active(job.State) {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	job, _ := service.Get(id)
	t.Fatalf("job did not complete: %+v", job)
	return Job{}
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper process did not start")
}

func contains(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func containsPair(args []string, first, second string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == first && args[i+1] == second {
			return true
		}
	}
	return false
}

func validJPEGBytes(data []byte) bool {
	return len(data) >= 5 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff && data[len(data)-2] == 0xff && data[len(data)-1] == 0xd9
}

func TestErrorCodesDoNotExposeCommandOrSourceURI(t *testing.T) {
	job := Job{ErrorCode: "ffmpeg_failed", OutputName: "recording-0123456789abcdef0123456789abcdef.mkv"}
	encoded, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/path/to", "token", "source.example", "private"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("unsafe job output contains %q: %s", forbidden, encoded)
		}
	}
	if !strings.HasPrefix(fmt.Sprint(job.ErrorCode), "ffmpeg_") {
		t.Fatal("unexpected error code")
	}
}
