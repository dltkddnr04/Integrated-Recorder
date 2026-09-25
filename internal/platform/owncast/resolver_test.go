package owncast

import "testing"

func TestResolveUsesOwncastStreamPath(t *testing.T) {
	got, err := (Resolver{}).Resolve("https://live.example/custom/base/?token=ignored#frag")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://live.example/custom/base/hls/stream.m3u8"; got != want {
		t.Fatalf("Resolve() = %q, want %q", got, want)
	}
}

func TestResolveRejectsUnsupportedURL(t *testing.T) {
	for _, input := range []string{"", "file:///tmp/live", "https:///missing-host", "https://user:pass@live.example"} {
		if _, err := (Resolver{}).Resolve(input); err == nil {
			t.Errorf("Resolve(%q) succeeded", input)
		}
	}
}
