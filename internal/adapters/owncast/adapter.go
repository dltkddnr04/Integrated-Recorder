// Package owncast implements the standalone first adapter. It is imported
// only by cmd/adapters/owncast and its tests, never by the Core.
package owncast

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
)

const StreamPath = "/hls/stream.m3u8"

func Describe() adapterproto.Descriptor {
	return adapterproto.Descriptor{ID: "owncast", Name: "Owncast", Version: "0.1.0", ProtocolVersion: adapterproto.Version, Capabilities: []string{"resolve"}, InputSchema: adapterproto.Schema{Fields: []adapterproto.Field{{Key: "source_url", Control: "text", Label: "Owncast instance URL", Description: "Base URL of an instance.", Required: true}}}, ConfigurationSchema: adapterproto.Schema{Fields: []adapterproto.Field{}}, ResourceTypes: []adapterproto.ResourceType{}, MediaTypes: []string{"hls"}}
}

func Resolve(input json.RawMessage) (adapterproto.MediaSource, error) {
	if err := adapterproto.ValidateObject(input); err != nil {
		return adapterproto.MediaSource{}, err
	}
	var values struct {
		SourceURL string `json:"source_url"`
	}
	if err := json.Unmarshal(input, &values); err != nil {
		return adapterproto.MediaSource{}, fmt.Errorf("invalid input")
	}
	u, err := url.Parse(strings.TrimSpace(values.SourceURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Opaque != "" {
		return adapterproto.MediaSource{}, fmt.Errorf("source_url must be an http or https instance URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + StreamPath
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return adapterproto.MediaSource{Type: "hls", ManifestURL: u.String()}, nil
}

func Serve(input io.Reader, output io.Writer) error {
	reader := bufio.NewReaderSize(input, 32<<10)
	for {
		request, err := adapterproto.ReadRequest(reader)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			id := request.ID
			if id == "" {
				id = "0"
			}
			code := "malformed_request"
			if strings.Contains(err.Error(), "unsupported protocol version") {
				code = "unsupported_protocol_version"
			}
			_ = adapterproto.WriteResponse(output, adapterproto.Failure(id, code, "invalid adapter request", nil))
			if code == "malformed_request" {
				return nil
			}
			continue
		}
		var response adapterproto.Response
		switch request.Method {
		case adapterproto.MethodDescribe:
			response, _ = adapterproto.Success(request.ID, Describe())
		case adapterproto.MethodResolve:
			var params adapterproto.ResolveParams
			if err = json.Unmarshal(request.Params, &params); err != nil {
				response = adapterproto.Failure(request.ID, "invalid_params", "resolve params are invalid", nil)
			} else {
				media, resolveErr := Resolve(params.Input)
				if resolveErr != nil {
					response = adapterproto.Failure(request.ID, "invalid_input", "input is invalid", nil)
				} else {
					response, _ = adapterproto.Success(request.ID, media)
				}
			}
		case adapterproto.MethodShutdown:
			response, _ = adapterproto.Success(request.ID, map[string]bool{"stopped": true})
		default:
			response = adapterproto.Failure(request.ID, "unsupported_method", "method is not supported", map[string]any{"method": request.Method})
		}
		if err = adapterproto.WriteResponse(output, response); err != nil {
			return err
		}
		if request.Method == adapterproto.MethodShutdown {
			return nil
		}
	}
}
