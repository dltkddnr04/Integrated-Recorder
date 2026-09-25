package adapterproto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
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

func TestRejectsUnsupportedVersionsMalformedAndOversizedFrames(t *testing.T) {
	for name, line := range map[string]string{"unsupported": `{"protocol_version":2,"id":"1","method":"describe"}` + "\n", "malformed": "{bad json}\n", "oversized": strings.Repeat("x", MaxFrameBytes+10) + "\n"} {
		t.Run(name, func(t *testing.T) {
			_, err := ReadRequest(bufio.NewReader(strings.NewReader(line)))
			if err == nil {
				t.Fatal("expected frame rejection")
			}
			if name == "unsupported" && !strings.Contains(err.Error(), "unsupported protocol version") {
				t.Fatalf("error = %v", err)
			}
		})
	}
	_, err := ReadResponse(bufio.NewReader(strings.NewReader(`{"protocol_version":2,"id":"1","result":{}}` + "\n")))
	if err == nil || !strings.Contains(err.Error(), "unsupported protocol version") {
		t.Fatalf("response error = %v", err)
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

func TestDescriptorSchemaValidation(t *testing.T) {
	d := Descriptor{ID: "adapter", Name: "Adapter", Version: "1", ProtocolVersion: Version, InputSchema: Schema{Fields: []Field{{Key: "mode", Control: "select", Label: "Mode", Options: []Option{{Value: "one", Label: "One"}}}}}, ConfigurationSchema: Schema{Fields: []Field{}}, MediaTypes: []string{"hls"}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	d.InputSchema.Fields = append(d.InputSchema.Fields, Field{Key: "mode", Control: "text", Label: "Duplicate"})
	if err := d.Validate(); err == nil {
		t.Fatal("duplicate schema key accepted")
	}
}

func TestResourceReferencesAreOpaqueButBounded(t *testing.T) {
	ref := &ResourceRef{Type: "unrecognized/type", ID: "id/with/slashes", Parent: &ResourceRef{Type: "parent", ID: "opaque id"}}
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
	d := Descriptor{ID: "adapter", Name: "Adapter", Version: "1", ProtocolVersion: Version, InputSchema: Schema{Fields: []Field{}}, ConfigurationSchema: Schema{Fields: []Field{}}, ResourceTypes: []ResourceType{{Type: "opaque.resource", ParentTypes: []string{"opaque.parent"}, ConfigurationSchema: Schema{Fields: []Field{}}}}, MediaTypes: []string{"hls"}}
	if err := d.Validate(); err != nil {
		t.Fatalf("valid opaque resource declaration rejected: %v", err)
	}
	d.ResourceTypes[0].ParentTypes = []string{"opaque.parent", "opaque.parent"}
	if err := d.Validate(); err == nil {
		t.Fatal("duplicate parent declaration accepted")
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
