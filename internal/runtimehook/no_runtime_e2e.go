//go:build !runtime_e2e

package runtimehook

// Pause is inert in normal product builds. A runtime_e2e build may block at a
// selected point until its private release marker appears.
func Pause(Point, string) error { return nil }

// Observe is inert in normal product builds.
func Observe(Observation, string) error { return nil }

// ChildEnvironment is empty in normal product builds. The tagged test build
// returns only the explicitly allowlisted failpoint environment variables.
func ChildEnvironment() []string { return nil }
