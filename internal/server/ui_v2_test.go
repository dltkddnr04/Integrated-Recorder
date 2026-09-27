package server

import (
	"strings"
	"testing"
)

func TestManagementUIIncludesConnectedProductSurfaces(t *testing.T) {
	targets := []string{
		`id="auth-panel"`, `id="dashboard-section"`, `id="recordings-section"`,
		`id="new-section"`, `id="adapters-section"`, `id="activity-section"`,
		`id="system-section"`, `id="recording-detail"`, `id="resource-search"`,
		`id="workflow-list"`, `id="archive-index"`, `id="detail-events"`,
		`id="export-panel"`, `id="create-export"`, `id="refresh-exports"`, `id="export-jobs"`,
		`/static/app.css`, `/static/hls.min.js`, `/static/app.js`,
	}
	for _, target := range targets {
		if !strings.Contains(safeIndexHTML, target) {
			t.Errorf("management UI is missing %q", target)
		}
	}
	if strings.Contains(safeIndexHTML, "cdn.jsdelivr.net") || strings.Contains(safeIndexHTML, "unpkg.com") {
		t.Fatal("management UI must not load scripts from a third-party CDN")
	}
	app, err := staticFiles.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/thumbnail/regenerate", "function loadThumbnail", "function regenerateThumbnail", "api/logs", "ensureApplicationLogPanel"} {
		if !strings.Contains(string(app), target) {
			t.Errorf("management UI has no connected thumbnail surface %q", target)
		}
	}
}
