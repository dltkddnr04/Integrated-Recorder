package owncast

import (
	"encoding/json"
	"testing"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
)

func TestResolveValidURLAndRemovesQueryAndFragment(t *testing.T) {
	media, err := Resolve(json.RawMessage(`{"source_url":"https://live.example/custom/base/?token=ignored#section"}`))
	if err != nil {
		t.Fatal(err)
	}
	if media.Type != "hls" || media.ManifestURL != "https://live.example/custom/base/hls/stream.m3u8" {
		t.Fatalf("media = %#v", media)
	}
}

func TestResolveRejectsInvalidURL(t *testing.T) {
	for _, input := range []string{`{"source_url":""}`, `{"source_url":"file:///tmp/live"}`, `{"source_url":"https:///missing-host"}`, `{"source_url":"https://user:pass@live.example"}`, `{"source_url":"//live.example"}`} {
		if _, err := Resolve(json.RawMessage(input)); err == nil {
			t.Errorf("Resolve(%s) succeeded", input)
		}
	}
}

func TestDescribeSchemaIsGenericAndValid(t *testing.T) {
	d := Describe()
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if d.ID != "owncast" || d.ProtocolVersion != adapterproto.Version || len(d.InputSchema.Fields) != 1 || d.InputSchema.Fields[0].Key != "source_url" || len(d.ConfigurationSchema.Fields) != 0 {
		t.Fatalf("descriptor = %#v", d)
	}
}
