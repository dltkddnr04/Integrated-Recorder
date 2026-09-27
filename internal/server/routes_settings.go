package server

import (
	"net/http"

	"github.com/dltkddnr04/integrated-recorder/internal/systemsettings"
)

type settingsPutRequest struct {
	UI        *settingsUIRequest        `json:"ui,omitempty"`
	Integrity *settingsIntegrityRequest `json:"integrity,omitempty"`
	Retention *settingsRetentionRequest `json:"retention,omitempty"`
}

type settingsUIRequest struct {
	Theme *string `json:"theme,omitempty"`
}

type settingsIntegrityRequest struct {
	Concurrency *int `json:"concurrency,omitempty"`
}

type settingsRetentionRequest struct {
	Enabled            *bool `json:"enabled,omitempty"`
	CompletedAfterDays *int  `json:"completed_after_days,omitempty"`
}

func (s *Server) registerSettingsRoutes() {
	s.mux.HandleFunc("GET /api/settings", s.settingsGet)
	s.mux.HandleFunc("PUT /api/settings", s.settingsPut)
	s.registerRetentionRoutes()
}

func (s *Server) settingsGet(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeError(w, http.StatusServiceUnavailable, "system settings are unavailable")
		return
	}
	s.writeSettings(w, http.StatusOK, s.settings.Current())
}

func (s *Server) settingsPut(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeError(w, http.StatusServiceUnavailable, "system settings are unavailable")
		return
	}
	var request settingsPutRequest
	if err := decodeJSONBody(w, r, 16<<10, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid settings request")
		return
	}
	if request.UI != nil && request.UI.Theme == nil ||
		request.Integrity != nil && request.Integrity.Concurrency == nil ||
		request.Retention != nil && request.Retention.Enabled == nil && request.Retention.CompletedAfterDays == nil ||
		request.UI == nil && request.Integrity == nil && request.Retention == nil {
		writeError(w, http.StatusBadRequest, "settings request contains no values")
		return
	}
	patch := systemsettings.Patch{}
	if request.UI != nil {
		patch.UITheme = request.UI.Theme
	}
	if request.Integrity != nil {
		patch.IntegrityConcurrency = request.Integrity.Concurrency
	}
	if request.Retention != nil {
		patch.RetentionEnabled = request.Retention.Enabled
		patch.RetentionAfterDays = request.Retention.CompletedAfterDays
	}
	settings, err := s.settings.Update(patch)
	if err != nil {
		writeError(w, http.StatusBadRequest, "settings values are invalid")
		return
	}
	s.writeSettings(w, http.StatusOK, settings)
}

func (s *Server) writeSettings(w http.ResponseWriter, status int, settings systemsettings.Settings) {
	restartRequired := []string{}
	if s.initialIntegrityConcurrency > 0 && settings.Integrity.Concurrency != s.initialIntegrityConcurrency {
		restartRequired = append(restartRequired, "integrity.concurrency")
	}
	writeJSON(w, status, map[string]any{
		"settings":         settings,
		"restart_required": restartRequired,
	})
}
