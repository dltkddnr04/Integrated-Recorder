// Package owncast contains platform-specific stream discovery only.
package owncast

import (
	"fmt"
	"net/url"
	"strings"
)

const StreamPath = "/hls/stream.m3u8"

type Resolver struct{}

// Resolve returns Owncast's documented HLS entry point. Rendition selection is
// intentionally handled by the shared HLS parser/acquirer.
func (Resolver) Resolve(instanceBase string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(instanceBase))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", fmt.Errorf("source_url must be an http or https Owncast instance URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + StreamPath
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
