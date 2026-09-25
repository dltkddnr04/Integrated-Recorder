package acquire

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
	"github.com/dltkddnr04/integrated-recorder/internal/domain"
	"github.com/dltkddnr04/integrated-recorder/internal/hls"
	"github.com/dltkddnr04/integrated-recorder/internal/storage"
)

const (
	maxPayloadBytes  = int64(512 << 20)
	manifestAttempts = 3
	segmentAttempts  = 3
)

func parseMaster(data []byte, url string) (hls.Master, error)       { return hls.ParseMaster(data, url) }
func parseMedia(data []byte, url string) (hls.MediaPlaylist, error) { return hls.ParseMedia(data, url) }
func chooseVariant(master hls.Master) (hls.Variant, error)          { return hls.SelectHighestBandwidth(master) }

func hasMasterTag(data []byte) bool { return strings.Contains(string(data), "#EXT-X-STREAM-INF:") }

func fetchManifest(ctx context.Context, client *http.Client, uri string, headers map[string]string, manifestURL string, policy *adapterproto.RequestPolicy) ([]byte, error) {
	var last error
	for attempt := 0; attempt < manifestAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/vnd.apple.mpegurl, application/x-mpegURL, */*")
		response, err := doMediaRequest(client, request, headers, manifestURL, policy)
		if err == nil {
			if response.StatusCode != http.StatusOK {
				response.Body.Close()
				err = fmt.Errorf("GET manifest returned %s", response.Status)
			} else {
				data, readErr := io.ReadAll(io.LimitReader(response.Body, hls.MaxManifestBytes+1))
				response.Body.Close()
				if readErr != nil {
					err = readErr
				} else if len(data) > hls.MaxManifestBytes {
					err = fmt.Errorf("manifest exceeds %d bytes", hls.MaxManifestBytes)
				} else {
					return data, nil
				}
			}
		}
		last = err
		if attempt+1 < manifestAttempts {
			if err = retryWait(ctx, attempt); err != nil {
				return nil, err
			}
		}
	}
	return nil, fmt.Errorf("manifest fetch failed after %d attempts: %w", manifestAttempts, last)
}

func (m *Manager) process(e *entry, ctx context.Context, playlist hls.MediaPlaylist) (bool, error) {
	if len(playlist.Segments) == 0 {
		if playlist.EndList {
			if err := m.update(e, func(r *domain.Recording) error {
				m.finalizePending(r, "stream ended with uncaptured segments")
				r.State = domain.StateCompleted
				now := time.Now().UTC()
				r.StoppedAt = &now
				return nil
			}); err != nil {
				return true, err
			}
			return true, nil
		}
		return false, nil
	}
	if err := m.observePlaylist(e, playlist); err != nil {
		return false, err
	}
	for _, source := range playlist.Segments {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if source.Gap {
			continue
		}
		if m.hasSequence(e, source.Sequence) {
			continue
		}
		initID := ""
		if source.Init != nil {
			id, err := m.acquireInit(ctx, e, *source.Init)
			if err != nil {
				m.markPending(e, source.Sequence, err)
				continue
			}
			initID = id
		}
		segment, err := m.acquireMedia(ctx, e, source, initID)
		if err != nil {
			m.markPending(e, source.Sequence, err)
			continue
		}
		if err = m.store.SaveSidecar(recordingID(e), segment.StoragePath, segment); err != nil {
			return false, err
		}
		if err = m.update(e, func(r *domain.Recording) error {
			t := r.Tracks["main"]
			for _, existing := range t.Segments {
				if existing.Sequence == segment.Sequence {
					return nil
				}
			}
			t.Segments = append(t.Segments, segment)
			sort.Slice(t.Segments, func(i, j int) bool { return t.Segments[i].Sequence < t.Segments[j].Sequence })
			t.PendingSequences = removeSequence(t.PendingSequences, segment.Sequence)
			r.LastError = ""
			return nil
		}); err != nil {
			return false, err
		}
	}
	if playlist.EndList {
		if err := m.update(e, func(r *domain.Recording) error {
			m.finalizePending(r, "stream ended with uncaptured segments")
			r.State = domain.StateCompleted
			now := time.Now().UTC()
			r.StoppedAt = &now
			return nil
		}); err != nil {
			return true, err
		}
		return true, nil
	}
	return false, nil
}

func (m *Manager) observePlaylist(e *entry, playlist hls.MediaPlaylist) error {
	return m.update(e, func(r *domain.Recording) error {
		t := r.Tracks["main"]
		if t == nil {
			return fmt.Errorf("main track is missing")
		}
		captured := map[uint64]bool{}
		for _, seg := range t.Segments {
			captured[seg.Sequence] = true
		}
		minSeq, maxSeq := playlist.Segments[0].Sequence, playlist.Segments[0].Sequence
		for _, seg := range playlist.Segments {
			if seg.Sequence < minSeq {
				minSeq = seg.Sequence
			}
			if seg.Sequence > maxSeq {
				maxSeq = seg.Sequence
			}
			if seg.Gap {
				addMissingRanges(r, t, seg.Sequence, seg.Sequence, "source manifest marked segment as a gap", captured)
			}
		}
		if t.HasLastObservedSequence && t.LastObservedSequence < ^uint64(0) && minSeq > t.LastObservedSequence && minSeq-t.LastObservedSequence > 1 {
			addMissingRanges(r, t, t.LastObservedSequence+1, minSeq-1, "sequence advanced past uncaptured media", captured)
		}
		for i := 1; i < len(playlist.Segments); i++ {
			prev, next := playlist.Segments[i-1].Sequence, playlist.Segments[i].Sequence
			if prev < ^uint64(0) && next > prev && next-prev > 1 {
				addMissingRanges(r, t, prev+1, next-1, "sequence skipped in manifest", captured)
			}
		}
		present := map[uint64]bool{}
		for _, seg := range playlist.Segments {
			present[seg.Sequence] = true
		}
		pending := t.PendingSequences[:0]
		for _, seq := range t.PendingSequences {
			if captured[seq] {
				continue
			}
			if !present[seq] && maxSeq > seq {
				addMissingRanges(r, t, seq, seq, "segment left the live window after retries", captured)
				continue
			}
			pending = append(pending, seq)
		}
		t.PendingSequences = pending
		if !t.HasLastObservedSequence || maxSeq > t.LastObservedSequence {
			t.LastObservedSequence = maxSeq
		}
		t.HasLastObservedSequence = true
		return nil
	})
}

func (m *Manager) acquireInit(ctx context.Context, e *entry, source hls.Map) (string, error) {
	key := source.URI
	if source.ByteRange != nil {
		key += fmt.Sprintf("|%d@%d", source.ByteRange.Length, source.ByteRange.Offset)
	}
	digest := sha256.Sum256([]byte(key))
	id := "init-" + hex.EncodeToString(digest[:8])
	var existing *domain.Segment
	e.mu.Lock()
	for i := range e.recording.Tracks["main"].InitSegments {
		if e.recording.Tracks["main"].InitSegments[i].ID == id {
			s := e.recording.Tracks["main"].InitSegments[i]
			existing = &s
			break
		}
	}
	recordingID := e.recording.ID
	e.mu.Unlock()
	if existing != nil {
		return id, nil
	}
	relative := "tracks/main/" + id + extensionFor(source.URI)
	result, err := m.downloadObject(ctx, source.URI, source.ByteRange, recordingID, relative, e.headers, e.manifestURL, e.requestPolicy)
	if err != nil {
		return "", fmt.Errorf("init segment download: %w", err)
	}
	asset := domain.Segment{ID: id, TrackID: "main", SourceURI: source.URI, ByteRange: cloneRange(source.ByteRange), StoragePath: relative, PayloadSize: result.Size, SHA256: result.SHA256, IsInit: true}
	if err = m.store.SaveSidecar(recordingID, relative, asset); err != nil {
		return "", err
	}
	if err = m.update(e, func(r *domain.Recording) error {
		t := r.Tracks["main"]
		for _, old := range t.InitSegments {
			if old.ID == id {
				return nil
			}
		}
		t.InitSegments = append(t.InitSegments, asset)
		return nil
	}); err != nil {
		return "", err
	}
	return id, nil
}

func (m *Manager) acquireMedia(ctx context.Context, e *entry, source hls.MediaSegment, initID string) (domain.Segment, error) {
	recordingID := recordingID(e)
	relative := fmt.Sprintf("tracks/main/%020d%s", source.Sequence, extensionFor(source.URI))
	result, err := m.downloadObject(ctx, source.URI, source.ByteRange, recordingID, relative, e.headers, e.manifestURL, e.requestPolicy)
	if err != nil {
		return domain.Segment{}, fmt.Errorf("sequence %d download: %w", source.Sequence, err)
	}
	segment := domain.Segment{ID: fmt.Sprintf("seg-%020d", source.Sequence), TrackID: "main", Sequence: source.Sequence, SourceURI: source.URI, Duration: source.Duration, ProgramDateTime: source.ProgramTime, InitSegmentID: initID, ByteRange: cloneRange(source.ByteRange), Discontinuity: source.Discontinuity, StoragePath: relative, PayloadSize: result.Size, SHA256: result.SHA256}
	return segment, nil
}

func (m *Manager) downloadObject(ctx context.Context, uri string, byteRange *domain.ByteRange, recordingID, relative string, headers map[string]string, manifestURL string, policy *adapterproto.RequestPolicy) (storage.PayloadResult, error) {
	if byteRange != nil && (byteRange.Length == 0 || byteRange.Length > uint64(maxPayloadBytes) || byteRange.Offset > ^uint64(0)-byteRange.Length) {
		return storage.PayloadResult{}, fmt.Errorf("byte range is invalid or exceeds the %d byte payload limit", maxPayloadBytes)
	}
	var last error
	for attempt := 0; attempt < segmentAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return storage.PayloadResult{}, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
		if err != nil {
			return storage.PayloadResult{}, err
		}
		if byteRange != nil {
			end := byteRange.Offset + byteRange.Length - 1
			request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", byteRange.Offset, end))
		}
		response, err := doMediaRequest(m.client, request, headers, manifestURL, policy)
		if err == nil {
			expected := http.StatusOK
			if byteRange != nil {
				expected = http.StatusPartialContent
			}
			if response.StatusCode != expected {
				response.Body.Close()
				err = fmt.Errorf("GET segment returned %s (expected %s)", response.Status, http.StatusText(expected))
			} else if byteRange != nil {
				if err = validateContentRange(response.Header.Get("Content-Range"), *byteRange); err != nil {
					response.Body.Close()
				} else {
					var result storage.PayloadResult
					result, err = m.store.SavePayloadExact(recordingID, relative, response.Body, maxPayloadBytes, int64(byteRange.Length))
					response.Body.Close()
					if err == nil {
						return result, nil
					}
				}
			} else {
				var result storage.PayloadResult
				result, err = m.store.SavePayload(recordingID, relative, response.Body, maxPayloadBytes)
				response.Body.Close()
				if err == nil {
					return result, nil
				}
			}
		}
		last = err
		if attempt+1 < segmentAttempts {
			if err = retryWait(ctx, attempt); err != nil {
				return storage.PayloadResult{}, err
			}
		}
	}
	return storage.PayloadResult{}, fmt.Errorf("download failed after %d attempts: %w", segmentAttempts, last)
}

func applyHeaders(request *http.Request, headers map[string]string) {
	for key, value := range headers {
		canonical := textproto.CanonicalMIMEHeaderKey(key)
		if canonical != "" {
			request.Header.Set(canonical, value)
		}
	}
}

func clearHeaders(request *http.Request, headers map[string]string) {
	for key := range headers {
		request.Header.Del(textproto.CanonicalMIMEHeaderKey(key))
	}
}

func applyHeadersForMediaURL(request *http.Request, headers map[string]string, manifestURL string, policy *adapterproto.RequestPolicy) {
	if !(adapterproto.MediaSource{ManifestURL: manifestURL, RequestPolicy: policy}).AllowsHeadersFor(request.URL) {
		return
	}
	applyHeaders(request, headers)
}

func doMediaRequest(client *http.Client, request *http.Request, headers map[string]string, manifestURL string, policy *adapterproto.RequestPolicy) (*http.Response, error) {
	applyHeadersForMediaURL(request, headers, manifestURL, policy)
	copyClient := *client
	originalCheck := client.CheckRedirect
	copyClient.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		clearHeaders(next, headers)
		if originalCheck != nil {
			if err := originalCheck(next, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		applyHeadersForMediaURL(next, headers, manifestURL, policy)
		return nil
	}
	return copyClient.Do(request)
}

func validateContentRange(value string, want domain.ByteRange) error {
	parts := strings.Fields(value)
	if len(parts) != 2 || parts[0] != "bytes" {
		return fmt.Errorf("range request refused: malformed Content-Range %q", value)
	}
	positions := strings.Split(parts[1], "/")
	bounds := strings.Split(positions[0], "-")
	if len(bounds) != 2 {
		return fmt.Errorf("range request refused: malformed Content-Range %q", value)
	}
	start, err := strconv.ParseUint(bounds[0], 10, 64)
	if err != nil {
		return fmt.Errorf("range request refused: malformed Content-Range")
	}
	end, err := strconv.ParseUint(bounds[1], 10, 64)
	if err != nil || end < start {
		return fmt.Errorf("range request refused: malformed Content-Range")
	}
	if start != want.Offset || end-start+1 != want.Length {
		return fmt.Errorf("range request refused: server returned bytes %d-%d for requested %d-%d", start, end, want.Offset, want.Offset+want.Length-1)
	}
	return nil
}

func (m *Manager) hasSequence(e *entry, sequence uint64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.recording.Tracks["main"].Segments {
		if s.Sequence == sequence {
			return true
		}
	}
	return false
}
func (m *Manager) markPending(e *entry, seq uint64, err error) {
	_ = m.update(e, func(r *domain.Recording) error {
		t := r.Tracks["main"]
		for _, p := range t.PendingSequences {
			if p == seq {
				r.LastError = err.Error()
				return nil
			}
		}
		t.PendingSequences = append(t.PendingSequences, seq)
		sort.Slice(t.PendingSequences, func(i, j int) bool { return t.PendingSequences[i] < t.PendingSequences[j] })
		r.LastError = err.Error()
		return nil
	})
}
func (m *Manager) finalizePending(r *domain.Recording, reason string) {
	for _, t := range r.Tracks {
		captured := map[uint64]bool{}
		for _, s := range t.Segments {
			captured[s.Sequence] = true
		}
		for _, seq := range t.PendingSequences {
			addMissingRanges(r, t, seq, seq, reason, captured)
		}
		t.PendingSequences = nil
	}
}

func addMissingRanges(r *domain.Recording, t *domain.Track, from, to uint64, reason string, captured map[uint64]bool) {
	if to < from {
		return
	}
	type interval struct{ start, end uint64 }
	blocked := make([]interval, 0, len(captured)+len(r.Gaps))
	for seq := range captured {
		if seq >= from && seq <= to {
			blocked = append(blocked, interval{seq, seq})
		}
	}
	for _, gap := range r.Gaps {
		if gap.TrackID != t.ID || gap.ToSequence < from || gap.FromSequence > to {
			continue
		}
		start, end := gap.FromSequence, gap.ToSequence
		if start < from {
			start = from
		}
		if end > to {
			end = to
		}
		blocked = append(blocked, interval{start, end})
	}
	sort.Slice(blocked, func(i, j int) bool { return blocked[i].start < blocked[j].start })
	cursor := from
	for _, item := range blocked {
		if item.end < cursor {
			continue
		}
		if item.start > cursor {
			r.Gaps = append(r.Gaps, domain.Gap{TrackID: t.ID, FromSequence: cursor, ToSequence: item.start - 1, DetectedAt: time.Now().UTC(), Reason: reason})
		}
		if item.end == ^uint64(0) {
			return
		}
		if item.end >= cursor {
			cursor = item.end + 1
		}
		if cursor > to {
			return
		}
	}
	if cursor <= to {
		r.Gaps = append(r.Gaps, domain.Gap{TrackID: t.ID, FromSequence: cursor, ToSequence: to, DetectedAt: time.Now().UTC(), Reason: reason})
	}
}
func gapCovers(r *domain.Recording, track string, seq uint64) bool {
	for _, g := range r.Gaps {
		if g.TrackID == track && seq >= g.FromSequence && seq <= g.ToSequence {
			return true
		}
	}
	return false
}
func removeSequence(values []uint64, target uint64) []uint64 {
	out := values[:0]
	for _, v := range values {
		if v != target {
			out = append(out, v)
		}
	}
	return out
}
func (m *Manager) recordID(e *entry) string { return recordingID(e) }
func recordingID(e *entry) string           { e.mu.Lock(); defer e.mu.Unlock(); return e.recording.ID }
func cloneRange(b *domain.ByteRange) *domain.ByteRange {
	if b == nil {
		return nil
	}
	copy := *b
	return &copy
}
func extensionFor(raw string) string {
	u, err := url.Parse(raw)
	if err == nil {
		ext := strings.ToLower(path.Ext(u.Path))
		switch ext {
		case ".ts", ".m4s", ".mp4", ".aac", ".mp3", ".vtt":
			return ext
		}
	}
	return ".bin"
}
func pollDelay(target int) time.Duration {
	delay := time.Duration(target) * time.Second / 2
	if delay < 500*time.Millisecond {
		delay = 500 * time.Millisecond
	}
	if delay > 15*time.Second {
		delay = 15 * time.Second
	}
	return delay
}
func retryWait(ctx context.Context, attempt int) error {
	delay := time.Duration(150+attempt*250) * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
