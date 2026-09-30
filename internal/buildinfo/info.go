// Package buildinfo exposes the identity embedded in an application release.
//
// Release build tooling should set version, commit, buildTime, and
// releaseChannel with Go linker -X flags. A normal development build keeps the
// explicit dev/unknown/development identity and must not be presented as a
// release.
package buildinfo

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const RuntimeProtocolVersion = 1

var (
	version        = "dev"
	commit         = "unknown"
	buildTime      = "unknown"
	releaseChannel = "development"

	semanticVersionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	commitPattern          = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
)

// Info is a value snapshot of an application build identity. Current returns a
// fresh value, so changing a caller's copy cannot mutate the process identity.
type Info struct {
	Version                string `json:"version"`
	Commit                 string `json:"commit"`
	BuildTime              string `json:"build_time"`
	ReleaseChannel         string `json:"release_channel"`
	RuntimeProtocolVersion int    `json:"runtime_protocol_version"`
}

// Current returns the build identity linked into this binary.
func Current() Info {
	return Info{
		Version:                version,
		Commit:                 commit,
		BuildTime:              buildTime,
		ReleaseChannel:         releaseChannel,
		RuntimeProtocolVersion: RuntimeProtocolVersion,
	}
}

// Validate accepts either the explicit development identity or a complete,
// structurally valid release identity. It does not infer release status from
// version ordering.
func (i Info) Validate() error {
	if i.RuntimeProtocolVersion != RuntimeProtocolVersion {
		return fmt.Errorf("runtime protocol version is unsupported")
	}
	if i.Version == "dev" || i.Commit == "unknown" || i.ReleaseChannel == "development" {
		if i.Version != "dev" || i.Commit != "unknown" || i.ReleaseChannel != "development" || i.BuildTime != "unknown" {
			return fmt.Errorf("development build identity is incomplete")
		}
		return nil
	}
	return i.ValidateRelease()
}

// ValidateRelease rejects development and partial identities. Release build
// tooling and startup checks can use it to fail closed on missing linker data.
func (i Info) ValidateRelease() error {
	if i.RuntimeProtocolVersion != RuntimeProtocolVersion {
		return fmt.Errorf("runtime protocol version is unsupported")
	}
	if strings.TrimSpace(i.Version) == "" || i.Version == "dev" || !semanticVersionPattern.MatchString(i.Version) {
		return fmt.Errorf("release version is invalid")
	}
	if !commitPattern.MatchString(i.Commit) {
		return fmt.Errorf("release commit is invalid")
	}
	if _, err := time.Parse(time.RFC3339, i.BuildTime); err != nil {
		return fmt.Errorf("release build time is invalid")
	}
	if i.ReleaseChannel != "stable" && i.ReleaseChannel != "prerelease" {
		return fmt.Errorf("release channel is invalid")
	}
	return nil
}
