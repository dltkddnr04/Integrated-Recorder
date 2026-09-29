package adapterproto

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMetadataResultValidationPreservesUnknownAndKnownEmpty(t *testing.T) {
	empty := ""
	result := MetadataResult{Metadata: StreamMetadata{Description: &empty}}
	if err := result.Validate(); err != nil {
		t.Fatalf("known empty description rejected: %v", err)
	}
	if result.Metadata.Title != nil || result.Metadata.Description == nil || *result.Metadata.Description != "" {
		t.Fatalf("nil/empty semantics lost: %#v", result.Metadata)
	}
	encoded, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(encoded), `"description":""`) || strings.Contains(string(encoded), `"title"`) {
		t.Fatalf("wire nil/empty semantics=%s err=%v", encoded, err)
	}
}

func TestMetadataResultRejectsUntrustedTextAndTimestamp(t *testing.T) {
	validTime := time.Date(2026, 9, 29, 1, 2, 3, 0, time.UTC)
	oversizedTitle := strings.Repeat("x", 4<<10+1)
	oversizedDescription := strings.Repeat("x", 64<<10+1)
	invalidUTF8 := string([]byte{0xff})
	withNUL := "bad\x00text"
	zero := time.Time{}
	for name, result := range map[string]MetadataResult{
		"title limit":       {Metadata: StreamMetadata{Title: &oversizedTitle}},
		"description limit": {Metadata: StreamMetadata{Description: &oversizedDescription}},
		"invalid UTF-8":     {Metadata: StreamMetadata{Title: &invalidUTF8}},
		"NUL":               {Metadata: StreamMetadata{Description: &withNUL}},
		"zero timestamp":    {SourceUpdatedAt: &zero},
		"valid timestamp":   {SourceUpdatedAt: &validTime},
	} {
		err := result.Validate()
		if name == "valid timestamp" && err != nil {
			t.Fatalf("valid source timestamp rejected: %v", err)
		}
		if name != "valid timestamp" && err == nil {
			t.Fatalf("%s unexpectedly accepted", name)
		}
	}
}
