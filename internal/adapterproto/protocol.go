// Package adapterproto defines the language-neutral stdio protocol shared by
// the Core and standalone adapter processes. Frames are one JSON object per
// line; protocol stdout must never contain diagnostic text.
package adapterproto

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	Version       = 1
	MaxFrameBytes = 8 << 20
)

// Capabilities name generic protocol behaviors and contain no platform domain.
const (
	CapabilityResolve     = "resolve"
	CapabilityStatus      = "status"
	CapabilityConfigure   = "configure"
	CapabilityInteraction = "interaction"
	CapabilityMetadata    = "metadata"
	CapabilityEvents      = "events"
	CapabilityRefresh     = "refresh"
)

const (
	MethodDescribe            = "describe"
	MethodResolve             = "resolve"
	MethodShutdown            = "shutdown"
	MethodGetStatus           = "get_status"
	MethodConfigure           = "configure"
	MethodInteractionBegin    = "interaction.begin"
	MethodInteractionContinue = "interaction.continue"
	MethodMetadata            = "metadata"
	MethodEvents              = "events"
	MethodRefresh             = "refresh"
)

type Request struct {
	ProtocolVersion int             `json:"protocol_version"`
	ID              string          `json:"id"`
	Method          string          `json:"method"`
	Params          json.RawMessage `json:"params,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

type Response struct {
	ProtocolVersion int             `json:"protocol_version"`
	ID              string          `json:"id"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           *Error          `json:"error,omitempty"`
}

func Success(id string, result any) (Response, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return Response{}, err
	}
	return Response{ProtocolVersion: Version, ID: id, Result: data}, nil
}

func Failure(id, code, message string, details any) Response {
	return Response{ProtocolVersion: Version, ID: id, Error: &Error{Code: code, Message: message, Details: details}}
}

// ReadRequest reads one bounded JSON frame. A clean EOF between frames is
// returned unchanged; malformed or oversized frames are explicit errors.
func ReadRequest(r *bufio.Reader) (Request, error) {
	line, err := readFrame(r)
	if err != nil {
		return Request{}, err
	}
	var request Request
	if err = json.Unmarshal(line, &request); err != nil {
		return Request{}, fmt.Errorf("malformed request JSON: %w", err)
	}
	if request.ProtocolVersion != Version {
		return request, fmt.Errorf("unsupported protocol version %d", request.ProtocolVersion)
	}
	if strings.TrimSpace(request.ID) == "" || strings.TrimSpace(request.Method) == "" {
		return request, errors.New("request id and method are required")
	}
	return request, nil
}

func ReadResponse(r *bufio.Reader) (Response, error) {
	line, err := readFrame(r)
	if err != nil {
		return Response{}, err
	}
	var response Response
	if err = json.Unmarshal(line, &response); err != nil {
		return Response{}, fmt.Errorf("malformed response JSON: %w", err)
	}
	if response.ProtocolVersion != Version {
		return response, fmt.Errorf("unsupported protocol version %d", response.ProtocolVersion)
	}
	if strings.TrimSpace(response.ID) == "" {
		return response, errors.New("response id is required")
	}
	if response.Error == nil && len(response.Result) == 0 {
		return response, errors.New("response must contain result or error")
	}
	if response.Error != nil && len(response.Result) != 0 {
		return response, errors.New("response cannot contain both result and error")
	}
	if response.Error != nil && response.Error.Code == "" {
		return response, errors.New("structured error code is required")
	}
	return response, nil
}

func WriteRequest(w io.Writer, request Request) error    { return writeFrame(w, request) }
func WriteResponse(w io.Writer, response Response) error { return writeFrame(w, response) }

func readFrame(r *bufio.Reader) ([]byte, error) {
	var frame []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(frame)+len(chunk) > MaxFrameBytes+1 {
			return nil, fmt.Errorf("protocol frame exceeds %d bytes", MaxFrameBytes)
		}
		frame = append(frame, chunk...)
		if err == nil {
			frame = frame[:len(frame)-1]
			if len(frame) == 0 || len(frame) > MaxFrameBytes {
				return nil, errors.New("empty or oversized protocol frame")
			}
			return frame, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) && len(frame) == 0 {
			return nil, io.EOF
		}
		if errors.Is(err, io.EOF) {
			return nil, errors.New("unterminated protocol frame")
		}
		return nil, err
	}
}

func writeFrame(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > MaxFrameBytes {
		return fmt.Errorf("protocol frame exceeds %d bytes", MaxFrameBytes)
	}
	data = append(data, '\n')
	for len(data) != 0 {
		n, writeErr := w.Write(data)
		if writeErr != nil {
			return writeErr
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
