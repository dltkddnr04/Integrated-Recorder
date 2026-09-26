package hls

import (
	"fmt"
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

func TestParseMediaDiscontinuitySequenceTracksEffectiveValue(t *testing.T) {
	data := []byte(`#EXTM3U
#EXT-X-TARGETDURATION:2
#EXT-X-DISCONTINUITY-SEQUENCE:7
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:1,
a.ts
#EXT-X-DISCONTINUITY
#EXTINF:1,
b.ts
#EXTINF:1,
c.ts
`)
	playlist, err := ParseMedia(data, "https://stream.example/list.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if playlist.DiscontinuitySequence != 7 || len(playlist.Segments) != 3 || playlist.Segments[0].DiscontinuitySequence != 7 || playlist.Segments[1].DiscontinuitySequence != 8 || !playlist.Segments[1].Discontinuity || playlist.Segments[2].DiscontinuitySequence != 8 {
		t.Fatalf("discontinuity sequence model = %#v", playlist)
	}
	for _, bad := range []string{
		"#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-DISCONTINUITY-SEQUENCE:18446744073709551615\n#EXT-X-DISCONTINUITY\n#EXTINF:1,\na.ts\n",
		"#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-DISCONTINUITY\n#EXT-X-DISCONTINUITY-SEQUENCE:1\n#EXTINF:1,\na.ts\n",
		"#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:1,\na.ts\n#EXT-X-DISCONTINUITY-SEQUENCE:1\n",
	} {
		if _, err := ParseMedia([]byte(bad), "https://stream.example/list.m3u8"); err == nil {
			t.Errorf("invalid discontinuity sequence accepted: %s", bad)
		}
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
	for _, unsupported := range []struct{ tag, message string }{
		{"#EXT-X-SKIP:SKIPPED-SEGMENTS=2", "delta updates"},
		{"#EXT-X-DEFINE:NAME=segment,VALUE=one.ts", "variable substitution"},
	} {
		data := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n" + unsupported.tag + "\n#EXTINF:2,\nsegment.ts\n")
		if _, err := ParseMedia(data, base); err == nil || !strings.Contains(err.Error(), unsupported.message) {
			t.Errorf("unsupported HLS mode %q error = %v", unsupported.tag, err)
		}
	}
}

func TestParseMediaEXTINFOptionalTitle(t *testing.T) {
	for _, input := range []struct {
		name, extinf string
		want         float64
	}{
		{name: "empty title", extinf: "10,", want: 10},
		{name: "title", extinf: "10.5,title", want: 10.5},
		{name: "commas in title", extinf: "10.5,title,with,commas", want: 10.5},
		{name: "no title", extinf: "10.5", want: 10.5},
	} {
		t.Run(input.name, func(t *testing.T) {
			playlist, err := ParseMedia([]byte(fmt.Sprintf("#EXTM3U\n#EXT-X-TARGETDURATION:11\n#EXTINF:%s\nsegment.ts\n", input.extinf)), "https://stream.example/list.m3u8")
			if err != nil {
				t.Fatal(err)
			}
			if len(playlist.Segments) != 1 || playlist.Segments[0].Duration != input.want {
				t.Fatalf("parsed segment = %#v", playlist.Segments)
			}
		})
	}
}

func TestParseMediaRejectsNonFiniteDurationsAndPathologicalTargets(t *testing.T) {
	base := "#EXTM3U\n"
	for _, duration := range []string{"NaN", "+Inf", "-Inf", "-1", "10oops", "1e2", "+1", ".5", "86401"} {
		t.Run("duration_"+duration, func(t *testing.T) {
			data := []byte(base + "#EXT-X-TARGETDURATION:86400\n#EXTINF:" + duration + ",\nsegment.ts\n")
			if _, err := ParseMedia(data, "https://stream.example/list.m3u8"); err == nil {
				t.Fatal("invalid duration was accepted")
			}
		})
	}
	for _, target := range []string{"0", "-1", "86401", "4294967296", "999999999999999999999999"} {
		t.Run("target_"+target, func(t *testing.T) {
			data := []byte(base + "#EXT-X-TARGETDURATION:" + target + "\n#EXTINF:1,\nsegment.ts\n")
			if _, err := ParseMedia(data, "https://stream.example/list.m3u8"); err == nil {
				t.Fatal("invalid target duration was accepted")
			}
		})
	}
}

func TestParseMediaSequenceAndByteRangeOverflow(t *testing.T) {
	base := "https://stream.example/list.m3u8"
	max := "18446744073709551615"
	validMaxSequence := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:" + max + "\n#EXTINF:1,\nlast.ts\n")
	playlist, err := ParseMedia(validMaxSequence, base)
	if err != nil || len(playlist.Segments) != 1 || playlist.Segments[0].Sequence != ^uint64(0) {
		t.Fatalf("single max sequence: playlist=%#v err=%v", playlist, err)
	}
	sequenceOverflow := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:" + max + "\n#EXTINF:1,\na.ts\n#EXTINF:1,\nb.ts\n")
	if _, err := ParseMedia(sequenceOverflow, base); err == nil {
		t.Fatal("sequence overflow was accepted")
	}
	for _, tag := range []string{
		"#EXT-X-BYTERANGE:2@18446744073709551614",
		"#EXT-X-BYTERANGE:1@18446744073709551615",
	} {
		data := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:1,\n" + tag + "\na.ts\n")
		if _, err := ParseMedia(data, base); err == nil {
			t.Errorf("overflowing range %q was accepted", tag)
		}
	}
	implicitOverflow := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:1,\n#EXT-X-BYTERANGE:18446744073709551615@0\na.ts\n#EXTINF:1,\n#EXT-X-BYTERANGE:1\na.ts\n")
	if _, err := ParseMedia(implicitOverflow, base); err == nil {
		t.Fatal("implicit range overflow was accepted")
	}
}

func TestParseMediaMapByteRangeRequiresExplicitOffset(t *testing.T) {
	base := "https://stream.example/list.m3u8"
	for _, tc := range []struct {
		name, byterange string
		wantErr         bool
	}{
		{name: "explicit", byterange: "8@0"},
		{name: "implicit", byterange: "8", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MAP:URI=\"init.mp4\",BYTERANGE=\"" + tc.byterange + "\"\n#EXTINF:1,\nseg.m4s\n")
			playlist, err := ParseMedia(data, base)
			if tc.wantErr {
				if err == nil {
					t.Fatal("implicit map range was accepted")
				}
				return
			}
			if err != nil || playlist.Segments[0].Init == nil || playlist.Segments[0].Init.ByteRange.Offset != 0 {
				t.Fatalf("playlist=%#v err=%v", playlist, err)
			}
		})
	}
}

func TestMasterPlaylistClassificationIsLineAware(t *testing.T) {
	if !IsMasterPlaylist([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\na.m3u8\n")) {
		t.Fatal("master playlist was not classified")
	}
	for _, data := range []string{
		"#EXTM3U\n# example mentions #EXT-X-STREAM-INF:BANDWIDTH=1\n",
		"#EXTM3U\n#EXT-X-STREAM-INF is not an HLS tag line\n",
		"#EXTM3U\n#EXT-X-STREAM-INF-BROKEN:BANDWIDTH=1\n",
	} {
		if IsMasterPlaylist([]byte(data)) {
			t.Errorf("misleading line classified as master: %q", data)
		}
	}
	iframe := []byte("#EXTM3U\n#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=100,URI=\"iframe.m3u8\"\n")
	if !IsMasterPlaylist(iframe) {
		t.Fatal("I-frame master was not classified for deterministic rejection")
	}
	if _, err := ParseMaster(iframe, "https://stream.example/master.m3u8"); err == nil || !strings.Contains(err.Error(), "I-frame-only") {
		t.Fatalf("I-frame master rejection = %v", err)
	}
	if _, err := ParseMaster([]byte("#EXTM3U\n#EXT-X-DEFINE:NAME=x,VALUE=y\n#EXT-X-STREAM-INF:BANDWIDTH=100\n{$x}.m3u8\n"), "https://stream.example/master.m3u8"); err == nil || !strings.Contains(err.Error(), "variable substitution") {
		t.Fatalf("master variable substitution rejection = %v", err)
	}
}

func TestParseMasterRejectsExternalSubtitleAndVideoRenditions(t *testing.T) {
	for _, group := range []string{"SUBTITLES", "VIDEO"} {
		t.Run(group, func(t *testing.T) {
			data := []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100," + group + "=\"external\"\nvariant.m3u8\n")
			if _, err := ParseMaster(data, "https://stream.example/master.m3u8"); err == nil {
				t.Fatalf("external %s rendition was accepted", group)
			}
		})
	}
}

func TestParseRejectsUserinfoInResolvedResourceURIs(t *testing.T) {
	for _, tc := range []struct {
		name, base, playlist string
	}{
		{
			name:     "absolute segment",
			base:     "https://stream.example/list.m3u8",
			playlist: "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:1,\nhttps://user:pass@cdn.example/segment.ts\n",
		},
		{
			name:     "relative segment resolving against credentialed base",
			base:     "https://user:pass@stream.example/list.m3u8",
			playlist: "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:1,\nsegment.ts\n",
		},
		{
			name:     "map",
			base:     "https://stream.example/list.m3u8",
			playlist: "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MAP:URI=\"https://user:pass@cdn.example/init.mp4\"\n#EXTINF:1,\nsegment.m4s\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseMedia([]byte(tc.playlist), tc.base); err == nil {
				t.Fatal("userinfo in resolved resource URI was accepted")
			}
		})
	}

	valid, err := ParseMedia([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:1,\nsegment.ts?signature=preserved\n"), "https://stream.example/list.m3u8")
	if err != nil || len(valid.Segments) != 1 || valid.Segments[0].URI != "https://stream.example/segment.ts?signature=preserved" {
		t.Fatalf("valid HTTP(S) relative resource rejected or changed: playlist=%#v err=%v", valid, err)
	}
	master, err := ParseMaster([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100\nhttps://user:pass@cdn.example/child.m3u8\n"), "https://stream.example/master.m3u8")
	if err == nil || len(master.Variants) != 0 {
		t.Fatalf("userinfo master rendition was accepted: %#v", master)
	}
}

func TestParseMediaAllowsCompletedSegmentsAlongsideLLTags(t *testing.T) {
	data := []byte(`#EXTM3U
#EXT-X-TARGETDURATION:2
#EXT-X-SERVER-CONTROL:CAN-BLOCK-RELOAD=YES
#EXT-X-PART-INF:PART-TARGET=0.5
#EXTINF:2,completed
full.ts
#EXT-X-PART:DURATION=0.5,URI="partial.m4s"
#EXT-X-PRELOAD-HINT:TYPE=PART,URI="next.m4s"
#EXT-X-RENDITION-REPORT:URI="other.m3u8"
`)
	playlist, err := ParseMedia(data, "https://stream.example/list.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if len(playlist.Segments) != 1 || playlist.Segments[0].URI != "https://stream.example/full.ts" {
		t.Fatalf("partial segments were not ignored: %#v", playlist.Segments)
	}
	partialOnly := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-PART-INF:PART-TARGET=0.5\n#EXT-X-PART:DURATION=0.5,URI=\"partial.m4s\"\n")
	if _, err := ParseMedia(partialOnly, "https://stream.example/list.m3u8"); err == nil || !strings.Contains(err.Error(), "no completed") {
		t.Fatalf("partial-only playlist error = %v", err)
	}
	deltaWithFull := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-SKIP:SKIPPED-SEGMENTS=1\n#EXTINF:1,\nfull.ts\n")
	if _, err := ParseMedia(deltaWithFull, "https://stream.example/list.m3u8"); err == nil || !strings.Contains(err.Error(), "delta updates") {
		t.Fatalf("delta playlist error = %v", err)
	}
}

func FuzzManifestParsersDoNotPanic(f *testing.F) {
	f.Add([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:3\n#EXTINF:3,\nsegment.ts\n"))
	f.Add([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nchild.m3u8\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxManifestBytes {
			return
		}
		_, _ = ParseMaster(data, "https://media.example/master.m3u8")
		_, _ = ParseMedia(data, "https://media.example/media.m3u8")
		_ = IsMasterPlaylist(data)
	})
}
