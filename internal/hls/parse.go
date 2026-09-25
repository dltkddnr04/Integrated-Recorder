// Package hls parses the bounded subset of HLS needed for single-rendition
// archival. It does not parse or modify media payloads.
package hls

import (
	"bufio"
	"bytes"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/domain"
)

const MaxManifestBytes = 4 << 20

type Variant struct {
	URI        string
	Bandwidth  int64
	AudioGroup string
}

type Master struct {
	Variants []Variant
}

type Map struct {
	URI       string
	ByteRange *domain.ByteRange
}

type MediaSegment struct {
	Sequence      uint64
	URI           string
	Duration      float64
	ProgramTime   *time.Time
	Init          *Map
	ByteRange     *domain.ByteRange
	Discontinuity bool
	Gap           bool
}

type MediaPlaylist struct {
	TargetDuration int
	MediaSequence  uint64
	Segments       []MediaSegment
	EndList        bool
}

func ParseMaster(data []byte, baseURL string) (Master, error) {
	lines, err := playlistLines(data)
	if err != nil {
		return Master{}, err
	}
	var result Master
	var pending *Variant
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			if _, err := parseAttrs(strings.TrimPrefix(line, "#EXT-X-MEDIA:")); err != nil {
				return Master{}, err
			}
		case strings.HasPrefix(line, "#EXT-X-SESSION-KEY:"):
			return Master{}, fmt.Errorf("encrypted HLS is unsupported (EXT-X-SESSION-KEY)")
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			if pending != nil {
				return Master{}, fmt.Errorf("master playlist has EXT-X-STREAM-INF without a URI")
			}
			attrs, err := parseAttrs(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
			if err != nil {
				return Master{}, err
			}
			bw, err := strconv.ParseInt(attrs["BANDWIDTH"], 10, 64)
			if err != nil || bw <= 0 {
				return Master{}, fmt.Errorf("invalid EXT-X-STREAM-INF BANDWIDTH")
			}
			pending = &Variant{Bandwidth: bw, AudioGroup: attrs["AUDIO"]}
		case strings.HasPrefix(line, "#") || line == "":
			continue
		default:
			if pending == nil {
				continue
			}
			resolved, err := resolveURI(baseURL, line)
			if err != nil {
				return Master{}, err
			}
			pending.URI = resolved
			result.Variants = append(result.Variants, *pending)
			pending = nil
		}
	}
	if pending != nil {
		return Master{}, fmt.Errorf("master playlist has EXT-X-STREAM-INF without a URI")
	}
	if len(result.Variants) == 0 {
		return Master{}, fmt.Errorf("master playlist contains no variants")
	}
	// Any AUDIO reference indicates that the rendition is not self-contained.
	compatible := result.Variants[:0]
	for _, v := range result.Variants {
		if v.AudioGroup == "" {
			compatible = append(compatible, v)
		}
	}
	if len(compatible) == 0 {
		return Master{}, fmt.Errorf("master playlist requires an external audio rendition, which is unsupported")
	}
	result.Variants = compatible
	return result, nil
}

func SelectHighestBandwidth(master Master) (Variant, error) {
	if len(master.Variants) == 0 {
		return Variant{}, fmt.Errorf("no compatible variants")
	}
	best := master.Variants[0]
	for _, candidate := range master.Variants[1:] {
		if candidate.Bandwidth > best.Bandwidth {
			best = candidate
		}
	}
	return best, nil
}

func ParseMedia(data []byte, playlistURL string) (MediaPlaylist, error) {
	lines, err := playlistLines(data)
	if err != nil {
		return MediaPlaylist{}, err
	}
	var result MediaPlaylist
	var haveMediaSequence bool
	var nextDuration *float64
	var nextPDT *time.Time
	var discontinuity bool
	var currentMap *Map
	var currentRange *domain.ByteRange
	var previousRangeEnd uint64
	var previousRangeURI string
	var rangeImplicit bool
	var segmentGap bool
	var sequence uint64
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			if len(result.Segments) > 0 {
				return MediaPlaylist{}, fmt.Errorf("MEDIA-SEQUENCE appears after media entries")
			}
			value := strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:")
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return MediaPlaylist{}, fmt.Errorf("invalid MEDIA-SEQUENCE")
			}
			result.MediaSequence, sequence, haveMediaSequence = parsed, parsed, true
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			parsed, err := strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"))
			if err != nil || parsed <= 0 {
				return MediaPlaylist{}, fmt.Errorf("invalid TARGETDURATION")
			}
			result.TargetDuration = parsed
		case strings.HasPrefix(line, "#EXTINF:"):
			value := strings.TrimSuffix(strings.TrimPrefix(line, "#EXTINF:"), ",")
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil || parsed < 0 {
				return MediaPlaylist{}, fmt.Errorf("invalid EXTINF duration")
			}
			nextDuration = &parsed
		case strings.HasPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:"):
			parsed, err := parseProgramDateTime(strings.TrimPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:"))
			if err != nil {
				return MediaPlaylist{}, fmt.Errorf("invalid PROGRAM-DATE-TIME: %w", err)
			}
			nextPDT = &parsed
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			attrs, err := parseAttrs(strings.TrimPrefix(line, "#EXT-X-MAP:"))
			if err != nil {
				return MediaPlaylist{}, err
			}
			uri, err := resolveURI(playlistURL, attrs["URI"])
			if err != nil {
				return MediaPlaylist{}, fmt.Errorf("invalid EXT-X-MAP URI: %w", err)
			}
			m := &Map{URI: uri}
			if attrs["BYTERANGE"] != "" {
				br, err := parseRange(attrs["BYTERANGE"], 0)
				if err != nil {
					return MediaPlaylist{}, err
				}
				m.ByteRange = &br
			}
			currentMap = m
		case strings.HasPrefix(line, "#EXT-X-BYTERANGE:"):
			value := strings.TrimPrefix(line, "#EXT-X-BYTERANGE:")
			offset := uint64(0)
			rangeImplicit = !strings.Contains(value, "@")
			if rangeImplicit {
				if previousRangeURI == "" {
					return MediaPlaylist{}, fmt.Errorf("implicit BYTERANGE offset has no previous range")
				}
				offset = previousRangeEnd
			}
			br, err := parseRange(value, offset)
			if err != nil {
				return MediaPlaylist{}, err
			}
			currentRange = &br
		case line == "#EXT-X-DISCONTINUITY":
			discontinuity = true
		case line == "#EXT-X-GAP":
			segmentGap = true
		case line == "#EXT-X-ENDLIST":
			result.EndList = true
		case strings.HasPrefix(line, "#EXT-X-PART:") || strings.HasPrefix(line, "#EXT-X-PART-INF:") || strings.HasPrefix(line, "#EXT-X-PRELOAD-HINT:") || strings.HasPrefix(line, "#EXT-X-SERVER-CONTROL:") || strings.HasPrefix(line, "#EXT-X-RENDITION-REPORT:"):
			return MediaPlaylist{}, fmt.Errorf("low-latency HLS is unsupported (%s)", strings.SplitN(line, ":", 2)[0])
		case strings.HasPrefix(line, "#EXT-X-SKIP:"):
			return MediaPlaylist{}, fmt.Errorf("HLS delta updates are unsupported (EXT-X-SKIP)")
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			attrs, err := parseAttrs(strings.TrimPrefix(line, "#EXT-X-KEY:"))
			if err != nil {
				return MediaPlaylist{}, err
			}
			if !strings.EqualFold(attrs["METHOD"], "NONE") {
				return MediaPlaylist{}, fmt.Errorf("encrypted HLS is unsupported (EXT-X-KEY METHOD=%q)", attrs["METHOD"])
			}
		case strings.HasPrefix(line, "#EXT-X-SESSION-KEY:"):
			return MediaPlaylist{}, fmt.Errorf("encrypted HLS is unsupported (EXT-X-SESSION-KEY)")
		case strings.HasPrefix(line, "#") || line == "":
			continue
		default:
			if nextDuration == nil {
				return MediaPlaylist{}, fmt.Errorf("media URI without preceding EXTINF")
			}
			if !haveMediaSequence {
				haveMediaSequence = true
				sequence = 0
				result.MediaSequence = 0
			}
			uri, err := resolveURI(playlistURL, line)
			if err != nil {
				return MediaPlaylist{}, err
			}
			if rangeImplicit && uri != previousRangeURI {
				return MediaPlaylist{}, fmt.Errorf("implicit BYTERANGE offset requires the same resource URI")
			}
			segment := MediaSegment{Sequence: sequence, URI: uri, Duration: *nextDuration, ProgramTime: nextPDT, Init: cloneMap(currentMap), ByteRange: cloneRange(currentRange), Discontinuity: discontinuity, Gap: segmentGap}
			result.Segments = append(result.Segments, segment)
			if currentRange != nil {
				previousRangeURI = uri
				previousRangeEnd = currentRange.Offset + currentRange.Length
			} else {
				// An implicit offset may only continue the immediately preceding
				// media segment when that segment was a sub-range of this resource.
				previousRangeURI = ""
				previousRangeEnd = 0
			}
			sequence++
			nextDuration, nextPDT, currentRange, discontinuity, rangeImplicit, segmentGap = nil, nil, nil, false, false, false
		}
	}
	if nextDuration != nil || segmentGap {
		return MediaPlaylist{}, fmt.Errorf("EXTINF has no media URI")
	}
	if result.TargetDuration == 0 {
		return MediaPlaylist{}, fmt.Errorf("media playlist is missing TARGETDURATION")
	}
	return result, nil
}

func parseProgramDateTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err == nil {
		return parsed, nil
	}
	// Owncast emits valid UTC offsets without the RFC3339 colon (for example,
	// 2026-09-25T02:46:24.040+0000). Accept that common HLS form as well.
	return time.Parse("2006-01-02T15:04:05.999999999-0700", value)
}

func playlistLines(data []byte) ([]string, error) {
	if len(data) > MaxManifestBytes {
		return nil, fmt.Errorf("manifest exceeds %d bytes", MaxManifestBytes)
	}
	s := bufio.NewScanner(bytes.NewReader(data))
	s.Buffer(make([]byte, 1024), MaxManifestBytes)
	lines := make([]string, 0, 64)
	first := true
	for s.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(s.Text(), "\ufeff"))
		if first {
			first = false
			if line != "#EXTM3U" {
				return nil, fmt.Errorf("invalid HLS header")
			}
		}
		lines = append(lines, line)
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	if first {
		return nil, fmt.Errorf("empty manifest")
	}
	return lines, nil
}

func parseAttrs(raw string) (map[string]string, error) {
	attrs := map[string]string{}
	for i := 0; i < len(raw); {
		start := i
		for i < len(raw) && raw[i] != '=' && raw[i] != ',' {
			i++
		}
		if i >= len(raw) || raw[i] != '=' {
			return nil, fmt.Errorf("malformed HLS attribute list")
		}
		key := strings.TrimSpace(raw[start:i])
		i++
		var value string
		if i < len(raw) && raw[i] == '"' {
			i++
			var b strings.Builder
			closed := false
			for i < len(raw) {
				if raw[i] == '"' {
					i++
					closed = true
					break
				}
				b.WriteByte(raw[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated quoted HLS attribute")
			}
			value = b.String()
		} else {
			start = i
			for i < len(raw) && raw[i] != ',' {
				i++
			}
			value = strings.TrimSpace(raw[start:i])
		}
		if _, exists := attrs[key]; exists {
			return nil, fmt.Errorf("duplicate HLS attribute %q", key)
		}
		attrs[key] = value
		if i < len(raw) {
			if raw[i] != ',' {
				return nil, fmt.Errorf("malformed HLS attribute separator")
			}
			i++
		}
	}
	return attrs, nil
}

func resolveURI(base, ref string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(ref))
	if err != nil || u.String() == "" {
		return "", fmt.Errorf("invalid URI %q", ref)
	}
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	out := b.ResolveReference(u)
	if (out.Scheme != "http" && out.Scheme != "https") || out.Host == "" {
		return "", fmt.Errorf("URI must resolve to http or https")
	}
	return out.String(), nil
}
func parseRange(s string, defaultOffset uint64) (domain.ByteRange, error) {
	pieces := strings.Split(s, "@")
	if len(pieces) > 2 {
		return domain.ByteRange{}, fmt.Errorf("invalid byte range %q", s)
	}
	length, err := strconv.ParseUint(pieces[0], 10, 64)
	if err != nil || length == 0 {
		return domain.ByteRange{}, fmt.Errorf("invalid byte range %q", s)
	}
	offset := defaultOffset
	if len(pieces) == 2 {
		offset, err = strconv.ParseUint(pieces[1], 10, 64)
		if err != nil {
			return domain.ByteRange{}, fmt.Errorf("invalid byte range offset")
		}
	}
	return domain.ByteRange{Length: length, Offset: offset}, nil
}
func cloneMap(m *Map) *Map {
	if m == nil {
		return nil
	}
	r := *m
	r.ByteRange = cloneRange(m.ByteRange)
	return &r
}
func cloneRange(r *domain.ByteRange) *domain.ByteRange {
	if r == nil {
		return nil
	}
	c := *r
	return &c
}
