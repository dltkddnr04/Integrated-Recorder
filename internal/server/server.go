// Package server exposes the headless control API and generated HLS VOD views.
package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/acquire"
	"github.com/dltkddnr04/integrated-recorder/internal/adapterhost"
	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
	"github.com/dltkddnr04/integrated-recorder/internal/domain"
	"github.com/dltkddnr04/integrated-recorder/internal/pluginconfig"
	"github.com/dltkddnr04/integrated-recorder/internal/storage"
)

type Server struct {
	manager        *acquire.Manager
	adapters       *adapterhost.Host
	configs        *pluginconfig.Service
	mux            *http.ServeMux
	mu             sync.Mutex
	workflowTitles map[string]string
}

func New(manager *acquire.Manager, adapters *adapterhost.Host, configs *pluginconfig.Service) http.Handler {
	s := &Server{manager: manager, adapters: adapters, configs: configs, mux: http.NewServeMux(), workflowTitles: map[string]string{}}
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("GET /", s.index)
	s.mux.HandleFunc("POST /api/recordings", s.create)
	s.mux.HandleFunc("GET /api/resolve-workflows/{id}", s.workflowGet)
	s.mux.HandleFunc("POST /api/resolve-workflows/{id}/continue", s.workflowContinue)
	s.mux.HandleFunc("GET /api/adapters", s.adapterList)
	s.mux.HandleFunc("GET /api/adapters/{id}", s.adapterGet)
	s.mux.HandleFunc("GET /api/adapters/{id}/schema", s.adapterSchema)
	s.mux.HandleFunc("GET /api/adapters/{id}/config", s.configGet)
	s.mux.HandleFunc("PUT /api/adapters/{id}/config", s.configPut)
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
	AdapterID string                    `json:"adapter_id"`
	Input     json.RawMessage           `json:"input"`
	Resource  *adapterproto.ResourceRef `json:"resource,omitempty"`
	Title     string                    `json:"title,omitempty"`
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
	if strings.TrimSpace(request.AdapterID) == "" || len(request.Input) == 0 {
		writeError(w, http.StatusBadRequest, "adapter_id and input are required")
		return
	}
	if s.adapters == nil {
		writeError(w, http.StatusServiceUnavailable, "adapter resolver is unavailable")
		return
	}
	progress, err := s.adapters.BeginResolution(r.Context(), request.AdapterID, request.Input, request.Resource)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if progress.State != "resolved" || progress.Media == nil {
		s.mu.Lock()
		s.workflowTitles[progress.WorkflowID] = strings.TrimSpace(request.Title)
		s.mu.Unlock()
		writeJSON(w, http.StatusAccepted, progress)
		return
	}
	recording, err := s.manager.StartResolved(r.Context(), progress.AdapterID, *progress.Media, progress.Resource, strings.TrimSpace(request.Title), &progress.Provenance)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, detail(recording))
}

func (s *Server) workflowGet(w http.ResponseWriter, r *http.Request) {
	if s.adapters == nil {
		writeError(w, http.StatusServiceUnavailable, "adapter workflows are unavailable")
		return
	}
	progress, err := s.adapters.Workflow(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}
	writeJSON(w, http.StatusOK, progress)
}

type workflowContinueRequest struct {
	Values  map[string]json.RawMessage `json:"values,omitempty"`
	Secrets map[string]string          `json:"secrets,omitempty"`
	Persist bool                       `json:"persist,omitempty"`
}

func (s *Server) workflowContinue(w http.ResponseWriter, r *http.Request) {
	if s.adapters == nil {
		writeError(w, http.StatusServiceUnavailable, "adapter workflows are unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request workflowContinueRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid workflow continuation")
		return
	}
	progress, err := s.adapters.ContinueResolution(r.Context(), r.PathValue("id"), request.Values, request.Secrets, request.Persist)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if progress.State != "resolved" || progress.Media == nil {
		writeJSON(w, http.StatusAccepted, progress)
		return
	}
	s.mu.Lock()
	title := s.workflowTitles[progress.WorkflowID]
	delete(s.workflowTitles, progress.WorkflowID)
	s.mu.Unlock()
	recording, err := s.manager.StartResolved(r.Context(), progress.AdapterID, *progress.Media, progress.Resource, title, &progress.Provenance)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, detail(recording))
}

func (s *Server) adapterList(w http.ResponseWriter, r *http.Request) {
	if s.adapters == nil {
		writeJSON(w, http.StatusOK, []adapterhost.Adapter{})
		return
	}
	writeJSON(w, http.StatusOK, s.adapters.List())
}
func (s *Server) adapterGet(w http.ResponseWriter, r *http.Request) {
	if s.adapters == nil {
		writeError(w, http.StatusNotFound, "adapter not found")
		return
	}
	adapter, err := s.adapters.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "adapter not found")
		return
	}
	writeJSON(w, http.StatusOK, adapter)
}
func (s *Server) adapterSchema(w http.ResponseWriter, r *http.Request) {
	if s.adapters == nil {
		writeError(w, http.StatusNotFound, "adapter not found")
		return
	}
	adapter, err := s.adapters.Get(r.PathValue("id"))
	if err != nil || adapter.Descriptor == nil {
		writeError(w, http.StatusNotFound, "adapter schema is unavailable")
		return
	}
	resource, err := resourceQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid resource scope")
		return
	}
	configurationSchema, err := s.adapters.Schema(r.PathValue("id"), resource)
	if err != nil {
		writeError(w, http.StatusNotFound, "adapter configuration schema is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"input_schema": adapter.Descriptor.InputSchema, "configuration_schema": configurationSchema, "resource_types": adapter.Descriptor.ResourceTypes, "media_types": adapter.Descriptor.MediaTypes})
}

type configPutRequest struct {
	Resource     *adapterproto.ResourceRef  `json:"resource,omitempty"`
	Values       map[string]json.RawMessage `json:"values,omitempty"`
	Secrets      map[string]string          `json:"secrets,omitempty"`
	ClearSecrets []string                   `json:"clear_secrets,omitempty"`
}
type secretState struct {
	Configured bool `json:"configured"`
}
type maskedConfig struct {
	Values  map[string]json.RawMessage `json:"values"`
	Secrets map[string]secretState     `json:"secrets"`
}

type configScopeView struct {
	Values  map[string]json.RawMessage `json:"values"`
	Secrets map[string]secretState     `json:"secrets"`
}
type configAPIView struct {
	Schema        adapterproto.Schema        `json:"schema"`
	Values        map[string]json.RawMessage `json:"values"`
	Secrets       map[string]secretState     `json:"secrets"`
	Stored        configScopeView            `json:"stored"`
	Effective     configScopeView            `json:"effective"`
	ValueSources  map[string]string          `json:"value_sources"`
	SecretSources map[string]string          `json:"secret_sources"`
	CurrentScope  string                     `json:"current_scope"`
}

func configView(values map[string]json.RawMessage, configured map[string]bool, schema adapterproto.Schema) configScopeView {
	secretKeys := map[string]bool{}
	for _, field := range schema.Fields {
		if field.Control == "secret" {
			secretKeys[field.Key] = true
		}
	}
	ordinary := map[string]json.RawMessage{}
	for key, value := range values {
		if !secretKeys[key] {
			ordinary[key] = append(json.RawMessage(nil), value...)
		}
	}
	secrets := map[string]secretState{}
	for key, value := range configured {
		secrets[key] = secretState{Configured: value}
	}
	for key := range secretKeys {
		if _, ok := secrets[key]; !ok {
			secrets[key] = secretState{}
		}
	}
	return configScopeView{Values: ordinary, Secrets: secrets}
}

func configAPI(schema adapterproto.Schema, snapshot pluginconfig.Snapshot, resource *adapterproto.ResourceRef) configAPIView {
	stored := configView(snapshot.StoredValues, snapshot.StoredSecrets, schema)
	effective := configView(snapshot.EffectiveValues, snapshot.EffectiveSecret, schema)
	return configAPIView{Schema: schema, Values: stored.Values, Secrets: stored.Secrets, Stored: stored, Effective: effective, ValueSources: snapshot.ValueSources, SecretSources: snapshot.SecretSources, CurrentScope: pluginconfig.ScopeIdentity(pluginconfig.Scope{Resource: resource})}
}

func (s *Server) configGet(w http.ResponseWriter, r *http.Request) {
	if s.configs == nil || s.adapters == nil {
		writeError(w, http.StatusServiceUnavailable, "adapter configuration is unavailable")
		return
	}
	resource, err := resourceQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid resource scope")
		return
	}
	id := r.PathValue("id")
	schema, err := s.adapters.Schema(id, resource)
	if err != nil {
		writeError(w, http.StatusNotFound, "adapter configuration schema is unavailable")
		return
	}
	snapshot, err := s.adapters.ConfigSnapshot(id, resource)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "adapter configuration could not be read")
		return
	}
	writeJSON(w, http.StatusOK, configAPI(schema, snapshot, resource))
}
func (s *Server) configPut(w http.ResponseWriter, r *http.Request) {
	if s.configs == nil || s.adapters == nil {
		writeError(w, http.StatusServiceUnavailable, "adapter configuration is unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request configPutRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid configuration request")
		return
	}
	id := r.PathValue("id")
	schema, err := s.adapters.Schema(id, request.Resource)
	if err != nil {
		writeError(w, http.StatusNotFound, "adapter configuration schema is unavailable")
		return
	}
	if err = s.configs.PutPartial(pluginconfig.Scope{PluginID: id, Resource: request.Resource}, schema, request.Values, request.Secrets, request.ClearSecrets); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, err := s.adapters.ConfigSnapshot(id, request.Resource)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "adapter configuration could not be read")
		return
	}
	writeJSON(w, http.StatusOK, configAPI(schema, snapshot, request.Resource))
}

func projectConfig(values map[string]json.RawMessage, configured map[string]bool, schema adapterproto.Schema) maskedConfig {
	secretKeys := make(map[string]bool)
	for _, field := range schema.Fields {
		if field.Control == "secret" {
			secretKeys[field.Key] = true
		}
	}
	maskedValues := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		if !secretKeys[key] {
			maskedValues[key] = value
		}
	}
	maskedSecrets := maskSecrets(configured, schema)
	for key := range secretKeys {
		if _, exists := values[key]; exists {
			maskedSecrets[key] = secretState{Configured: true}
		}
	}
	return maskedConfig{Values: maskedValues, Secrets: maskedSecrets}
}

func maskSecrets(values map[string]bool, schema adapterproto.Schema) map[string]secretState {
	out := make(map[string]secretState, len(values))
	for _, field := range schema.Fields {
		if field.Control == "secret" {
			out[field.Key] = secretState{Configured: false}
		}
	}
	for key, configured := range values {
		out[key] = secretState{Configured: configured}
	}
	return out
}

func resourceQuery(r *http.Request) (*adapterproto.ResourceRef, error) {
	raw := r.URL.Query().Get("resource")
	if raw == "" {
		return nil, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	var ref adapterproto.ResourceRef
	if err = json.Unmarshal(data, &ref); err != nil {
		return nil, err
	}
	if err = adapterproto.ValidateResourceRef(&ref); err != nil {
		return nil, err
	}
	return &ref, nil
}

type recordingSummary struct {
	ID                      string                          `json:"id"`
	Title                   string                          `json:"title,omitempty"`
	AdapterID               string                          `json:"adapter_id,omitempty"`
	Adapter                 *adapterproto.AdapterProvenance `json:"adapter,omitempty"`
	SourceURIClassification string                          `json:"source_uri_classification"`
	Resource                *adapterproto.ResourceRef       `json:"resource,omitempty"`
	SourceURL               string                          `json:"source_url,omitempty"`
	State                   domain.RecordingState           `json:"state"`
	CreatedAt               time.Time                       `json:"created_at"`
	StartedAt               time.Time                       `json:"started_at"`
	StoppedAt               any                             `json:"stopped_at,omitempty"`
	TrackCount              int                             `json:"track_count"`
	SegmentCount            int                             `json:"segment_count"`
	Duration                float64                         `json:"duration_seconds"`
	GapCount                int                             `json:"gap_count"`
	LastError               string                          `json:"last_error,omitempty"`
}

func summary(r *domain.Recording) recordingSummary {
	classification := r.SourceURIClassification
	if classification == "" {
		classification = "sensitive"
	}
	return recordingSummary{ID: r.ID, Title: r.Title, AdapterID: r.AdapterID, Adapter: r.Adapter, Resource: r.Resource, SourceURIClassification: classification, State: r.State, CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, StoppedAt: r.StoppedAt, TrackCount: len(r.Tracks), SegmentCount: r.SegmentCount(), Duration: r.Duration(), GapCount: len(r.Gaps), LastError: publicLastError(r.LastError)}
}

func publicLastError(value string) string {
	if value == "" {
		return ""
	}
	return "acquisition error"
}

type recordingDetail struct {
	*domain.Recording
	TrackCount   int     `json:"track_count"`
	SegmentCount int     `json:"segment_count"`
	Duration     float64 `json:"duration_seconds"`
}

func detail(r *domain.Recording) recordingDetail {
	projected := *r
	projected.SourceURL = ""
	projected.LastError = publicLastError(projected.LastError)
	if projected.SourceURIClassification == "" {
		projected.SourceURIClassification = "sensitive"
	}
	projected.Snapshots = append([]domain.ManifestSnapshot(nil), r.Snapshots...)
	for index := range projected.Snapshots {
		projected.Snapshots[index].SourceURI = ""
	}
	projected.Tracks = make(map[string]*domain.Track, len(r.Tracks))
	for key, track := range r.Tracks {
		copyTrack := *track
		copyTrack.SourcePlaylistURL = ""
		copyTrack.Segments = append([]domain.Segment(nil), track.Segments...)
		for index := range copyTrack.Segments {
			copyTrack.Segments[index].SourceURI = ""
		}
		copyTrack.InitSegments = append([]domain.Segment(nil), track.InitSegments...)
		for index := range copyTrack.InitSegments {
			copyTrack.InitSegments[index].SourceURI = ""
		}
		projected.Tracks[key] = &copyTrack
	}
	return recordingDetail{Recording: &projected, TrackCount: len(r.Tracks), SegmentCount: r.SegmentCount(), Duration: r.Duration()}
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
<form id="start"><label>Adapter <select id="adapter" required></select></label> <label>Title <input name="title"></label><fieldset><legend>Recording input</legend><div id="input-fields"></div></fieldset><button>Start recording</button></form>
<section id="configuration-panel"><h2>Adapter settings</h2><div id="config-fields"></div><button id="save-config" type="button">Save Settings</button></section>
<section id="challenge-panel" hidden><h2>Continue adapter workflow</h2><p id="challenge-message"></p><div id="challenge-fields"></div><label id="persist-choice" hidden><input id="persist-answer" type="checkbox"> Save these values for this resource</label><button id="continue-workflow" type="button">Continue</button></section>
<p id="message" role="status"></p><ul id="recordings"></ul><video id="player" controls playsinline style="width:min(100%,800px)"></video>
<script src="https://cdn.jsdelivr.net/npm/hls.js@1.5.17/dist/hls.min.js"></script>
<script>
const message=document.querySelector('#message'),list=document.querySelector('#recordings'),video=document.querySelector('#player'),adapterSelect=document.querySelector('#adapter'),inputFields=document.querySelector('#input-fields'),configFields=document.querySelector('#config-fields'),challengeFields=document.querySelector('#challenge-fields');let hls,inputSchema={fields:[]},activeWorkflow=null;
async function api(url,opts={}){const r=await fetch(url,{headers:{'Content-Type':'application/json'},...opts});const body=await r.json();if(!r.ok)throw Error(body.error||r.statusText);return body}
function encodedResource(resource){const bytes=new TextEncoder().encode(JSON.stringify(resource));let binary='';bytes.forEach(value=>binary+=String.fromCharCode(value));return btoa(binary).replace(/=/g,'').replace(/\+/g,'-').replace(/\//g,'_')}
function currentScope(resource){return resource?'resource:'+encodedResource(resource):'plugin'}
function serialValue(field,control){if(field.control==='boolean')return control.checked;if(field.control==='number')return control.value===''?undefined:Number(control.value);if(field.control==='select')return control.value===''?undefined:JSON.parse(control.value);if(field.control==='multi-select')return Array.from(control.selectedOptions).map(option=>JSON.parse(option.value));return control.value===''?undefined:control.value}
function renderSchema(target,schema,projection={}){target.replaceChildren();const values=projection.values||{},secrets=projection.secrets||{},sources=projection.sources||{},secretSources=projection.secretSources||{};const resource=projection.resource||null,current=projection.currentScope||currentScope(resource);for(const field of schema.fields||[]){const row=document.createElement('div');const title=document.createElement('label');title.append(document.createTextNode(field.label+' '));if(field.control==='action'||field.control==='status'){const display=document.createElement('span');display.textContent=field.description||'';row.append(title,display);target.append(row);continue}let control;if(field.control==='textarea'){control=document.createElement('textarea')}else if(field.control==='select'||field.control==='multi-select'){control=document.createElement('select');control.multiple=field.control==='multi-select';(field.options||[]).forEach(option=>{const item=document.createElement('option');item.value=JSON.stringify(option.value);item.textContent=option.label;control.append(item)})}else{control=document.createElement('input');control.type=field.control==='secret'?'password':field.control==='number'?'number':field.control==='boolean'?'checkbox':'text'}control.dataset.key=field.key;if(field.required&&field.control!=='secret')control.required=true;const constraint=field.constraints||{};if(control.type==='number'){if(constraint.min!==undefined)control.min=constraint.min;if(constraint.max!==undefined)control.max=constraint.max}if(control.type==='text'||control.tagName==='TEXTAREA'){if(constraint.min_length!==undefined)control.minLength=constraint.min_length;if(constraint.max_length!==undefined)control.maxLength=constraint.max_length;if(constraint.pattern)control.pattern=constraint.pattern}let initial=values[field.key];if(initial===undefined&&field.default!==undefined)initial=field.default;if(field.control==='secret'){control.value='';const state=secrets[field.key]||{configured:false};const status=document.createElement('small');status.textContent=state.configured?' configured':' not configured';title.append(status);const clearLabel=document.createElement('label');const clear=document.createElement('input');clear.type='checkbox';clear.dataset.clearSecret=field.key;clearLabel.append(clear,document.createTextNode(' Clear'));if(state.configured)row.append(clearLabel);control.required=!!field.required&&!state.configured}else if(initial!==undefined){if(field.control==='boolean')control.checked=!!initial;else if(field.control==='select')control.value=JSON.stringify(initial);else if(field.control==='multi-select'){const selected=new Set(initial||[]);Array.from(control.options).forEach(option=>option.selected=selected.has(JSON.parse(option.value)))}else control.value=initial}control.dataset.initial=JSON.stringify(serialValue(field,control));title.append(control);row.append(title);if(field.description){const hint=document.createElement('small');hint.textContent=field.description;row.append(hint)}const source=field.control==='secret'?secretSources[field.key]:sources[field.key];if(source&&source!==current){const inherited=document.createElement('small');inherited.textContent='Inherited from '+source;row.append(inherited)}target.append(row)}}
function collect(target,schema,partial){const values={},secrets={},clear=[];for(const field of schema.fields||[]){if(field.control==='action'||field.control==='status')continue;if(field.control==='secret'){const control=target.querySelector('[data-key="'+CSS.escape(field.key)+'"]');const clearControl=target.querySelector('[data-clear-secret="'+CSS.escape(field.key)+'"]');if(clearControl&&clearControl.checked){clear.push(field.key);continue}if(control&&control.value!=='')secrets[field.key]=control.value;continue}const control=target.querySelector('[data-key="'+CSS.escape(field.key)+'"]');if(!control)continue;const value=serialValue(field,control);if(value===undefined)continue;if(partial&&control.dataset.initial===JSON.stringify(value))continue;values[field.key]=value}return {values:values,secrets:secrets,clear:clear}}
async function loadSchema(){const id=adapterSelect.value;inputFields.replaceChildren();configFields.replaceChildren();if(!id)return;const schema=await api('/api/adapters/'+encodeURIComponent(id)+'/schema');inputSchema=schema.input_schema||{fields:[]};renderSchema(inputFields,inputSchema);await loadConfiguration(id,null)}
async function loadConfiguration(id,resource){const query=resource?'?resource='+encodedResource(resource):'';const data=await api('/api/adapters/'+encodeURIComponent(id)+'/config'+query);const config=data.configuration||data;renderSchema(configFields,data.schema||{fields:[]},{values:config.effective_values||config.effective?.values||{},secrets:config.effective_secrets||config.effective?.secrets||{},sources:data.value_sources||config.value_sources||{},secretSources:data.secret_sources||config.secret_sources||{},resource:resource,currentScope:data.current_scope});document.querySelector('#save-config').onclick=async()=>{try{const fields=data.schema||{fields:[]},submitted=collect(configFields,fields,true);await api('/api/adapters/'+encodeURIComponent(id)+'/config',{method:'PUT',body:JSON.stringify({resource:resource||undefined,values:submitted.values,secrets:submitted.secrets,clear_secrets:submitted.clear})});await loadConfiguration(id,resource);message.textContent='Settings saved.'}catch(error){message.textContent=error.message}}}
async function loadAdapters(){const adapters=await api('/api/adapters');adapterSelect.replaceChildren();adapters.forEach(item=>{const option=document.createElement('option');if(item.descriptor){option.value=item.descriptor.id;option.textContent=item.descriptor.name+' — '+item.status.state+' v'+item.descriptor.version}else{option.value='';option.textContent=item.status.id+' — '+item.status.state;option.disabled=true}adapterSelect.append(option)});await loadSchema()}
async function refresh(){const items=await api('/api/recordings');list.replaceChildren(...items.map(item=>{const li=document.createElement('li');li.append(document.createTextNode((item.title||item.id)+' — '+item.state+' — '+item.segment_count+' segments '));if(item.state==='recording'){const stop=document.createElement('button');stop.textContent='Stop';stop.onclick=async()=>{try{await api('/api/recordings/'+item.id+'/stop',{method:'POST'});refresh()}catch(error){message.textContent=error.message}};li.append(stop)}else{const play=document.createElement('button');play.textContent='Play VOD';play.onclick=()=>playRecording(item.id);li.append(play)}return li}))}
function playRecording(id){const src='/api/recordings/'+id+'/play/master.m3u8';if(hls)hls.destroy();if(video.canPlayType('application/vnd.apple.mpegurl')){video.src=src;video.play()}else if(window.Hls&&Hls.isSupported()){hls=new Hls();hls.loadSource(src);hls.attachMedia(video);hls.on(Hls.Events.MANIFEST_PARSED,()=>video.play())}else{message.textContent='This browser has no HLS playback support.'}}
async function showWorkflow(progress){activeWorkflow=progress;const panel=document.querySelector('#challenge-panel');panel.hidden=false;document.querySelector('#challenge-message').textContent=(progress.challenge.prompt?.title||'Additional configuration is required')+' '+(progress.challenge.prompt?.message||'');await loadConfiguration(adapterSelect.value,progress.resource||null);renderSchema(challengeFields,progress.challenge.schema);const persist=document.querySelector('#persist-choice');persist.hidden=!progress.challenge.persistable;document.querySelector('#persist-answer').checked=false}
document.querySelector('#continue-workflow').onclick=async()=>{if(!activeWorkflow)return;try{const result=collect(challengeFields,activeWorkflow.challenge.schema,false),persist=activeWorkflow.challenge.persistable&&document.querySelector('#persist-answer').checked;const next=await api('/api/resolve-workflows/'+encodeURIComponent(activeWorkflow.workflow_id)+'/continue',{method:'POST',body:JSON.stringify({values:result.values,secrets:result.secrets,persist:persist})});if(next.workflow_id){await showWorkflow(next);return}document.querySelector('#challenge-panel').hidden=true;activeWorkflow=null;message.textContent='Started '+next.id;refresh()}catch(error){message.textContent=error.message}}
document.querySelector('#start').addEventListener('submit',async event=>{event.preventDefault();const values=collect(inputFields,inputSchema,false);try{const result=await api('/api/recordings',{method:'POST',body:JSON.stringify({adapter_id:adapterSelect.value,input:values.values,title:event.currentTarget.elements.title.value})});if(result.workflow_id){await showWorkflow(result);message.textContent='Adapter needs additional information.';return}message.textContent='Started '+result.id;event.currentTarget.elements.title.value='';refresh()}catch(error){message.textContent=error.message}})
adapterSelect.addEventListener('change',()=>loadSchema().catch(error=>message.textContent=error.message));loadAdapters().catch(error=>message.textContent=error.message);refresh().catch(error=>message.textContent=error.message);setInterval(()=>refresh().catch(()=>{}),5000);
</script></body></html>`
