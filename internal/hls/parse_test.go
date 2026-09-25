package hls

import (
	"strings"
	"testing"
	"time"
)

func TestParseMasterSelectsHighestCompatibleVariant(t *testing.T) {
	data := []byte(`#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aac",NAME="Audio",URI="audio.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=900000,AUDIO="aac"
video-only.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=500000
../muxed/playlist.m3u8
`)
	master, err := ParseMaster(data, "https://stream.example/hls/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	variant, err := SelectHighestBandwidth(master)
	if err != nil {
		t.Fatal(err)
	}
	if variant.Bandwidth != 500000 || variant.URI != "https://stream.example/muxed/playlist.m3u8" {
		t.Fatalf("selected %#v", variant)
	}
}

func TestParseMediaTagsAndRelativeURIs(t *testing.T) {
	data := []byte(`#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:41
#EXT-X-MAP:URI="../init.mp4",BYTERANGE="8@0"
#EXT-X-PROGRAM-DATE-TIME:2026-09-25T10:11:12.123Z
#EXTINF:5.25,
#EXT-X-BYTERANGE:3@0
chunk.m4s
#EXT-X-DISCONTINUITY
#EXTINF:4.5,
#EXT-X-BYTERANGE:4
chunk.m4s
#EXT-X-ENDLIST
`)
	playlist, err := ParseMedia(data, "https://cdn.example/hls/rendition/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if playlist.TargetDuration != 6 || playlist.MediaSequence != 41 || !playlist.EndList || len(playlist.Segments) != 2 {
		t.Fatalf("playlist %#v", playlist)
	}
	first, second := playlist.Segments[0], playlist.Segments[1]
	if first.URI != "https://cdn.example/hls/rendition/chunk.m4s" {
		t.Fatalf("relative segment URI = %q", first.URI)
	}
	if first.Init == nil || first.Init.URI != "https://cdn.example/hls/init.mp4" || first.Init.ByteRange.Length != 8 {
		t.Fatalf("map %#v", first.Init)
	}
	if first.ByteRange.Offset != 0 || second.ByteRange.Offset != 3 || second.ByteRange.Length != 4 {
		t.Fatalf("ranges %#v, %#v", first.ByteRange, second.ByteRange)
	}
	if first.ProgramTime == nil || !first.ProgramTime.Equal(time.Date(2026, 9, 25, 10, 11, 12, 123000000, time.UTC)) {
		t.Fatalf("PDT = %v", first.ProgramTime)
	}
	if !second.Discontinuity || second.Sequence != 42 || second.Duration != 4.5 {
		t.Fatalf("second segment %#v", second)
	}
}

func TestParseMediaCompactProgramDateTimeOffset(t *testing.T) {
	playlist, err := ParseMedia([]byte(`#EXTM3U
#EXT-X-TARGETDURATION:3
#EXT-X-MEDIA-SEQUENCE:248223
#EXT-X-PROGRAM-DATE-TIME:2026-09-25T02:46:24.040+0000
#EXTINF:3.000000,
stream-248223.ts
`), "https://stream.example/hls/0/stream.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if len(playlist.Segments) != 1 || playlist.Segments[0].ProgramTime == nil {
		t.Fatalf("playlist %#v", playlist)
	}
	want := time.Date(2026, 9, 25, 2, 46, 24, 40_000_000, time.UTC)
	if !playlist.Segments[0].ProgramTime.Equal(want) {
		t.Fatalf("PDT = %v, want %v", playlist.Segments[0].ProgramTime, want)
	}
}

func TestParseMediaRejectsEncryptionAndMalformedRanges(t *testing.T) {
	base := "https://stream.example/list.m3u8"
	for name, playlist := range map[string]string{
		"encrypted":                         "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-KEY:METHOD=AES-128,URI=\"key\"\n#EXTINF:2,\na.ts\n",
		"implicit range without prior":      "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\n#EXT-X-BYTERANGE:5\na.ts\n",
		"range changes resource":            "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\n#EXT-X-BYTERANGE:5@0\na.ts\n#EXTINF:2,\n#EXT-X-BYTERANGE:5\nb.ts\n",
		"implicit range after full segment": "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\n#EXT-X-BYTERANGE:5@0\na.ts\n#EXTINF:2,\nb.ts\n#EXTINF:2,\n#EXT-X-BYTERANGE:5\na.ts\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMedia([]byte(playlist), base); err == nil {
				t.Fatal("ParseMedia succeeded")
			}
		})
	}
	if _, err := ParseMedia([]byte(strings.Repeat("x", MaxManifestBytes+1)), base); err == nil {
		t.Fatal("oversized manifest was accepted")
	}
}

func TestParseMediaMarksExplicitGapAndRejectsUnsupportedModes(t *testing.T) {
	base := "https://stream.example/list.m3u8"
	playlist, err := ParseMedia([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:7\n#EXTINF:2,\n#EXT-X-GAP\nmissing.ts\n#EXTINF:2,\nnext.ts\n"), base)
	if err != nil {
		t.Fatal(err)
	}
	if len(playlist.Segments) != 2 || !playlist.Segments[0].Gap || playlist.Segments[1].Gap {
		t.Fatalf("explicit gap not associated with its segment: %#v", playlist.Segments)
	}
	for _, unsupported := range []string{
		"#EXT-X-PART:DURATION=0.5,URI=\"part.m4s\"",
		"#EXT-X-PRELOAD-HINT:TYPE=PART,URI=\"part.m4s\"",
		"#EXT-X-SKIP:SKIPPED-SEGMENTS=2",
	} {
		data := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n" + unsupported + "\n#EXTINF:2,\nsegment.ts\n")
		if _, err := ParseMedia(data, base); err == nil {
			t.Errorf("unsupported HLS mode %q was accepted", unsupported)
		}
	}
}
