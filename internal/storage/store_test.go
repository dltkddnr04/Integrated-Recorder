package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
	"github.com/dltkddnr04/integrated-recorder/internal/domain"
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
	recording := &domain.Recording{FormatVersion: 1, ID: id, SourceURL: "https://source.example", State: domain.StateRecording, CreatedAt: started, StartedAt: started, Tracks: map[string]*domain.Track{"main": {ID: "main", PendingSequences: []uint64{3}, Segments: []domain.Segment{{ID: "s2", Sequence: 2}, {ID: "s1", Sequence: 1}}}}}
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

func TestRecordingPermissionsCanonicalURIsAndProvenanceSurviveReload(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	const id = "abcdef0123456789abcdef0123456789"
	if err = store.NewRecordingDir(id); err != nil {
		t.Fatal(err)
	}
	uri := "https://media.example/segment.m4s?token=opaque-value"
	recording := &domain.Recording{FormatVersion: 1, ID: id, AdapterID: "opaque-adapter", Adapter: &adapterproto.AdapterProvenance{ID: "opaque-adapter", Version: "2.1", ProtocolVersion: 1, Fingerprint: "abcd"}, SourceURIClassification: "sensitive", State: domain.StateStopped, CreatedAt: time.Now().UTC(), StartedAt: time.Now().UTC(), Tracks: map[string]*domain.Track{"main": {ID: "main", SourcePlaylistURL: "https://media.example/live.m3u8?sig=opaque", Segments: []domain.Segment{{ID: "segment-1", TrackID: "main", SourceURI: uri, StoragePath: "tracks/main/segment.m4s", PayloadSize: 3, SHA256: "hash"}}}}}
	if err = store.SaveRecording(recording); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveSidecar(id, "tracks/main/segment.m4s", recording.Tracks["main"].Segments[0]); err != nil {
		t.Fatal(err)
	}
	manifest := []byte("#EXTM3U\nsegment.m4s?token=opaque-value\n")
	snapshot, err := store.SaveSnapshot(id, "main", "https://media.example/live.m3u8?auth=opaque", manifest, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	storedManifest, err := store.OpenPayload(id, snapshot.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, len(manifest))
	_, err = storedManifest.Read(data)
	storedManifest.Close()
	if err != nil || !bytes.Equal(data, manifest) {
		t.Fatalf("raw manifest changed: %q %v", data, err)
	}
	for path, want := range map[string]os.FileMode{filepath.Join(root, "recordings"): 0700, filepath.Join(root, "recordings", id): 0700, filepath.Join(root, "recordings", id, "manifests"): 0700, filepath.Join(root, "recordings", id, "recording.json"): 0600, filepath.Join(root, "recordings", id, "tracks", "main", "segment.m4s.json"): 0600} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != want {
			t.Errorf("mode %s=%o want %o", path, info.Mode().Perm(), want)
		}
	}
	loaded, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d recordings", len(loaded))
	}
	got := loaded[0]
	if got.Adapter == nil || got.Adapter.ID != "opaque-adapter" || got.Adapter.Version != "2.1" || got.Adapter.ProtocolVersion != 1 || got.Adapter.Fingerprint != "abcd" {
		t.Fatalf("provenance did not reload: %#v", got.Adapter)
	}
	if got.SourceURIClassification != "sensitive" || got.Tracks["main"].Segments[0].SourceURI != uri || got.Tracks["main"].SourcePlaylistURL != "https://media.example/live.m3u8?sig=opaque" {
		t.Fatalf("canonical source URI was altered: %#v", got)
	}
}

func TestLegacyRecordingWithoutProvenanceStillLoads(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "0123456789abcdef0123456789abcdef"
	if err = store.NewRecordingDir(id); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"format_version":1,"id":"` + id + `","state":"stopped","created_at":"2020-01-01T00:00:00Z","started_at":"2020-01-01T00:00:00Z","tracks":{}}`)
	if err = os.WriteFile(filepath.Join(store.recordingDir(id), "recording.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadAll()
	if err != nil || len(loaded) != 1 || loaded[0].Adapter != nil {
		t.Fatalf("legacy recording load=%#v %v", loaded, err)
	}
}
