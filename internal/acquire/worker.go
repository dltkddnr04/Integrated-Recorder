package acquire

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	maxRefreshCycles = 3
)

func parseMaster(data []byte, url string) (hls.Master, error)       { return hls.ParseMaster(data, url) }
func parseMedia(data []byte, url string) (hls.MediaPlaylist, error) { return hls.ParseMedia(data, url) }
func chooseVariant(master hls.Master) (hls.Variant, error)          { return hls.SelectHighestBandwidth(master) }

// FetchError carries only fields safe for policy decisions and diagnostics.
// It intentionally never retains the request URL or a transport error, both
// of which may contain signed query data.
type FetchError struct {
	Operation         string
	HTTPStatus        int
	Retryable         bool
	URLClassification string

	statusResponse bool
}

type segmentRefreshTrigger struct {
	epoch    uint64
	sequence uint64
	fetchErr *FetchError
}

func (e *segmentRefreshTrigger) Error() string {
	if e == nil || e.fetchErr == nil {
		return "segment source requires refresh"
	}
	return e.fetchErr.Error()
}

func (e *segmentRefreshTrigger) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.fetchErr
}

func (e *FetchError) Error() string {
	if e == nil {
		return "media request failed"
	}
	if e.HTTPStatus > 0 {
		return fmt.Sprintf("%s request failed (HTTP %d)", e.Operation, e.HTTPStatus)
	}
	return fmt.Sprintf("%s request failed", e.Operation)
}

func (e *FetchError) refreshStatus() int {
	if e == nil || !e.statusResponse {
		return 0
	}
	return e.HTTPStatus
}

func isRetryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooEarly || status == http.StatusTooManyRequests || status >= 500
}

func newFetchError(operation string, status int, retryable, response bool) *FetchError {
	return &FetchError{Operation: operation, HTTPStatus: status, Retryable: retryable, URLClassification: operation, statusResponse: response}
}

func fetchManifest(ctx context.Context, client *http.Client, uri string, headers map[string]string, manifestURL string, policy *adapterproto.RequestPolicy) ([]byte, error) {
	var last *FetchError
	for attempt := 0; attempt < manifestAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
		if err != nil {
			return nil, newFetchError("manifest", 0, false, false)
		}
		request.Header.Set("Accept", "application/vnd.apple.mpegurl, application/x-mpegURL, */*")
		response, err := doMediaRequest(client, request, headers, manifestURL, policy)
		if err == nil {
			if response.StatusCode != http.StatusOK {
				status := response.StatusCode
				response.Body.Close()
				last = newFetchError("manifest", status, isRetryableStatus(status), true)
			} else if hasNonIdentityContentEncoding(response.Header) {
				response.Body.Close()
				return nil, newFetchError("manifest", 0, false, false)
			} else {
				data, readErr := io.ReadAll(io.LimitReader(response.Body, hls.MaxManifestBytes+1))
				response.Body.Close()
				if readErr != nil {
					last = newFetchError("manifest", 0, true, false)
				} else if len(data) > hls.MaxManifestBytes {
					return nil, newFetchError("manifest", 0, false, false)
				} else {
					return data, nil
				}
			}
		} else {
			last = newFetchError("manifest", 0, true, false)
		}
		if last != nil && last.Retryable && attempt+1 < manifestAttempts {
			if err = retryWait(ctx, attempt); err != nil {
				return nil, err
			}
		} else {
			break
		}
	}
	if last == nil {
		last = newFetchError("manifest", 0, false, false)
	}
	return nil, last
}

func (m *Manager) process(e *entry, ctx context.Context, playlist hls.MediaPlaylist) (bool, error) {
	if len(playlist.Segments) == 0 {
		if playlist.EndList {
			if err := m.update(e, func(r *domain.Recording) error {
				m.finalizePending(r, "stream ended with uncaptured media")
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
	epoch := currentEpoch(e)
	for index, source := range playlist.Segments {
		if ctx.Err() != nil {
			if err := m.markRemainingPending(e, epoch, playlist.Segments[index:], ctx.Err()); err != nil {
				return false, err
			}
			return false, ctx.Err()
		}
		if source.Gap {
			continue
		}
		if m.hasSequence(e, epoch, source.Sequence) {
			continue
		}
		media := currentMedia(e)
		initID := ""
		if source.Init != nil {
			id, err := m.acquireInit(ctx, e, *source.Init, epoch, source.DiscontinuitySequence, media)
			if err != nil {
				if m.shouldRefresh(media, err) {
					return false, makeSegmentRefreshTrigger(epoch, source.Sequence, err)
				}
				if pendingErr := m.markPending(e, epoch, source.Sequence, err); pendingErr != nil {
					return false, pendingErr
				}
				return false, nil
			}
			initID = id
		}
		ordinal, err := nextArchiveOrdinal(e)
		if err != nil {
			return false, err
		}
		firstInEpoch := epoch > 0 && !hasCapturedEpoch(e, epoch)
		segment, err := m.acquireMedia(ctx, e, source, epoch, ordinal, initID, firstInEpoch, media)
		if err != nil {
			if m.shouldRefresh(media, err) {
				return false, makeSegmentRefreshTrigger(epoch, source.Sequence, err)
			}
			if pendingErr := m.markPending(e, epoch, source.Sequence, err); pendingErr != nil {
				return false, pendingErr
			}
			return false, nil
		}
		if err = m.store.SaveSidecar(recordingID(e), segment.StoragePath, segment); err != nil {
			return false, err
		}
		if err = m.update(e, func(r *domain.Recording) error {
			t := r.Tracks["main"]
			for _, existing := range t.Segments {
				if existing.SourceEpoch == segment.SourceEpoch && existing.Sequence == segment.Sequence {
					return nil
				}
			}
			t.Segments = append(t.Segments, segment)
			sort.Slice(t.Segments, func(i, j int) bool {
				if t.Segments[i].ArchiveOrdinal != t.Segments[j].ArchiveOrdinal {
					return t.Segments[i].ArchiveOrdinal < t.Segments[j].ArchiveOrdinal
				}
				if t.Segments[i].SourceEpoch != t.Segments[j].SourceEpoch {
					return t.Segments[i].SourceEpoch < t.Segments[j].SourceEpoch
				}
				return t.Segments[i].Sequence < t.Segments[j].Sequence
			})
			t.NextArchiveOrdinal = segment.ArchiveOrdinal + 1
			t.PendingSegments = removePending(t.PendingSegments, segment.SourceEpoch, segment.Sequence)
			if segment.SourceEpoch == 0 {
				t.PendingSequences = removeSequence(t.PendingSequences, segment.Sequence)
			}
			r.LastError = ""
			return nil
		}); err != nil {
			return false, err
		}
	}
	if playlist.EndList {
		if err := m.update(e, func(r *domain.Recording) error {
			m.finalizePending(r, "stream ended with uncaptured media")
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
	if len(playlist.Segments) == 0 {
		return nil
	}
	return m.update(e, func(r *domain.Recording) error {
		t := r.Tracks["main"]
		if t == nil {
			return fmt.Errorf("main track is missing")
		}
		minSeq, maxSeq := playlist.Segments[0].Sequence, playlist.Segments[0].Sequence
		for _, seg := range playlist.Segments {
			if seg.Sequence < minSeq {
				minSeq = seg.Sequence
			}
			if seg.Sequence > maxSeq {
				maxSeq = seg.Sequence
			}
		}
		sequenceReset := t.HasLastObservedSequence && maxSeq < t.LastObservedSequence
		// A reset may catch up to or pass the old high-water mark between polls.
		// An overlapping sequence with a different discontinuity sequence is a
		// source identity change, even when maxSeq no longer regresses.
		if !sequenceReset {
			for _, incoming := range playlist.Segments {
				var latest *domain.Segment
				for i := range t.Segments {
					captured := &t.Segments[i]
					if captured.SourceEpoch == t.SourceEpoch && captured.Sequence == incoming.Sequence && latest == nil {
						latest = captured
					}
				}
				if latest == nil {
					continue
				}
				identityChanged := latest.DiscontinuitySequence != incoming.DiscontinuitySequence
				if latest.SourceURI != "" && incoming.URI != "" && sourceURIIdentity(latest.SourceURI) != sourceURIIdentity(incoming.URI) {
					identityChanged = true
				}
				if latest.ProgramDateTime != nil && incoming.ProgramTime != nil && !latest.ProgramDateTime.Equal(*incoming.ProgramTime) {
					identityChanged = true
				}
				if identityChanged {
					sequenceReset = true
					break
				}
			}
		}
		if sequenceReset {
			m.finalizePendingEpoch(r, t, t.SourceEpoch, "source sequence reset before pending media was captured")
			if t.SourceEpoch == ^uint64(0) {
				return errors.New("source sequence epoch overflow")
			}
			t.SourceEpoch++
			t.HasLastObservedSequence = false
		}
		epoch := t.SourceEpoch
		captured := capturedSequenceSet(t, epoch)
		for _, seg := range playlist.Segments {
			if seg.Gap {
				addMissingRanges(r, t, epoch, seg.Sequence, seg.Sequence, "source manifest marked segment as a gap", captured)
			}
		}
		if t.HasLastObservedSequence && t.LastObservedSequence < ^uint64(0) && minSeq > t.LastObservedSequence && minSeq-t.LastObservedSequence > 1 {
			addMissingRanges(r, t, epoch, t.LastObservedSequence+1, minSeq-1, "sequence advanced past uncaptured media", captured)
		}
		for i := 1; i < len(playlist.Segments); i++ {
			prev, next := playlist.Segments[i-1].Sequence, playlist.Segments[i].Sequence
			if prev < ^uint64(0) && next > prev && next-prev > 1 {
				addMissingRanges(r, t, epoch, prev+1, next-1, "sequence skipped in manifest", captured)
			}
		}
		present := map[uint64]bool{}
		for _, seg := range playlist.Segments {
			present[seg.Sequence] = true
		}
		pending := t.PendingSegments[:0]
		for _, item := range t.PendingSegments {
			if item.SourceEpoch != epoch {
				pending = append(pending, item)
				continue
			}
			if captured[item.Sequence] {
				continue
			}
			if !present[item.Sequence] && maxSeq > item.Sequence {
				addMissingRanges(r, t, epoch, item.Sequence, item.Sequence, "media left the live window after retries", captured)
				continue
			}
			pending = append(pending, item)
		}
		t.PendingSegments = pending
		if epoch == 0 {
			legacyPending := t.PendingSequences[:0]
			for _, seq := range t.PendingSequences {
				if captured[seq] {
					continue
				}
				if !present[seq] && maxSeq > seq {
					addMissingRanges(r, t, epoch, seq, seq, "media left the live window after retries", captured)
					continue
				}
				legacyPending = append(legacyPending, seq)
			}
			t.PendingSequences = legacyPending
		}
		if !t.HasLastObservedSequence || maxSeq > t.LastObservedSequence {
			t.LastObservedSequence = maxSeq
		}
		t.HasLastObservedSequence = true
		return nil
	})
}

// sourceURIIdentity compares the stable resource portion of a media URI.
// Query strings and fragments are omitted because adapters commonly refresh
// signed fetch parameters while the same segment remains in a sliding window.
// The original complete URI is still retained for fetching and provenance.
func sourceURIIdentity(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return raw
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	if u.User != nil {
		host = u.User.String() + "@" + host
	}
	return scheme + "://" + host + u.EscapedPath()
}

func (m *Manager) acquireInit(ctx context.Context, e *entry, source hls.Map, epoch, discontinuitySequence uint64, media adapterproto.MediaSource) (string, error) {
	key := source.URI
	key += fmt.Sprintf("|epoch=%d|discontinuity=%d", epoch, discontinuitySequence)
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
	result, err := m.downloadObject(ctx, source.URI, source.ByteRange, recordingID, relative, media)
	if err != nil {
		return "", fmt.Errorf("init segment download: %w", err)
	}
	asset := domain.Segment{ID: id, TrackID: "main", SourceEpoch: epoch, DiscontinuitySequence: discontinuitySequence, SourceURI: source.URI, ByteRange: cloneRange(source.ByteRange), StoragePath: relative, PayloadSize: result.Size, SHA256: result.SHA256, IsInit: true}
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

func (m *Manager) acquireMedia(ctx context.Context, e *entry, source hls.MediaSegment, epoch, ordinal uint64, initID string, firstInEpoch bool, media adapterproto.MediaSource) (domain.Segment, error) {
	recordingID := recordingID(e)
	relative := fmt.Sprintf("tracks/main/%020d%s", ordinal, extensionFor(source.URI))
	result, err := m.downloadObject(ctx, source.URI, source.ByteRange, recordingID, relative, media)
	if err != nil {
		return domain.Segment{}, err
	}
	segment := domain.Segment{ID: fmt.Sprintf("seg-%020d", ordinal), TrackID: "main", Sequence: source.Sequence, SourceEpoch: epoch, DiscontinuitySequence: source.DiscontinuitySequence, ArchiveOrdinal: ordinal, SourceURI: source.URI, Duration: source.Duration, ProgramDateTime: source.ProgramTime, InitSegmentID: initID, ByteRange: cloneRange(source.ByteRange), Discontinuity: source.Discontinuity || firstInEpoch, StoragePath: relative, PayloadSize: result.Size, SHA256: result.SHA256}
	return segment, nil
}

func (m *Manager) downloadObject(ctx context.Context, uri string, byteRange *domain.ByteRange, recordingID, relative string, media adapterproto.MediaSource) (storage.PayloadResult, error) {
	if byteRange != nil && (byteRange.Length == 0 || byteRange.Length > uint64(maxPayloadBytes) || byteRange.Offset > ^uint64(0)-byteRange.Length) {
		return storage.PayloadResult{}, fmt.Errorf("byte range is invalid or exceeds the %d byte payload limit", maxPayloadBytes)
	}
	var last *FetchError
	for attempt := 0; attempt < segmentAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return storage.PayloadResult{}, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
		if err != nil {
			return storage.PayloadResult{}, newFetchError("segment", 0, false, false)
		}
		if byteRange != nil {
			end := byteRange.Offset + byteRange.Length - 1
			request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", byteRange.Offset, end))
		}
		response, err := doMediaRequest(m.client, request, media.Headers, media.ManifestURL, media.RequestPolicy)
		if err == nil {
			expected := http.StatusOK
			if byteRange != nil {
				expected = http.StatusPartialContent
			}
			if response.StatusCode != expected {
				status := response.StatusCode
				response.Body.Close()
				last = newFetchError("segment", status, isRetryableStatus(status), true)
			} else if hasNonIdentityContentEncoding(response.Header) {
				response.Body.Close()
				last = newFetchError("segment", 0, false, false)
			} else if byteRange != nil {
				if err = validateContentRange(response.Header.Get("Content-Range"), *byteRange); err != nil {
					response.Body.Close()
					last = newFetchError("segment", response.StatusCode, false, false)
				} else {
					var result storage.PayloadResult
					tracked := &responseBodyReadTracker{reader: response.Body}
					result, err = m.store.SavePayloadExact(recordingID, relative, tracked, maxPayloadBytes, int64(byteRange.Length))
					response.Body.Close()
					if err == nil {
						return result, nil
					}
					last = newFetchError("segment", 0, tracked.err != nil || errors.Is(err, storage.ErrPayloadSizeMismatch), false)
				}
			} else {
				var result storage.PayloadResult
				tracked := &responseBodyReadTracker{reader: response.Body}
				result, err = m.store.SavePayload(recordingID, relative, tracked, maxPayloadBytes)
				response.Body.Close()
				if err == nil {
					return result, nil
				}
				last = newFetchError("segment", 0, tracked.err != nil, false)
			}
		} else {
			last = newFetchError("segment", 0, true, false)
		}
		if last != nil && last.Retryable && attempt+1 < segmentAttempts {
			if err = retryWait(ctx, attempt); err != nil {
				return storage.PayloadResult{}, err
			}
		} else {
			break
		}
	}
	if last == nil {
		last = newFetchError("segment", 0, false, false)
	}
	return storage.PayloadResult{}, last
}

type responseBodyReadTracker struct {
	reader io.Reader
	err    error
}

func (r *responseBodyReadTracker) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		r.err = err
	}
	return n, err
}

func (m *Manager) runWorker(ctx context.Context, e *entry, media adapterproto.MediaSource) {
	defer close(e.done)
	defer func() {
		e.mu.Lock()
		e.cancel = nil
		e.mu.Unlock()
	}()
	defer func() {
		if ctx.Err() == nil {
			return
		}
		if err := m.update(e, func(r *domain.Recording) error {
			if r.State == domain.StateRecording {
				m.finalizePending(r, "recording stopped before pending media could be captured")
				r.State = domain.StateStopped
				now := time.Now().UTC()
				r.StoppedAt = &now
			}
			return nil
		}); err != nil {
			m.setTerminalError(e, errors.New("recording could not be stopped durably"))
		}
	}()

	selectedURL := media.ManifestURL
	firstPlaylist := true
	refreshCycles := 0
	proactiveRefreshes := 0
	for ctx.Err() == nil {
		media = currentMedia(e)
		if due, _ := proactiveRefreshDue(media, time.Now()); due {
			if proactiveRefreshes >= maxRefreshCycles {
				m.fail(e, errors.New("media refresh retry limit reached"))
				return
			}
			refreshed, err := m.refreshMedia(ctx, e, media)
			if err != nil {
				m.fail(e, errors.New("media source refresh failed"))
				return
			}
			proactiveRefreshes++
			media = refreshed
			selectedURL = media.ManifestURL
			firstPlaylist = true
			if stillDue, _ := proactiveRefreshDue(media, time.Now()); stillDue {
				// The adapter did not move the source outside its own refresh
				// window. Do not fetch a manifest it still declares due; retry
				// refresh only, bounded by proactiveRefreshes above.
				continue
			}
			proactiveRefreshes = 0
		} else {
			proactiveRefreshes = 0
		}

		body, err := fetchManifest(ctx, m.client, selectedURL, media.Headers, media.ManifestURL, media.RequestPolicy)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if m.shouldRefresh(media, err) {
				if refreshCycles >= maxRefreshCycles {
					m.fail(e, errors.New("media refresh retry limit reached"))
					return
				}
				refreshed, refreshErr := m.refreshMedia(ctx, e, media)
				if refreshErr == nil {
					refreshCycles++
					if due, _ := proactiveRefreshDue(refreshed, time.Now()); due {
						proactiveRefreshes++
					} else {
						proactiveRefreshes = 0
					}
					media = refreshed
					selectedURL = media.ManifestURL
					firstPlaylist = true
					continue
				}
				m.fail(e, errors.New("media source refresh failed"))
				return
			}
			m.fail(e, err)
			return
		}
		if err = m.saveSnapshot(e, "main", selectedURL, body); err != nil {
			m.fail(e, err)
			return
		}
		if firstPlaylist && hls.IsMasterPlaylist(body) {
			master, parseErr := hlsParseMaster(body, selectedURL)
			if parseErr != nil {
				m.fail(e, parseErr)
				return
			}
			variant, selectErr := selectVariant(master)
			if selectErr != nil {
				m.fail(e, selectErr)
				return
			}
			selectedURL = variant.URI
			if err = m.update(e, func(r *domain.Recording) error {
				t := r.Tracks["main"]
				if t == nil {
					return errors.New("main track is missing")
				}
				t.SourcePlaylistURL = selectedURL
				t.Bandwidth = variant.Bandwidth
				return nil
			}); err != nil {
				m.fail(e, err)
				return
			}
			continue
		}
		firstPlaylist = false
		playlist, err := hlsParseMedia(body, selectedURL)
		if err != nil {
			m.fail(e, err)
			return
		}
		done, err := m.process(e, ctx, playlist)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var trigger *segmentRefreshTrigger
			if errors.As(err, &trigger) {
				if refreshCycles >= maxRefreshCycles {
					if pendingErr := m.markPending(e, trigger.epoch, trigger.sequence, trigger); pendingErr != nil {
						m.fail(e, pendingErr)
						return
					}
					m.fail(e, errors.New("media refresh retry limit reached"))
					return
				}
				refreshed, refreshErr := m.refreshMedia(ctx, e, media)
				if refreshErr == nil {
					refreshCycles++
					if due, _ := proactiveRefreshDue(refreshed, time.Now()); due {
						proactiveRefreshes++
					} else {
						proactiveRefreshes = 0
					}
					if pendingErr := m.markPending(e, trigger.epoch, trigger.sequence, trigger); pendingErr != nil {
						m.fail(e, pendingErr)
						return
					}
					media = refreshed
					selectedURL = media.ManifestURL
					firstPlaylist = true
					continue
				}
				if pendingErr := m.markPending(e, trigger.epoch, trigger.sequence, trigger); pendingErr != nil {
					m.fail(e, pendingErr)
					return
				}
				m.fail(e, errors.New("media source refresh failed"))
				return
			}
			m.fail(e, err)
			return
		}
		if done {
			return
		}
		refreshCycles = 0
		if err = wait(ctx, pollDelay(playlist.TargetDuration)); err != nil {
			return
		}
	}
}

func (m *Manager) refreshMedia(ctx context.Context, e *entry, current adapterproto.MediaSource) (adapterproto.MediaSource, error) {
	e.mu.Lock()
	adapterID, resource := e.adapterID, cloneResourceRef(e.resource)
	e.mu.Unlock()
	var candidate adapterproto.MediaSource
	var commit func() error
	var err error
	if preparer, ok := m.resolver.(RefreshPreparer); ok {
		candidate, commit, err = preparer.PrepareRefresh(ctx, adapterID, resource, cloneMediaSource(current))
	} else if refresher, ok := m.resolver.(Refresher); ok {
		candidate, err = refresher.Refresh(ctx, adapterID, resource, cloneMediaSource(current))
	} else {
		return adapterproto.MediaSource{}, errors.New("adapter does not support refresh")
	}
	if err != nil {
		return adapterproto.MediaSource{}, errors.New("adapter refresh failed")
	}
	if err = adapterproto.ValidateMediaSource(candidate, []string{"hls"}); err != nil {
		return adapterproto.MediaSource{}, errors.New("adapter returned invalid refreshed media source")
	}
	if err = m.validate(ctx, candidate.ManifestURL); err != nil {
		return adapterproto.MediaSource{}, errors.New("refreshed media URL is invalid")
	}
	// A later source may contain signed URLs even if an earlier source was
	// classified public. Keep the recording-level API redaction conservative:
	// once a source is sensitive, never downgrade the archive classification.
	if candidate.SourceURIClassification() == "sensitive" {
		if err = m.update(e, func(r *domain.Recording) error {
			r.SourceURIClassification = "sensitive"
			return nil
		}); err != nil {
			return adapterproto.MediaSource{}, errors.New("recording URI classification could not be saved")
		}
	}
	if commit != nil {
		if err = commit(); err != nil {
			return adapterproto.MediaSource{}, errors.New("adapter refresh state could not be committed")
		}
	}
	copy := cloneMediaSource(candidate)
	e.mu.Lock()
	e.media = copy
	e.mu.Unlock()
	return cloneMediaSource(copy), nil
}

func (m *Manager) shouldRefresh(media adapterproto.MediaSource, err error) bool {
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) || fetchErr.refreshStatus() == 0 || media.RefreshPolicy == nil {
		return false
	}
	for _, status := range media.RefreshPolicy.OnHTTPStatus {
		if status == fetchErr.refreshStatus() {
			return true
		}
	}
	return false
}

func makeSegmentRefreshTrigger(epoch, sequence uint64, err error) *segmentRefreshTrigger {
	var fetchErr *FetchError
	if errors.As(err, &fetchErr) {
		return &segmentRefreshTrigger{epoch: epoch, sequence: sequence, fetchErr: fetchErr}
	}
	return &segmentRefreshTrigger{epoch: epoch, sequence: sequence, fetchErr: newFetchError("segment", 0, false, false)}
}

func expiryKey(media adapterproto.MediaSource) string {
	if media.RefreshPolicy == nil || media.RefreshPolicy.ExpiresAt == nil {
		return ""
	}
	return media.RefreshPolicy.ExpiresAt.UTC().Format(time.RFC3339Nano)
}

func refreshAttemptKey(current, refreshed adapterproto.MediaSource, now time.Time, previous string) string {
	// Retained as a compatibility helper for existing unit callers. Runtime
	// refresh limiting is attempt-count based because adapters may return a new
	// expiry on every call without making the source usable for longer.
	_ = current
	_ = refreshed
	_ = now
	return previous
}

func proactiveRefreshDue(media adapterproto.MediaSource, now time.Time) (bool, string) {
	policy := media.RefreshPolicy
	if policy == nil || policy.ExpiresAt == nil {
		return false, ""
	}
	refreshAt := policy.ExpiresAt.Add(-time.Duration(policy.RefreshBeforeSeconds) * time.Second)
	if now.Before(refreshAt) {
		return false, ""
	}
	return true, policy.ExpiresAt.UTC().Format(time.RFC3339Nano)
}

func currentMedia(e *entry) adapterproto.MediaSource {
	e.mu.Lock()
	defer e.mu.Unlock()
	return cloneMediaSource(e.media)
}

func currentEpoch(e *entry) uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track := e.recording.Tracks["main"]; track != nil {
		return track.SourceEpoch
	}
	return 0
}

func hasCapturedEpoch(e *entry, epoch uint64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	track := e.recording.Tracks["main"]
	if track == nil {
		return false
	}
	for _, segment := range track.Segments {
		if segment.SourceEpoch == epoch {
			return true
		}
	}
	return false
}

func removePending(values []domain.PendingSequence, epoch, sequence uint64) []domain.PendingSequence {
	out := values[:0]
	for _, value := range values {
		if value.SourceEpoch != epoch || value.Sequence != sequence {
			out = append(out, value)
		}
	}
	return out
}

func nextArchiveOrdinal(e *entry) (uint64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	track := e.recording.Tracks["main"]
	if track == nil {
		return 0, errors.New("main track is missing")
	}
	if track.NextArchiveOrdinal > 0 {
		if track.NextArchiveOrdinal == ^uint64(0) {
			return 0, errors.New("archive ordinal overflow")
		}
		return track.NextArchiveOrdinal, nil
	}
	var maximum uint64
	for _, segment := range track.Segments {
		if segment.ArchiveOrdinal > maximum {
			maximum = segment.ArchiveOrdinal
		}
	}
	if maximum > 0 {
		if maximum == ^uint64(0) {
			return 0, errors.New("archive ordinal overflow")
		}
		return maximum + 1, nil
	}
	if uint64(len(track.Segments)) >= ^uint64(0)-1 {
		return 0, errors.New("archive ordinal overflow")
	}
	return uint64(len(track.Segments)) + 1, nil
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
	request.Header.Set("Accept-Encoding", "identity")
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
		next.Header.Set("Accept-Encoding", "identity")
		return nil
	}
	return copyClient.Do(request)
}

func hasNonIdentityContentEncoding(header http.Header) bool {
	for _, value := range header.Values("Content-Encoding") {
		for _, encoding := range strings.Split(value, ",") {
			encoding = strings.TrimSpace(encoding)
			if encoding != "" && !strings.EqualFold(encoding, "identity") {
				return true
			}
		}
	}
	return false
}

func validateContentRange(value string, want domain.ByteRange) error {
	malformed := errors.New("range request refused: malformed Content-Range")
	if want.Length == 0 || want.Offset > ^uint64(0)-want.Length {
		return errors.New("range request refused: requested range overflows")
	}
	if strings.Count(value, " ") != 1 {
		return malformed
	}
	unit, rest, found := strings.Cut(value, " ")
	if !found || unit != "bytes" || rest == "" || strings.ContainsAny(rest, " \t\r\n") {
		return malformed
	}
	positions := strings.Split(rest, "/")
	if len(positions) != 2 || !decimalUint(positions[1]) {
		return malformed
	}
	bounds := strings.Split(positions[0], "-")
	if len(bounds) != 2 || !decimalUint(bounds[0]) || !decimalUint(bounds[1]) {
		return malformed
	}
	start, err := strconv.ParseUint(bounds[0], 10, 64)
	if err != nil {
		return malformed
	}
	end, err := strconv.ParseUint(bounds[1], 10, 64)
	if err != nil || end < start {
		return malformed
	}
	total, err := strconv.ParseUint(positions[1], 10, 64)
	if err != nil || total <= end {
		return malformed
	}
	wantEnd := want.Offset + want.Length - 1
	if start != want.Offset || end != wantEnd {
		return errors.New("range request refused: server returned a different byte range")
	}
	return nil
}

func decimalUint(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (m *Manager) hasSequence(e *entry, epoch, sequence uint64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	track := e.recording.Tracks["main"]
	if track == nil {
		return false
	}
	for _, s := range track.Segments {
		if s.SourceEpoch == epoch && s.Sequence == sequence {
			return true
		}
	}
	return false
}
func (m *Manager) markPending(e *entry, epoch, seq uint64, err error) error {
	return m.update(e, func(r *domain.Recording) error {
		t := r.Tracks["main"]
		if t == nil {
			return errors.New("main track is missing")
		}
		for _, pending := range t.PendingSegments {
			if pending.SourceEpoch == epoch && pending.Sequence == seq {
				r.LastError = safeFailureDescription(err)
				return nil
			}
		}
		t.PendingSegments = append(t.PendingSegments, domain.PendingSequence{SourceEpoch: epoch, Sequence: seq})
		sort.Slice(t.PendingSegments, func(i, j int) bool {
			if t.PendingSegments[i].SourceEpoch != t.PendingSegments[j].SourceEpoch {
				return t.PendingSegments[i].SourceEpoch < t.PendingSegments[j].SourceEpoch
			}
			return t.PendingSegments[i].Sequence < t.PendingSegments[j].Sequence
		})
		r.LastError = safeFailureDescription(err)
		return nil
	})
}

// markRemainingPending preserves the manifest's known media inventory when a
// stop arrives between segment downloads. The worker's terminal update will
// turn these entries into epoch-scoped gaps.
func (m *Manager) markRemainingPending(e *entry, epoch uint64, segments []hls.MediaSegment, err error) error {
	if len(segments) == 0 {
		return nil
	}
	return m.update(e, func(r *domain.Recording) error {
		t := r.Tracks["main"]
		if t == nil {
			return errors.New("main track is missing")
		}
		captured := capturedSequenceSet(t, epoch)
		pending := make(map[uint64]bool, len(t.PendingSegments))
		for _, item := range t.PendingSegments {
			if item.SourceEpoch == epoch {
				pending[item.Sequence] = true
			}
		}
		for _, source := range segments {
			if source.Gap || captured[source.Sequence] || pending[source.Sequence] {
				continue
			}
			t.PendingSegments = append(t.PendingSegments, domain.PendingSequence{SourceEpoch: epoch, Sequence: source.Sequence})
			pending[source.Sequence] = true
		}
		sort.Slice(t.PendingSegments, func(i, j int) bool {
			if t.PendingSegments[i].SourceEpoch != t.PendingSegments[j].SourceEpoch {
				return t.PendingSegments[i].SourceEpoch < t.PendingSegments[j].SourceEpoch
			}
			return t.PendingSegments[i].Sequence < t.PendingSegments[j].Sequence
		})
		r.LastError = safeFailureDescription(err)
		return nil
	})
}

func (m *Manager) finalizePending(r *domain.Recording, reason string) {
	for _, t := range r.Tracks {
		epochs := map[uint64]struct{}{t.SourceEpoch: {}}
		for _, pending := range t.PendingSegments {
			epochs[pending.SourceEpoch] = struct{}{}
		}
		if len(t.PendingSequences) > 0 {
			epochs[0] = struct{}{}
		}
		ordered := make([]uint64, 0, len(epochs))
		for epoch := range epochs {
			ordered = append(ordered, epoch)
		}
		sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
		for _, epoch := range ordered {
			m.finalizePendingEpoch(r, t, epoch, reason)
		}
	}
}

func (m *Manager) finalizePendingEpoch(r *domain.Recording, t *domain.Track, epoch uint64, reason string) {
	captured := capturedSequenceSet(t, epoch)
	remaining := t.PendingSegments[:0]
	for _, item := range t.PendingSegments {
		if item.SourceEpoch != epoch {
			remaining = append(remaining, item)
			continue
		}
		addMissingRanges(r, t, epoch, item.Sequence, item.Sequence, reason, captured)
	}
	t.PendingSegments = remaining
	if epoch == 0 {
		for _, seq := range t.PendingSequences {
			addMissingRanges(r, t, epoch, seq, seq, reason, captured)
		}
		t.PendingSequences = nil
	}
}

func capturedSequenceSet(t *domain.Track, epoch uint64) map[uint64]bool {
	captured := make(map[uint64]bool)
	for _, segment := range t.Segments {
		if segment.SourceEpoch == epoch {
			captured[segment.Sequence] = true
		}
	}
	return captured
}

func addMissingRanges(r *domain.Recording, t *domain.Track, epoch, from, to uint64, reason string, captured map[uint64]bool) {
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
		if gap.TrackID != t.ID || gap.SourceEpoch != epoch || gap.ToSequence < from || gap.FromSequence > to {
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
			r.Gaps = append(r.Gaps, domain.Gap{TrackID: t.ID, SourceEpoch: epoch, FromSequence: cursor, ToSequence: item.start - 1, DetectedAt: time.Now().UTC(), Reason: reason})
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
		r.Gaps = append(r.Gaps, domain.Gap{TrackID: t.ID, SourceEpoch: epoch, FromSequence: cursor, ToSequence: to, DetectedAt: time.Now().UTC(), Reason: reason})
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
	if target <= 1 {
		return 500 * time.Millisecond
	}
	if target >= 30 {
		return 15 * time.Second
	}
	return time.Duration(target) * time.Second / 2
}
func retryWait(ctx context.Context, attempt int) error {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 7 {
		attempt = 7
	}
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
	if d < 0 {
		d = 0
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
