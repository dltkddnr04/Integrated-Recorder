// Package owncast implements the standalone first adapter. It is imported
// only by cmd/adapters/owncast and its tests, never by the Core.
package owncast

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
	"github.com/dltkddnr04/integrated-recorder/internal/network"
)

const StreamPath = "/hls/stream.m3u8"
const statusPath = "/api/status"
const maxStatusBytes = 64 << 10

type watchCheckError struct{ code string }

func (e *watchCheckError) Error() string { return "status service requires attention" }

func watchCheckFailure(id string, err error) adapterproto.Response {
	code := "watch_check_failed"
	var checkErr *watchCheckError
	if errors.As(err, &checkErr) && checkErr.code == "authentication_required" {
		code = checkErr.code
	}
	return adapterproto.Failure(id, code, "status check failed", nil)
}

//go:embed assets/owncast-logo.png
var owncastLogo []byte

func Describe() adapterproto.Descriptor {
	return adapterproto.Descriptor{
		ID: "owncast", Name: "Owncast", Version: "0.1.0", ProtocolVersion: adapterproto.Version,
		Capabilities:        []string{adapterproto.CapabilityResolve, adapterproto.CapabilityWatch},
		InputSchema:         adapterproto.Schema{Fields: []adapterproto.Field{{Key: "source_url", Control: "text", Label: "Owncast 인스턴스 URL", Description: "Owncast 인스턴스의 기본 URL입니다.", Required: true}}},
		ConfigurationSchema: adapterproto.Schema{Fields: []adapterproto.Field{}}, ResourceTypes: []adapterproto.ResourceType{}, MediaTypes: []string{"hls"},
		Branding: &adapterproto.Branding{Icon: &adapterproto.BrandIcon{MediaType: "image/png", Data: append([]byte(nil), owncastLogo...)}},
	}
}

// WatchCheck uses Owncast's status endpoint to distinguish a successful
// offline observation from transport or server errors. Production calls use
// the shared public-network client and explicit URL validation; tests may pass
// an injected client and validator for a local httptest server.
func WatchCheck(input json.RawMessage) (adapterproto.WatchCheckResult, error) {
	return WatchCheckWith(input, network.NewPublicHTTPClient(8*time.Second), network.ValidatePublicURL)
}

func WatchCheckWith(input json.RawMessage, client *http.Client, validate func(context.Context, string) error) (adapterproto.WatchCheckResult, error) {
	if client == nil || validate == nil {
		return adapterproto.WatchCheckResult{}, fmt.Errorf("Owncast status dependencies are unavailable")
	}
	media, err := Resolve(input)
	if err != nil {
		return adapterproto.WatchCheckResult{}, err
	}
	manifest, err := url.Parse(media.ManifestURL)
	if err != nil {
		return adapterproto.WatchCheckResult{}, fmt.Errorf("invalid source URL")
	}
	statusURL := *manifest
	statusURL.Path = strings.TrimSuffix(strings.TrimSuffix(statusURL.Path, StreamPath), "/") + statusPath
	statusURL.RawPath = ""
	statusURL.RawQuery = ""
	statusURL.Fragment = ""
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := validate(ctx, statusURL.String()); err != nil {
		return adapterproto.WatchCheckResult{}, fmt.Errorf("status URL is not allowed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL.String(), nil)
	if err != nil {
		return adapterproto.WatchCheckResult{}, fmt.Errorf("status request could not be created")
	}
	response, err := client.Do(request)
	if err != nil {
		return adapterproto.WatchCheckResult{}, fmt.Errorf("status request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return adapterproto.WatchCheckResult{}, &watchCheckError{code: "authentication_required"}
		}
		return adapterproto.WatchCheckResult{}, fmt.Errorf("status service returned an error")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxStatusBytes+1))
	if err != nil || len(data) > maxStatusBytes {
		return adapterproto.WatchCheckResult{}, fmt.Errorf("status response is invalid")
	}
	var status struct {
		Online          *bool           `json:"online"`
		LastConnectTime json.RawMessage `json:"lastConnectTime,omitempty"`
	}
	if err := json.Unmarshal(data, &status); err != nil || status.Online == nil {
		return adapterproto.WatchCheckResult{}, fmt.Errorf("status response is invalid")
	}
	if !*status.Online {
		return adapterproto.WatchCheckResult{State: "offline"}, nil
	}
	result := adapterproto.WatchCheckResult{State: "live", Media: &media}
	if len(status.LastConnectTime) > 0 && string(status.LastConnectTime) != "null" {
		var value string
		if json.Unmarshal(status.LastConnectTime, &value) != nil || len(value) > 128 {
			return adapterproto.WatchCheckResult{}, fmt.Errorf("status response is invalid")
		}
		startedAt, parseErr := time.Parse(time.RFC3339, value)
		if parseErr != nil {
			return adapterproto.WatchCheckResult{}, fmt.Errorf("status response is invalid")
		}
		startedAt = startedAt.UTC()
		result.SessionRef = value
		result.StartedAt = &startedAt
	}
	if err := result.Validate([]string{"hls"}); err != nil {
		return adapterproto.WatchCheckResult{}, fmt.Errorf("status response is invalid")
	}
	return result, nil
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
		case adapterproto.MethodWatchCheck:
			var params adapterproto.WatchCheckParams
			if err = json.Unmarshal(request.Params, &params); err != nil {
				response = adapterproto.Failure(request.ID, "invalid_params", "watch check params are invalid", nil)
			} else {
				result, checkErr := WatchCheck(params.Input)
				if checkErr != nil {
					response = watchCheckFailure(request.ID, checkErr)
				} else {
					response, _ = adapterproto.Success(request.ID, result)
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
