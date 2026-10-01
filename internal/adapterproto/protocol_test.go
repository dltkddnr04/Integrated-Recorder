package adapterproto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRequestResponseFramesAndStructuredError(t *testing.T) {
	var frame bytes.Buffer
	if err := WriteRequest(&frame, Request{ProtocolVersion: Version, ID: "42", Method: MethodDescribe, Params: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	request, err := ReadRequest(bufio.NewReader(&frame))
	if err != nil {
		t.Fatal(err)
	}
	if request.ID != "42" || request.Method != MethodDescribe || string(request.Params) != "{}" {
		t.Fatalf("request = %#v", request)
	}
	frame.Reset()
	if err = WriteResponse(&frame, Failure("42", "unsupported_method", "not supported", map[string]any{"method": "future"})); err != nil {
		t.Fatal(err)
	}
	response, err := ReadResponse(bufio.NewReader(&frame))
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != "42" || response.Error == nil || response.Error.Code != "unsupported_method" || response.Error.Message != "not supported" {
		t.Fatalf("response = %#v", response)
	}
}

func TestResourceBrowseRequestAndPageWireTypes(t *testing.T) {
	parent := &ResourceRef{Type: "opaque.alpha", ID: "parent-id"}
	list, err := json.Marshal(ResourceListParams{Parent: parent, ResourceType: "opaque.beta", Cursor: "cursor-1", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var listDecoded ResourceListParams
	if err = json.Unmarshal(list, &listDecoded); err != nil {
		t.Fatal(err)
	}
	if listDecoded.Parent == nil || listDecoded.Parent.Type != parent.Type || listDecoded.Parent.ID != parent.ID || listDecoded.ResourceType != "opaque.beta" || listDecoded.Cursor != "cursor-1" || listDecoded.Limit != 20 {
		t.Fatalf("list params round trip = %#v", listDecoded)
	}

	search, err := json.Marshal(ResourceSearchParams{Parent: parent, ResourceType: "opaque.beta", Query: "opaque query", Cursor: "cursor-2", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var searchDecoded ResourceSearchParams
	if err = json.Unmarshal(search, &searchDecoded); err != nil {
		t.Fatal(err)
	}
	if searchDecoded.Parent == nil || searchDecoded.Query != "opaque query" || searchDecoded.Limit != 50 {
		t.Fatalf("search params round trip = %#v", searchDecoded)
	}

	page := ResourcePage{Items: []Resource{{ResourceRef: ResourceRef{Type: "opaque.beta", ID: "item", Parent: parent}, DisplayName: "Example", Attributes: map[string]json.RawMessage{"opaque": json.RawMessage(`{"nested":[1,true]}`)}}}, NextCursor: "next"}
	wire, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	var pageDecoded ResourcePage
	if err = json.Unmarshal(wire, &pageDecoded); err != nil {
		t.Fatal(err)
	}
	if len(pageDecoded.Items) != 1 || pageDecoded.Items[0].Attributes["opaque"] == nil || string(pageDecoded.Items[0].Attributes["opaque"]) != `{"nested":[1,true]}` || pageDecoded.NextCursor != "next" {
		t.Fatalf("resource page round trip = %#v", pageDecoded)
	}
}

func TestRejectsUnsupportedVersionsMalformedAndOversizedFrames(t *testing.T) {
	for name, line := range map[string]string{"unsupported": `{"protocol_version":2,"id":"1","method":"describe"}` + "\n", "malformed": "{bad json}\n", "oversized": strings.Repeat("x", MaxFrameBytes+10) + "\n"} {
		t.Run(name, func(t *testing.T) {
			_, err := ReadRequest(bufio.NewReader(strings.NewReader(line)))
			if err == nil {
				t.Fatal("expected frame rejection")
			}
			if name == "unsupported" && (!strings.Contains(err.Error(), "unsupported protocol version") || !errors.Is(err, ErrUnsupportedProtocolVersion)) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	_, err := ReadResponse(bufio.NewReader(strings.NewReader(`{"protocol_version":2,"id":"1","result":{}}` + "\n")))
	if err == nil || !strings.Contains(err.Error(), "unsupported protocol version") || !errors.Is(err, ErrUnsupportedProtocolVersion) {
		t.Fatalf("response error = %v", err)
	}
	_, err = ParseFrame([]byte(`{"protocol_version":2,"type":"notification","method":"events.emit"}`))
	if !errors.Is(err, ErrUnsupportedProtocolVersion) {
		t.Fatalf("notification version error = %v", err)
	}
}

func TestParseFrameRejectsInvalidUTF8(t *testing.T) {
	frame := append([]byte(`{"protocol_version":1,"id":"1","method":"describe","params":{"x":"`), 0xff)
	frame = append(frame, []byte(`"}}`)...)
	if _, err := ParseFrame(frame); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid UTF-8 frame error = %v", err)
	}
}

func TestRejectsUnterminatedFrameAndResponseWithoutPayload(t *testing.T) {
	if _, err := ReadRequest(bufio.NewReader(strings.NewReader(`{"protocol_version":1,"id":"1","method":"describe"}`))); err == nil {
		t.Fatal("expected unterminated frame error")
	}
	if _, err := ReadResponse(bufio.NewReader(strings.NewReader(`{"protocol_version":1,"id":"1"}` + "\n"))); err == nil {
		t.Fatal("expected empty response error")
	}
}

func TestParseFramePreservesLegacyRequestAndResponseWireForms(t *testing.T) {
	requestWire := []byte(`{"protocol_version":1,"id":"legacy-request","method":"resolve","params":{}}`)
	requestFrame, err := ParseFrame(requestWire)
	if err != nil || requestFrame.Kind != FrameTypeRequest || requestFrame.Request == nil || requestFrame.Request.ID != "legacy-request" {
		t.Fatalf("legacy request frame = %#v, %v", requestFrame, err)
	}
	responseWire := []byte(`{"protocol_version":1,"id":"legacy-request","result":{"ok":true}}`)
	responseFrame, err := ParseFrame(responseWire)
	if err != nil || responseFrame.Kind != FrameTypeResponse || responseFrame.Response == nil || responseFrame.Response.ID != "legacy-request" {
		t.Fatalf("legacy response frame = %#v, %v", responseFrame, err)
	}
	if bytes.Contains(requestWire, []byte(`"type"`)) || bytes.Contains(responseWire, []byte(`"type"`)) {
		t.Fatal("legacy wire fixture unexpectedly requires a type property")
	}
}

func TestParseFrameAcceptsTypedRequestAndResponseExtensions(t *testing.T) {
	requestWire := `{"protocol_version":1,"type":"request","id":"typed-1","method":"resolve","params":{}}`
	request, err := ParseFrame([]byte(requestWire))
	if err != nil || request.Kind != FrameTypeRequest || request.Request == nil || request.Request.ID != "typed-1" {
		t.Fatalf("typed request frame = %#v, %v", request, err)
	}
	if read, readErr := ReadRequest(bufio.NewReader(strings.NewReader(requestWire + "\n"))); readErr != nil || read.ID != "typed-1" {
		t.Fatalf("typed request reader = %#v, %v", read, readErr)
	}
	responseWire := `{"protocol_version":1,"type":"response","id":"typed-1","result":{}}`
	response, err := ParseFrame([]byte(responseWire))
	if err != nil || response.Kind != FrameTypeResponse || response.Response == nil || response.Response.ID != "typed-1" {
		t.Fatalf("typed response frame = %#v, %v", response, err)
	}
	if read, readErr := ReadResponse(bufio.NewReader(strings.NewReader(responseWire + "\n"))); readErr != nil || read.ID != "typed-1" {
		t.Fatalf("typed response reader = %#v, %v", read, readErr)
	}
}

func TestWriteRequestResponseKeepLegacyUntypedWireFormat(t *testing.T) {
	var request bytes.Buffer
	if err := WriteRequest(&request, Request{ProtocolVersion: Version, ID: "1", Method: "describe", Params: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if got, want := request.String(), "{\"protocol_version\":1,\"id\":\"1\",\"method\":\"describe\",\"params\":{}}\n"; got != want {
		t.Fatalf("request wire = %s, want %s", got, want)
	}
	var response bytes.Buffer
	if err := WriteResponse(&response, Response{ProtocolVersion: Version, ID: "1", Result: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if got, want := response.String(), "{\"protocol_version\":1,\"id\":\"1\",\"result\":{}}\n"; got != want {
		t.Fatalf("response wire = %s, want %s", got, want)
	}
}

func TestNotificationIsDistinctFrameWithoutResponseID(t *testing.T) {
	var wire bytes.Buffer
	notification := Notification{ProtocolVersion: Version, Method: "events.emit", Params: []byte(`{"kind":"state"}`)}
	if err := WriteNotification(&wire, notification); err != nil {
		t.Fatal(err)
	}
	frame, err := ParseFrame(bytes.TrimSuffix(wire.Bytes(), []byte("\n")))
	if err != nil || frame.Kind != FrameTypeNotification || frame.Notification == nil || frame.Notification.Method != "events.emit" {
		t.Fatalf("notification frame = %#v, %v", frame, err)
	}
	if frame.Notification.ProtocolVersion != Version || string(frame.Notification.Params) != `{"kind":"state"}` {
		t.Fatalf("notification payload = %#v", frame.Notification)
	}
	if bytes.Contains(wire.Bytes(), []byte(`"id"`)) {
		t.Fatalf("notification unexpectedly has a request ID: %s", wire.String())
	}
	if _, err = ReadResponse(bufio.NewReader(bytes.NewReader(wire.Bytes()))); err == nil || !strings.Contains(err.Error(), "expected response frame") {
		t.Fatalf("ReadResponse notification error = %v", err)
	}
}

func TestParseFrameRejectsUnknownAndMalformedFrameKinds(t *testing.T) {
	for name, wire := range map[string]string{
		"unknown typed kind":     `{"protocol_version":1,"type":"event","method":"events.emit"}`,
		"nonstring type":         `{"protocol_version":1,"type":7,"method":"events.emit"}`,
		"notification id":        `{"protocol_version":1,"type":"notification","id":"1","method":"events.emit"}`,
		"missing method":         `{"protocol_version":1,"type":"notification","params":{}}`,
		"mixed request response": `{"protocol_version":1,"id":"1","method":"resolve","result":{}}`,
		"unknown legacy shape":   `{"protocol_version":1,"id":"1","other":true}`,
		"malformed json":         `{"protocol_version":1,`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseFrame([]byte(wire)); err == nil {
				t.Fatalf("accepted malformed frame %s", wire)
			}
		})
	}
}

func FuzzParseFrameBounded(f *testing.F) {
	f.Add([]byte(`{"protocol_version":1,"id":"1","method":"describe","params":{}}`))
	f.Add([]byte(`{"protocol_version":1,"type":"notification","method":"events.emit","params":{}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxFrameBytes {
			return
		}
		_, _ = ParseFrame(data)
	})
}

func FuzzSchemaAndResourceValidation(f *testing.F) {
	f.Add([]byte(`{"fields":[{"key":"enabled","control":"boolean","label":"Enabled","default":false}]}`))
	f.Add([]byte(`{"fields":[{"key":"dependent","control":"text","label":"Dependent","visible_when":{"field":"enabled","truthy":true}}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		var schema Schema
		if json.Unmarshal(data, &schema) == nil {
			_ = schema.Validate()
			_ = ValidateProvidedValues(schema, map[string]json.RawMessage{})
		}
		var ref ResourceRef
		if json.Unmarshal(data, &ref) == nil {
			_ = ValidateResourceRef(&ref)
			descriptor := Descriptor{ID: "fuzz", Name: "Fuzz", Version: "1", ProtocolVersion: Version, Capabilities: []string{CapabilityResolve}, MediaTypes: []string{"hls"}}
			_ = ValidateResourceRefForDescriptor(descriptor, &ref)
		}
	})
}

func TestDescriptorSchemaValidation(t *testing.T) {
	d := Descriptor{ID: "adapter", Name: "Adapter", Version: "1", ProtocolVersion: Version, Capabilities: []string{CapabilityResolve}, InputSchema: Schema{Fields: []Field{{Key: "mode", Control: "select", Label: "Mode", Options: []Option{{Value: "one", Label: "One"}}}}}, ConfigurationSchema: Schema{Fields: []Field{}}, MediaTypes: []string{"hls"}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	d.InputSchema.Fields = append(d.InputSchema.Fields, Field{Key: "mode", Control: "text", Label: "Duplicate"})
	if err := d.Validate(); err == nil {
		t.Fatal("duplicate schema key accepted")
	}
}

func TestDescriptorBrandingPNGValidation(t *testing.T) {
	base := Descriptor{ID: "adapter", Name: "Adapter", Version: "1", ProtocolVersion: Version, Capabilities: []string{CapabilityResolve}, InputSchema: Schema{Fields: []Field{}}, ConfigurationSchema: Schema{Fields: []Field{}}, MediaTypes: []string{"hls"}}
	if err := base.Validate(); err != nil {
		t.Fatalf("descriptor without branding rejected: %v", err)
	}
	base.Branding = &Branding{Icon: &BrandIcon{MediaType: "image/png", Data: testPNG(t, 1, 1, false)}}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid PNG branding rejected: %v", err)
	}

	tests := map[string]BrandIcon{
		"unsupported media type": {MediaType: "image/svg+xml", Data: testPNG(t, 1, 1, false)},
		"empty data":             {MediaType: "image/png"},
		"malformed png":          {MediaType: "image/png", Data: []byte("not a png")},
		"oversized encoded data": {MediaType: "image/png", Data: testPNG(t, 512, 512, true)},
		"dimensions too large":   {MediaType: "image/png", Data: testPNG(t, 513, 1, false)},
	}
	for name, icon := range tests {
		t.Run(name, func(t *testing.T) {
			descriptor := base
			descriptor.Branding = &Branding{Icon: &icon}
			if err := descriptor.Validate(); err == nil {
				t.Fatal("invalid branding was accepted")
			}
		})
	}
}

func testPNG(t *testing.T, width, height int, uncompressed bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if uncompressed {
				// Deterministic pseudo-random pixels make a valid PNG larger than
				// the descriptor byte limit without relying on external fixtures.
				v := uint32(x+1)*0x9e3779b9 ^ uint32(y+1)*0x85ebca6b
				img.SetNRGBA(x, y, color.NRGBA{R: byte(v), G: byte(v >> 8), B: byte(v >> 16), A: 255})
			} else {
				img.SetNRGBA(x, y, color.NRGBA{R: 30, G: 80, B: 140, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	encoder := png.Encoder{}
	if uncompressed {
		encoder.CompressionLevel = png.NoCompression
	}
	if err := encoder.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestResourceReferencesAreOpaqueButBounded(t *testing.T) {
	ref := &ResourceRef{Type: "unrecognized.type", ID: "id/with/slashes", Parent: &ResourceRef{Type: "parent", ID: "opaque id"}}
	if err := ValidateResourceRef(ref); err != nil {
		t.Fatalf("arbitrary resource reference rejected: %v", err)
	}
	if err := ValidateResourceRef(&ResourceRef{Type: "", ID: "id"}); err == nil {
		t.Fatal("empty resource type accepted")
	}
	cyclic := &ResourceRef{Type: "type", ID: "id"}
	cyclic.Parent = cyclic
	if err := ValidateResourceRef(cyclic); err == nil {
		t.Fatal("cyclic resource hierarchy accepted")
	}
	var deep *ResourceRef
	for i := 0; i < 65; i++ {
		deep = &ResourceRef{Type: "opaque", ID: "opaque", Parent: deep}
	}
	if err := ValidateResourceRef(deep); err == nil {
		t.Fatal("unbounded resource hierarchy accepted")
	}
}

func TestDescriptorValidatesOpaqueResourceDeclarations(t *testing.T) {
	d := Descriptor{ID: "adapter", Name: "Adapter", Version: "1", ProtocolVersion: Version, Capabilities: []string{CapabilityResolve}, InputSchema: Schema{Fields: []Field{}}, ConfigurationSchema: Schema{Fields: []Field{}}, ResourceTypes: []ResourceType{{Type: "opaque.resource", ParentTypes: []string{"opaque.parent"}, ConfigurationSchema: Schema{Fields: []Field{}}}, {Type: "opaque.parent", ConfigurationSchema: Schema{Fields: []Field{}}}}, MediaTypes: []string{"hls"}}
	if err := d.Validate(); err != nil {
		t.Fatalf("valid opaque resource declaration rejected: %v", err)
	}
	d.ResourceTypes[0].ParentTypes = []string{"opaque.parent", "opaque.parent"}
	if err := d.Validate(); err == nil {
		t.Fatal("duplicate parent declaration accepted")
	}
	d.ResourceTypes = []ResourceType{{Type: "alpha", ParentTypes: []string{"beta"}}, {Type: "beta", ParentTypes: []string{"alpha"}}}
	if err := d.Validate(); err == nil {
		t.Fatal("cyclic resource declarations accepted")
	}
}

func TestDescriptorForwardCompatibleCapabilitiesAndResourceEdges(t *testing.T) {
	d := Descriptor{ID: "adapter.v1", Name: "Adapter", Version: "1", ProtocolVersion: Version, Capabilities: []string{CapabilityResolve, "optional.ext"}, InputSchema: Schema{}, ConfigurationSchema: Schema{}, ResourceTypes: []ResourceType{{Type: "alpha"}, {Type: "beta", ParentTypes: []string{"alpha"}}}, MediaTypes: []string{"hls"}}
	if err := d.Validate(); err != nil {
		t.Fatalf("optional capability rejected: %v", err)
	}
	if err := ValidateResourceRefForDescriptor(d, &ResourceRef{Type: "beta", ID: "b", Parent: &ResourceRef{Type: "alpha", ID: "a"}}); err != nil {
		t.Fatalf("declared edge rejected: %v", err)
	}
	if err := ValidateResourceRefForDescriptor(d, &ResourceRef{Type: "beta", ID: "root"}); err != nil {
		t.Fatalf("declared type with optional parent edge rejected as a root: %v", err)
	}
	for _, ref := range []*ResourceRef{
		{Type: "gamma", ID: "g"},
		{Type: "beta", ID: "b", Parent: &ResourceRef{Type: "gamma", ID: "g"}},
	} {
		if err := ValidateResourceRefForDescriptor(d, ref); err == nil {
			t.Fatalf("undeclared chain accepted: %#v", ref)
		}
	}
	d.Capabilities = append(d.Capabilities, "bad capability")
	if err := d.Validate(); err == nil {
		t.Fatal("malformed capability accepted")
	}
}

func TestWatchCheckResultValidationAndWireShape(t *testing.T) {
	media := MediaSource{Type: "hls", ManifestURL: "https://stream.example/live.m3u8"}
	result := WatchCheckResult{State: "live", SessionRef: "opaque-session", Title: "Example", Media: &media}
	if err := result.Validate([]string{"hls"}); err != nil {
		t.Fatalf("valid live watch result rejected: %v", err)
	}
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(wire, &shape); err != nil {
		t.Fatal(err)
	}
	for key := range map[string]bool{"state": true, "session_ref": true, "title": true, "media": true} {
		if _, ok := shape[key]; !ok {
			t.Errorf("watch result wire shape missing %q: %s", key, wire)
		}
	}

	invalid := []WatchCheckResult{
		{State: "unknown"},
		{State: "offline", SessionRef: strings.Repeat("x", 4097)},
		{State: "offline", SessionRef: string([]byte{0xff})},
		{State: "offline", Media: &media},
		{State: "live", Media: &MediaSource{Type: "hls", ManifestURL: "file:///tmp/unsafe.m3u8"}},
		{State: "live", StartedAt: &time.Time{}},
		{State: "offline", StateMutations: make([]StateMutation, 65)},
	}
	for i, candidate := range invalid {
		if err := candidate.Validate([]string{"hls"}); err == nil {
			t.Errorf("invalid watch result %d accepted", i)
		}
	}

	for _, malformed := range []string{
		`{"state":"live","started_at":"not-a-time"}`,
		`{"state":"live","media":{"type":"unknown","manifest_url":"https://stream.example/live.m3u8"}}`,
	} {
		var decoded WatchCheckResult
		if err := json.Unmarshal([]byte(malformed), &decoded); err == nil && decoded.Validate([]string{"hls"}) == nil {
			t.Errorf("malformed watch JSON accepted: %s", malformed)
		}
	}
}

func TestNumericOptionsUseJSONSemanticEqualityAndRejectDuplicates(t *testing.T) {
	var schema Schema
	if err := json.Unmarshal([]byte(`{"fields":[{"key":"one","control":"select","label":"One","options":[{"value":1,"label":"One"},{"value":2,"label":"Two"}]},{"key":"many","control":"multi-select","label":"Many","constraints":{"min_items":2},"options":[{"value":1,"label":"One"},{"value":2,"label":"Two"}]}]}`), &schema); err != nil {
		t.Fatal(err)
	}
	if err := ValidateValues(schema, map[string]json.RawMessage{"one": json.RawMessage(`1.0`), "many": json.RawMessage(`[1.0,2e0]`)}); err != nil {
		t.Fatalf("equivalent numeric option representations rejected: %v", err)
	}
	if err := ValidateValues(schema, map[string]json.RawMessage{"many": json.RawMessage(`[1,1.0]`)}); err == nil {
		t.Fatal("duplicate multi-select values satisfied min_items")
	}
	var duplicate Schema
	if err := json.Unmarshal([]byte(`{"fields":[{"key":"one","control":"select","label":"One","options":[{"value":1,"label":"One"},{"value":1.0,"label":"Duplicate"}]}]}`), &duplicate); err != nil {
		t.Fatal(err)
	}
	if err := duplicate.Validate(); err == nil {
		t.Fatal("semantically duplicate numeric options were accepted")
	}
}

func TestNumericCanonicalizationDoesNotExpandLargeExponent(t *testing.T) {
	var schema Schema
	if err := json.Unmarshal([]byte(`{"fields":[{"key":"value","control":"select","label":"Value","options":[{"value":2e1000000000,"label":"Large"}]}]}`), &schema); err != nil {
		t.Fatal(err)
	}
	if err := ValidateValues(schema, map[string]json.RawMessage{"value": json.RawMessage(`20e999999999`)}); err != nil {
		t.Fatalf("equivalent large decimal exponent rejected: %v", err)
	}
	var duplicates Schema
	if err := json.Unmarshal([]byte(`{"fields":[{"key":"value","control":"select","label":"Value","options":[{"value":2e1000000000,"label":"Large"},{"value":20e999999999,"label":"Equivalent"}]}]}`), &duplicates); err != nil {
		t.Fatal(err)
	}
	if err := duplicates.Validate(); err == nil {
		t.Fatal("equivalent large exponents were not identified as duplicate options")
	}
}

func TestResolveDescriptorRequiresDeclaredMediaType(t *testing.T) {
	d := Descriptor{ID: "adapter", Name: "Adapter", Version: "1", ProtocolVersion: Version, Capabilities: []string{CapabilityResolve}, InputSchema: Schema{}, ConfigurationSchema: Schema{}}
	if err := d.Validate(); err == nil {
		t.Fatal("resolve descriptor without media types was accepted")
	}
	d.MediaTypes = []string{"hls"}
	if err := d.Validate(); err != nil {
		t.Fatalf("valid resolve descriptor rejected: %v", err)
	}
}

func TestSchemaDefaultsConstraintsOptionsAndVisibility(t *testing.T) {
	schema := Schema{Fields: []Field{
		{Key: "mode", Control: "select", Label: "Mode", Required: true, Default: json.RawMessage(`"basic"`), Options: []Option{{Value: "basic", Label: "Basic"}, {Value: "advanced", Label: "Advanced"}}},
		{Key: "count", Control: "number", Label: "Count", Default: json.RawMessage(`2`), Constraints: &Constraints{Min: floatPtr(1), Max: floatPtr(4)}},
		{Key: "advanced_value", Control: "text", Label: "Advanced value", Required: true, VisibleWhen: json.RawMessage(`{"field":"mode","equals":"advanced"}`)},
	}}
	if err := schema.Validate(); err != nil {
		t.Fatal(err)
	}
	defaults := ApplyDefaults(schema, map[string]json.RawMessage{})
	if string(defaults["mode"]) != `"basic"` || string(defaults["count"]) != "2" {
		t.Fatalf("defaults = %#v", defaults)
	}
	if err := ValidateValues(schema, map[string]json.RawMessage{}); err != nil {
		t.Fatalf("hidden required field blocked omission: %v", err)
	}
	if err := ValidateValues(schema, map[string]json.RawMessage{"mode": json.RawMessage(`"advanced"`)}); err == nil {
		t.Fatal("visible required field accepted missing value")
	}
	if err := ValidateProvidedValues(schema, map[string]json.RawMessage{"advanced_value": json.RawMessage(`"stale"`)}); err == nil {
		t.Fatal("value for hidden field accepted")
	}
	visible, err := IsVisible(schema, "advanced_value", map[string]json.RawMessage{"mode": json.RawMessage(`"advanced"`)})
	if err != nil || !visible {
		t.Fatalf("visible condition result=%v, err=%v", visible, err)
	}
	for name, invalid := range map[string]Schema{
		"invalid number default":       {Fields: []Field{{Key: "count", Control: "number", Label: "Count", Default: json.RawMessage(`8`), Constraints: &Constraints{Max: floatPtr(4)}}}},
		"duplicate options":            {Fields: []Field{{Key: "mode", Control: "select", Label: "Mode", Options: []Option{{Value: map[string]any{"a": 1, "b": 2}, Label: "One"}, {Value: map[string]any{"b": 2, "a": 1}, Label: "Duplicate"}}}}},
		"bad item range":               {Fields: []Field{{Key: "items", Control: "multi-select", Label: "Items", Options: []Option{{Value: "x", Label: "X"}}, Constraints: &Constraints{MinItems: intPtr(3), MaxItems: intPtr(2)}}}},
		"visibility cycle":             {Fields: []Field{{Key: "a", Control: "boolean", Label: "A", VisibleWhen: json.RawMessage(`{"field":"b","truthy":true}`)}, {Key: "b", Control: "boolean", Label: "B", VisibleWhen: json.RawMessage(`{"field":"a","truthy":true}`)}}},
		"self visibility":              {Fields: []Field{{Key: "a", Control: "text", Label: "A", VisibleWhen: json.RawMessage(`{"field":"a","truthy":true}`)}}},
		"truthy requires boolean":      {Fields: []Field{{Key: "a", Control: "text", Label: "A"}, {Key: "b", Control: "text", Label: "B", VisibleWhen: json.RawMessage(`{"field":"a","truthy":true}`)}}},
		"secret visibility dependency": {Fields: []Field{{Key: "secret", Control: "secret", Label: "Secret"}, {Key: "conditional", Control: "text", Label: "Conditional", VisibleWhen: json.RawMessage(`{"field":"secret","truthy":true}`)}}},
		"secret default":               {Fields: []Field{{Key: "secret", Control: "secret", Label: "Secret", Default: json.RawMessage(`"do-not-default-secrets"`)}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := invalid.Validate(); err == nil {
				t.Fatal("invalid schema accepted")
			}
		})
	}
}

func TestMediaHeadersUseTokenValidationAndRejectUnsafeDuplicates(t *testing.T) {
	base := MediaSource{Type: "hls", ManifestURL: "https://stream.example/live.m3u8"}
	for name, headers := range map[string]map[string]string{
		"invalid token":       {"Bad Header": "x"},
		"canonical duplicate": {"authorization": "a", "Authorization": "b"},
		"transport header":    {"Host": "evil.example"},
		"hop header":          {"Connection": "keep-alive"},
	} {
		t.Run(name, func(t *testing.T) {
			media := base
			media.Headers = headers
			if err := ValidateMediaSource(media, []string{"hls"}); err == nil {
				t.Fatal("unsafe header accepted")
			}
		})
	}
	base.Headers = map[string]string{"Authorization": "Bearer x", "Cookie": "a=b", "Referer": "https://example/", "Origin": "https://example/", "User-Agent": "test"}
	if err := ValidateMediaSource(base, []string{"hls"}); err != nil {
		t.Fatalf("normal media headers rejected: %v", err)
	}
}

func TestMediaRequestPolicyUsesExactOriginsAndRestrictiveDefaults(t *testing.T) {
	manifest, err := url.Parse("https://Example.com/live/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	defaults := MediaSource{ManifestURL: manifest.String()}
	for _, test := range []struct {
		name string
		url  string
		want bool
	}{
		{name: "same origin", url: "https://example.com/segment.ts", want: true},
		{name: "explicit default port", url: "https://example.com:443/segment.ts", want: true},
		{name: "similar hostname prefix", url: "https://evil-example.com/segment.ts"},
		{name: "different scheme", url: "http://example.com/segment.ts"},
		{name: "different port", url: "https://example.com:444/segment.ts"},
	} {
		t.Run(test.name, func(t *testing.T) {
			target, parseErr := url.Parse(test.url)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if got := defaults.AllowsHeadersFor(target); got != test.want {
				t.Fatalf("AllowsHeadersFor(%q) = %t, want %t", test.url, got, test.want)
			}
		})
	}

	allowlist := MediaSource{ManifestURL: manifest.String(), RequestPolicy: &RequestPolicy{HeaderForwarding: &HeaderForwardingPolicy{Mode: HeaderForwardingAllowlist, Origins: []string{"https://edge.example.net"}}}}
	for _, test := range []struct {
		name string
		url  string
		want bool
	}{
		{name: "listed cross origin", url: "https://EDGE.example.net:443/segment.ts", want: true},
		{name: "unlisted similar prefix", url: "https://evil-edge.example.net/segment.ts"},
		{name: "scheme is significant", url: "http://edge.example.net/segment.ts"},
		{name: "nondefault port is significant", url: "https://edge.example.net:444/segment.ts"},
	} {
		t.Run(test.name, func(t *testing.T) {
			target, parseErr := url.Parse(test.url)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if got := allowlist.AllowsHeadersFor(target); got != test.want {
				t.Fatalf("AllowsHeadersFor(%q) = %t, want %t", test.url, got, test.want)
			}
		})
	}
}

func TestValidateMediaSourceRequestPolicy(t *testing.T) {
	base := MediaSource{Type: "hls", ManifestURL: "https://stream.example/live.m3u8"}
	valid := []RequestPolicy{
		{},
		{HeaderForwarding: &HeaderForwardingPolicy{Mode: HeaderForwardingSameOrigin}},
		{HeaderForwarding: &HeaderForwardingPolicy{Mode: HeaderForwardingAllowlist, Origins: []string{"https://stream.example", "https://cdn.example:443/"}}},
	}
	for _, policy := range valid {
		media := base
		media.RequestPolicy = &policy
		if err := ValidateMediaSource(media, []string{"hls"}); err != nil {
			t.Errorf("valid policy rejected: %v", err)
		}
	}
	invalid := []RequestPolicy{
		{HeaderForwarding: &HeaderForwardingPolicy{Mode: HeaderForwardingSameOrigin, Origins: []string{"https://cdn.example"}}},
		{HeaderForwarding: &HeaderForwardingPolicy{Mode: HeaderForwardingAllowlist}},
		{HeaderForwarding: &HeaderForwardingPolicy{Mode: HeaderForwardingAllowlist, Origins: []string{"https://cdn.example/path"}}},
		{HeaderForwarding: &HeaderForwardingPolicy{Mode: HeaderForwardingAllowlist, Origins: []string{"https://cdn.example?token=x"}}},
		{HeaderForwarding: &HeaderForwardingPolicy{Mode: HeaderForwardingAllowlist, Origins: []string{"https://cdn.example#fragment"}}},
		{HeaderForwarding: &HeaderForwardingPolicy{Mode: HeaderForwardingAllowlist, Origins: []string{"https://user@cdn.example"}}},
		{HeaderForwarding: &HeaderForwardingPolicy{Mode: HeaderForwardingAllowlist, Origins: []string{"file://cdn.example"}}},
		{HeaderForwarding: &HeaderForwardingPolicy{Mode: "forward_everywhere"}},
	}
	for _, policy := range invalid {
		media := base
		media.RequestPolicy = &policy
		if err := ValidateMediaSource(media, []string{"hls"}); err == nil {
			t.Errorf("invalid policy accepted: %#v", policy)
		}
	}

	wire, err := json.Marshal(MediaSource{Type: "hls", ManifestURL: base.ManifestURL, RequestPolicy: &RequestPolicy{HeaderForwarding: &HeaderForwardingPolicy{Mode: HeaderForwardingAllowlist, Origins: []string{"https://cdn.example"}}}})
	if err != nil || !strings.Contains(string(wire), `"request_policy":{"header_forwarding"`) {
		t.Fatalf("request policy wire field missing: %s, %v", wire, err)
	}
}

func TestValidateObjectAgainstSchemaRejectsInvalidInputWithoutEchoingValues(t *testing.T) {
	schema := Schema{Fields: []Field{
		{Key: "text", Control: "text", Label: "Text", Required: true, Constraints: &Constraints{Pattern: "^[a-z]+$", MinLength: intPtr(2), MaxLength: intPtr(8)}},
		{Key: "choice", Control: "select", Label: "Choice", Options: []Option{{Value: "a", Label: "A"}, {Value: "b", Label: "B"}}},
		{Key: "count", Control: "number", Label: "Count", Constraints: &Constraints{Min: floatPtr(1), Max: floatPtr(4)}},
		{Key: "many", Control: "multi-select", Label: "Many", Options: []Option{{Value: "x", Label: "X"}, {Value: "y", Label: "Y"}}, Constraints: &Constraints{MaxItems: intPtr(1)}},
	}}
	valid := json.RawMessage(`{"text":"abc","choice":"a","count":3,"many":["x"]}`)
	if err := ValidateObjectAgainstSchema(schema, valid); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	for _, raw := range []string{
		`{"choice":"a","count":3,"many":["x"]}`,
		`{"text":false,"choice":"a","count":3,"many":["x"]}`,
		`{"text":"abc","choice":"other","count":3,"many":["x"]}`,
		`{"text":"abc","choice":"a","count":9,"many":["x"]}`,
		`{"text":"bad-value-sentinel","choice":"a","count":3,"many":["x"]}`,
		`{"text":"abc","choice":"a","count":3,"many":["x","y"]}`,
		`{"text":"abc","choice":"a","count":3,"many":["x"],"unknown":"sensitive-sentinel"}`,
	} {
		err := ValidateObjectAgainstSchema(schema, json.RawMessage(raw))
		if err == nil {
			t.Errorf("invalid input accepted: %s", raw)
		}
		if strings.Contains(fmt.Sprint(err), "sensitive-sentinel") || strings.Contains(fmt.Sprint(err), "bad-value-sentinel") {
			t.Errorf("validator echoed submitted value: %v", err)
		}
	}
}

func TestValidateResourceRefDepthLimit(t *testing.T) {
	var ref *ResourceRef
	for i := 0; i < 65; i++ {
		ref = &ResourceRef{Type: "opaque", ID: fmt.Sprint(i), Parent: ref}
	}
	if err := ValidateResourceRef(ref); err == nil {
		t.Fatal("over-depth resource chain accepted")
	}
}

func intPtr(v int) *int           { return &v }
func floatPtr(v float64) *float64 { return &v }
