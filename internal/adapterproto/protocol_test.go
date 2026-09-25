package adapterproto

import (
	"bufio"
	"bytes"
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
