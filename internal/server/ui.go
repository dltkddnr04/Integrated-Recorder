package server

import "strings"

// isSPARoute only admits routes that the embedded client application owns.
// Unknown paths must remain ordinary 404s instead of receiving index.html.
func isSPARoute(path string) bool {
	switch path {
	case "/", "/login", "/recordings", "/new", "/adapters", "/workflows", "/settings", "/watches", "/watches/new":
		return true
	}
	for _, prefix := range []string{"/recordings/", "/adapters/", "/workflows/", "/watches/"} {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		id := strings.TrimPrefix(path, prefix)
		return id != "" && id != "." && id != ".." && !strings.Contains(id, "/")
	}
	return false
}
