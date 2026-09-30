package buildinfo

import "testing"

func TestCurrentIsExplicitDevelopmentIdentity(t *testing.T) {
	got := Current()
	if got.Version != "dev" || got.Commit != "unknown" || got.BuildTime != "unknown" || got.ReleaseChannel != "development" || got.RuntimeProtocolVersion != RuntimeProtocolVersion {
		t.Fatalf("unexpected development identity: %+v", got)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("development identity should validate: %v", err)
	}
	if err := got.ValidateRelease(); err == nil {
		t.Fatal("development identity was accepted as a release")
	}
}

func TestInfoValidationRequiresCompleteReleaseIdentity(t *testing.T) {
	release := Info{
		Version:                "1.2.3",
		Commit:                 stringsOf('a', 40),
		BuildTime:              "2026-09-29T12:00:00Z",
		ReleaseChannel:         "stable",
		RuntimeProtocolVersion: RuntimeProtocolVersion,
	}
	if err := release.Validate(); err != nil {
		t.Fatalf("valid release identity rejected: %v", err)
	}
	if err := release.ValidateRelease(); err != nil {
		t.Fatalf("valid release identity rejected for release: %v", err)
	}

	invalid := []Info{
		{Version: "dev", Commit: stringsOf('a', 40), BuildTime: release.BuildTime, ReleaseChannel: "stable", RuntimeProtocolVersion: RuntimeProtocolVersion},
		{Version: "1.2.3", Commit: "unknown", BuildTime: release.BuildTime, ReleaseChannel: "stable", RuntimeProtocolVersion: RuntimeProtocolVersion},
		{Version: "1.2.3", Commit: release.Commit, BuildTime: "not a timestamp", ReleaseChannel: "stable", RuntimeProtocolVersion: RuntimeProtocolVersion},
		{Version: "1.2.3", Commit: release.Commit, BuildTime: release.BuildTime, ReleaseChannel: "development", RuntimeProtocolVersion: RuntimeProtocolVersion},
	}
	for i, item := range invalid {
		if err := item.Validate(); err == nil {
			t.Errorf("identity %d unexpectedly validated", i)
		}
	}
}

func stringsOf(ch byte, count int) string {
	result := make([]byte, count)
	for i := range result {
		result[i] = ch
	}
	return string(result)
}
