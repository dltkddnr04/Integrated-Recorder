// Package server exposes the headless control API and generated HLS VOD views.
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/example/integrated-recorder/internal/acquire"
	"github.com/example/integrated-recorder/internal/domain"
	"github.com/example/integrated-recorder/internal/storage"
)

type Server struct {
	manager *acquire.Manager
	mux     *http.ServeMux
}

func New(manager *acquire.Manager) http.Handler {
	s := &Server{manager: manager, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("GET /", s.index)
	s.mux.HandleFunc("POST /api/recordings", s.create)
	s.mux.HandleFunc("GET /api/recordings", s.list)
	s.mux.HandleFunc("GET /api/recordings/{id}", s.get)
	s.mux.HandleFunc("POST /api/recordings/{id}/stop", s.stop)
	s.mux.HandleFunc("GET /api/recordings/{id}/play/master.m3u8", s.masterPlaylist)
	s.mux.HandleFunc("GET /api/recordings/{id}/play/tracks/{track}/playlist.m3u8", s.trackPlaylist)
	s.mux.HandleFunc("GET /api/recordings/{id}/play/segments/{segmentID}", s.segment)
	return s.mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, indexHTML)
}

type createRequest struct {
	SourceURL string `json:"source_url"`
	Title     string `json:"title,omitempty"`
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request createRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(request.SourceURL) == "" {
		writeError(w, http.StatusBadRequest, "source_url is required")
		return
	}
	recording, err := s.manager.Start(r.Context(), request.SourceURL, strings.TrimSpace(request.Title))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, detail(recording))
}

type recordingSummary struct {
	ID           string                `json:"id"`
	Title        string                `json:"title,omitempty"`
	SourceURL    string                `json:"source_url"`
	State        domain.RecordingState `json:"state"`
	CreatedAt    time.Time             `json:"created_at"`
	StartedAt    time.Time             `json:"started_at"`
	StoppedAt    any                   `json:"stopped_at,omitempty"`
	TrackCount   int                   `json:"track_count"`
	SegmentCount int                   `json:"segment_count"`
	Duration     float64               `json:"duration_seconds"`
	GapCount     int                   `json:"gap_count"`
	LastError    string                `json:"last_error,omitempty"`
}

func summary(r *domain.Recording) recordingSummary {
	return recordingSummary{ID: r.ID, Title: r.Title, SourceURL: r.SourceURL, State: r.State, CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, StoppedAt: r.StoppedAt, TrackCount: len(r.Tracks), SegmentCount: r.SegmentCount(), Duration: r.Duration(), GapCount: len(r.Gaps), LastError: r.LastError}
}

type recordingDetail struct {
	*domain.Recording
	TrackCount   int     `json:"track_count"`
	SegmentCount int     `json:"segment_count"`
	Duration     float64 `json:"duration_seconds"`
}

func detail(r *domain.Recording) recordingDetail {
	return recordingDetail{Recording: r, TrackCount: len(r.Tracks), SegmentCount: r.SegmentCount(), Duration: r.Duration()}
}
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	recordings := s.manager.List()
	out := make([]recordingSummary, 0, len(recordings))
	for _, recording := range recordings {
		out = append(out, summary(recording))
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	recording, err := s.manager.Get(r.PathValue("id"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail(recording))
}
func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	recording, err := s.manager.Stop(r.PathValue("id"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail(recording))
}

func (s *Server) masterPlaylist(w http.ResponseWriter, r *http.Request) {
	recording, err := s.playableRecording(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	track := recording.Tracks["main"]
	if track == nil {
		http.NotFound(w, r)
		return
	}
	if len(track.Segments) == 0 {
		writeError(w, http.StatusConflict, "recording has no captured media segments yet")
		return
	}
	bandwidth := track.Bandwidth
	if bandwidth <= 0 {
		bandwidth = 1_000_000
	}
	playlist := fmt.Sprintf("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-STREAM-INF:BANDWIDTH=%d\n/api/recordings/%s/play/tracks/main/playlist.m3u8\n", bandwidth, recording.ID)
	writePlaylist(w, playlist)
}

func (s *Server) trackPlaylist(w http.ResponseWriter, r *http.Request) {
	recording, err := s.playableRecording(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	trackID := r.PathValue("track")
	track := recording.Tracks[trackID]
	if track == nil {
		http.NotFound(w, r)
		return
	}
	if len(track.Segments) == 0 {
		writeError(w, http.StatusConflict, "recording has no captured media segments yet")
		return
	}
	segments := append([]domain.Segment(nil), track.Segments...)
	sort.Slice(segments, func(i, j int) bool { return segments[i].Sequence < segments[j].Sequence })
	maxDuration := 0.0
	for _, segment := range segments {
		if segment.Duration > maxDuration {
			maxDuration = segment.Duration
		}
	}
	target := int(math.Ceil(maxDuration))
	if target < 1 {
		target = 1
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:%d\n", target, segments[0].Sequence)
	previousSequence := segments[0].Sequence
	previousInit := ""
	lastDiscontinuity := false
	for index, segment := range segments {
		if index > 0 && hasGapBetween(recording.Gaps, trackID, previousSequence, segment.Sequence) {
			fmt.Fprintln(&builder, "#EXT-X-DISCONTINUITY")
			lastDiscontinuity = true
		}
		if segment.Discontinuity && !lastDiscontinuity {
			fmt.Fprintln(&builder, "#EXT-X-DISCONTINUITY")
			lastDiscontinuity = true
		}
		if segment.InitSegmentID != "" && segment.InitSegmentID != previousInit {
			init, ok := findInit(track, segment.InitSegmentID)
			if !ok {
				writeError(w, http.StatusInternalServerError, "recording references a missing init segment")
				return
			}
			uri := fmt.Sprintf("/api/recordings/%s/play/segments/%s", recording.ID, init.ID)
			fmt.Fprintf(&builder, "#EXT-X-MAP:URI=%s\n", strconv.Quote(uri))
			previousInit = segment.InitSegmentID
		}
		if segment.ProgramDateTime != nil {
			fmt.Fprintf(&builder, "#EXT-X-PROGRAM-DATE-TIME:%s\n", segment.ProgramDateTime.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"))
		}
		fmt.Fprintf(&builder, "#EXTINF:%s,\n/api/recordings/%s/play/segments/%s\n", strconv.FormatFloat(segment.Duration, 'f', -1, 64), recording.ID, segment.ID)
		previousSequence = segment.Sequence
		lastDiscontinuity = false
	}
	builder.WriteString("#EXT-X-ENDLIST\n")
	writePlaylist(w, builder.String())
}

func (s *Server) segment(w http.ResponseWriter, r *http.Request) {
	recording, err := s.manager.Get(r.PathValue("id"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	var found *domain.Segment
	for _, track := range recording.Tracks {
		for i := range track.Segments {
			if track.Segments[i].ID == r.PathValue("segmentID") {
				copy := track.Segments[i]
				found = &copy
				break
			}
		}
		for i := range track.InitSegments {
			if track.InitSegments[i].ID == r.PathValue("segmentID") {
				copy := track.InitSegments[i]
				found = &copy
				break
			}
		}
	}
	if found == nil {
		http.NotFound(w, r)
		return
	}
	f, err := s.manager.Store().OpenPayload(recording.ID, found.StoragePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "stored payload is unavailable")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", mediaContentType(found.StoragePath))
	w.Header().Set("Content-Length", strconv.FormatInt(found.PayloadSize, 10))
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if _, err = io.Copy(w, f); err != nil {
		return
	}
}

func (s *Server) playableRecording(id string) (*domain.Recording, error) {
	r, err := s.manager.Get(id)
	if err != nil {
		return nil, err
	}
	if r.State == domain.StateRecording {
		return nil, fmt.Errorf("VOD playback is available after recording stops")
	}
	return r, nil
}
func hasGapBetween(gaps []domain.Gap, trackID string, previous, next uint64) bool {
	if next <= previous || previous == ^uint64(0) {
		return false
	}
	for _, gap := range gaps {
		if gap.TrackID == trackID && gap.ToSequence > previous && gap.FromSequence < next {
			return true
		}
	}
	return next-previous > 1
}
func findInit(track *domain.Track, id string) (domain.Segment, bool) {
	for _, segment := range track.InitSegments {
		if segment.ID == id {
			return segment, true
		}
	}
	return domain.Segment{}, false
}

func mediaContentType(storagePath string) string {
	switch strings.ToLower(filepath.Ext(storagePath)) {
	case ".ts":
		return "video/mp2t"
	case ".m4s":
		return "video/iso.segment"
	case ".mp4":
		return "video/mp4"
	case ".aac":
		return "audio/aac"
	default:
		return "application/octet-stream"
	}
}

func writePlaylist(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func writeStorageError(w http.ResponseWriter, err error) {
	if err == storage.ErrNotFound {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

const indexHTML = `<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Stream archive</title><body>
<h1>Stream archive</h1>
<form id="start"><label>Owncast URL <input name="source_url" type="url" required placeholder="https://owncast.example"></label> <label>Title <input name="title"></label> <button>Start recording</button></form>
<p id="message"></p><ul id="recordings"></ul><video id="player" controls playsinline style="width:min(100%,800px)"></video>
<script src="https://cdn.jsdelivr.net/npm/hls.js@1.5.17/dist/hls.min.js"></script>
<script>
const message=document.querySelector('#message'),list=document.querySelector('#recordings'),video=document.querySelector('#player');let hls;
async function api(url,opts={}){const r=await fetch(url,{headers:{'Content-Type':'application/json'},...opts});const body=await r.json();if(!r.ok)throw Error(body.error||r.statusText);return body}
async function refresh(){const items=await api('/api/recordings');list.replaceChildren(...items.map(item=>{const li=document.createElement('li');const label=document.createTextNode((item.title||item.id)+' — '+item.state+' — '+item.segment_count+' segments ');li.append(label);if(item.state==='recording'){const stop=document.createElement('button');stop.textContent='Stop';stop.onclick=async()=>{try{await api('/api/recordings/'+item.id+'/stop',{method:'POST'});refresh()}catch(e){message.textContent=e.message}};li.append(stop)}else{const play=document.createElement('button');play.textContent='Play VOD';play.onclick=()=>playRecording(item.id);li.append(play)}return li}))}
function playRecording(id){const src='/api/recordings/'+id+'/play/master.m3u8';if(hls)hls.destroy();if(video.canPlayType('application/vnd.apple.mpegurl')){video.src=src;video.play()}else if(window.Hls&&Hls.isSupported()){hls=new Hls();hls.loadSource(src);hls.attachMedia(video);hls.on(Hls.Events.MANIFEST_PARSED,()=>video.play())}else{message.textContent='This browser has no HLS playback support.'}}
document.querySelector('#start').addEventListener('submit',async e=>{e.preventDefault();const form=new FormData(e.currentTarget);try{const result=await api('/api/recordings',{method:'POST',body:JSON.stringify(Object.fromEntries(form))});message.textContent='Started '+result.id;e.currentTarget.reset();refresh()}catch(err){message.textContent=err.message}});refresh().catch(e=>message.textContent=e.message);setInterval(()=>refresh().catch(()=>{}),5000);
</script></body></html>`
