// Package httpapi exposes the authenticated Runtime Host update contract.
// It deliberately contains no release orchestration or filesystem access.
package httpapi

import (
	"fmt"
	"regexp"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	Endpoint          = "/api/runtime/update"
	MaxRequestBody    = 4 << 10
	maxResponseBytes  = 128 << 10
	maxGenerations    = 64
	maxGenerationName = 128
)

// BuildIdentity is the bounded public build identity of a Runtime Host or
// application release. It contains no executable, installation, or data path.
type BuildIdentity struct {
	Version                string `json:"version"`
	Commit                 string `json:"commit"`
	BuildTime              string `json:"build_time"`
	ReleaseChannel         string `json:"release_channel"`
	RuntimeProtocolVersion int    `json:"runtime_protocol_version"`
}

// GenerationSummary is a safe public projection; it intentionally excludes
// worker/process IDs, PIDs, endpoints, and executable paths.
type GenerationSummary struct {
	ID               string    `json:"id"`
	Version          string    `json:"version"`
	Commit           string    `json:"commit"`
	InstalledAt      time.Time `json:"installed_at"`
	State            string    `json:"state"`
	ActiveRecordings int       `json:"active_recordings"`
}

// ReleaseSummary is a display-only release identity. Artifact URLs, signing
// material, local staging paths, and raw release notes are intentionally absent.
type ReleaseSummary struct {
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	BuildTime      string `json:"build_time"`
	ReleaseChannel string `json:"release_channel"`
	NotesSummary   string `json:"notes_summary,omitempty"`
}

// Status is the only update state returned to browser clients. Keep this DTO
// bounded and composed only of allowlisted values and constrained identities.
type Status struct {
	Host                    BuildIdentity       `json:"host"`
	Application             BuildIdentity       `json:"application"`
	ActiveControl           *GenerationSummary  `json:"active_control,omitempty"`
	DefaultEngine           *GenerationSummary  `json:"default_engine,omitempty"`
	ActiveGenerations       []GenerationSummary `json:"active_generations"`
	DrainingGenerations     []GenerationSummary `json:"draining_generations"`
	StagedRelease           *ReleaseSummary     `json:"staged_release,omitempty"`
	PreviousRelease         *ReleaseSummary     `json:"previous_release,omitempty"`
	AvailableRelease        *ReleaseSummary     `json:"available_release,omitempty"`
	VerificationState       string              `json:"verification_state"`
	LastFailureCode         string              `json:"last_failure_code,omitempty"`
	UpdatesAvailable        bool                `json:"updates_available"`
	UpdateUnavailableReason string              `json:"update_unavailable_reason,omitempty"`
}

var (
	versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,127}$`)
	commitPattern  = regexp.MustCompile(`^(?:unknown|[0-9a-fA-F]{7,64})$`)
	genIDPattern   = regexp.MustCompile(`^(?:[0-9a-fA-F]{32,64}|[0-9a-fA-F]{8}-(?:[0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12})$`)
)

var (
	verificationStates = map[string]struct{}{
		"unknown": {}, "not_checked": {}, "checking": {}, "verified": {}, "failed": {},
	}
	generationStates = map[string]struct{}{
		"staging": {}, "verified": {}, "ready": {}, "active": {}, "draining": {}, "retired": {}, "failed": {},
	}
	releaseChannels = map[string]struct{}{"stable": {}, "prerelease": {}, "development": {}}
	failureCodes    = map[string]struct{}{
		"update_unavailable": {}, "update_check_failed": {}, "no_update_available": {},
		"operation_conflict": {}, "verification_failed": {}, "candidate_not_ready": {},
		"release_incompatible": {}, "stage_failed": {}, "activation_failed": {},
		"rollback_unavailable": {}, "rollback_failed": {}, "internal_error": {},
	}
	unavailableReasons = map[string]struct{}{
		"development_build": {}, "source_unavailable": {}, "unsupported_platform": {},
		"trust_key_unavailable": {}, "updates_disabled": {}, "host_update_required": {},
	}
)

// Validate checks public status values before they cross the HTTP boundary.
// All user-controlled or release-controlled strings are bounded and constrained
// so paths, URLs, query tokens, and raw diagnostics cannot be projected.
func (s Status) Validate() error {
	if !validIdentity(s.Host) || !validIdentity(s.Application) {
		return fmt.Errorf("runtime update status identity is invalid")
	}
	if !enumContains(verificationStates, s.VerificationState) {
		return fmt.Errorf("runtime update verification state is invalid")
	}
	if s.LastFailureCode != "" && !enumContains(failureCodes, s.LastFailureCode) {
		return fmt.Errorf("runtime update failure code is invalid")
	}
	if s.UpdateUnavailableReason != "" && !enumContains(unavailableReasons, s.UpdateUnavailableReason) {
		return fmt.Errorf("runtime update unavailable reason is invalid")
	}
	if len(s.ActiveGenerations) > maxGenerations || len(s.DrainingGenerations) > maxGenerations {
		return fmt.Errorf("runtime generation summary count exceeds limit")
	}
	seen := make(map[string]struct{}, len(s.ActiveGenerations)+len(s.DrainingGenerations)+2)
	for _, group := range [][]GenerationSummary{s.ActiveGenerations, s.DrainingGenerations} {
		for _, generation := range group {
			if err := validateGeneration(generation); err != nil {
				return err
			}
			if _, exists := seen[generation.ID]; exists {
				return fmt.Errorf("runtime generation summary is duplicated")
			}
			seen[generation.ID] = struct{}{}
		}
	}
	for _, optional := range []*GenerationSummary{s.ActiveControl, s.DefaultEngine} {
		if optional == nil {
			continue
		}
		if err := validateGeneration(*optional); err != nil {
			return err
		}
	}
	for _, release := range []*ReleaseSummary{s.StagedRelease, s.PreviousRelease, s.AvailableRelease} {
		if release != nil && !validRelease(*release) {
			return fmt.Errorf("runtime release summary is invalid")
		}
	}
	if s.UpdatesAvailable && s.AvailableRelease == nil {
		return fmt.Errorf("available update identity is missing")
	}
	return nil
}

func validIdentity(identity BuildIdentity) bool {
	if !validVersion(identity.Version) || !commitPattern.MatchString(identity.Commit) || identity.RuntimeProtocolVersion < 1 || identity.RuntimeProtocolVersion > 1024 {
		return false
	}
	if _, ok := releaseChannels[identity.ReleaseChannel]; !ok {
		return false
	}
	if identity.Version == "dev" || identity.Commit == "unknown" || identity.ReleaseChannel == "development" {
		return identity.Version == "dev" && identity.Commit == "unknown" && identity.ReleaseChannel == "development" && identity.BuildTime == "unknown"
	}
	return validBuildTime(identity.BuildTime)
}

func validateGeneration(g GenerationSummary) error {
	if len(g.ID) > maxGenerationName || !genIDPattern.MatchString(g.ID) || !validVersion(g.Version) ||
		!commitPattern.MatchString(g.Commit) || g.InstalledAt.IsZero() || g.ActiveRecordings < 0 || g.ActiveRecordings > 1_000_000 ||
		!enumContains(generationStates, g.State) {
		return fmt.Errorf("runtime generation summary is invalid")
	}
	return nil
}

func validRelease(release ReleaseSummary) bool {
	if !validVersion(release.Version) || !commitPattern.MatchString(release.Commit) || !validBuildTime(release.BuildTime) {
		return false
	}
	if len(release.NotesSummary) > 512 || !utf8.ValidString(release.NotesSummary) {
		return false
	}
	for _, r := range release.NotesSummary {
		if unicode.IsControl(r) {
			return false
		}
	}
	_, ok := releaseChannels[release.ReleaseChannel]
	return ok && release.ReleaseChannel != "development"
}

func validBuildTime(value string) bool {
	parsed, err := time.Parse(time.RFC3339, value)
	return err == nil && parsed.Format(time.RFC3339) == value
}

func validVersion(value string) bool {
	return len(value) <= 128 && versionPattern.MatchString(value)
}

func enumContains(values map[string]struct{}, value string) bool {
	_, ok := values[value]
	return ok
}
