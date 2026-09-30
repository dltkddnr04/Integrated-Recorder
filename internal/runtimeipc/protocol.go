// Package runtimeipc implements the private, versioned local protocol used
// between the runtime host, control plane, and recorder engine.
package runtimeipc

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	ProtocolVersion = 1
	MaxFrameBytes   = 8 << 20
)

var (
	ErrFrameTooLarge  = errors.New("runtime IPC frame exceeds size limit")
	ErrMalformedFrame = errors.New("runtime IPC frame is malformed")
)

// Request is one framed operation. AuthToken is encoded as standard base64 by
// encoding/json and contains exactly 32 random bytes at runtime.
type Request struct {
	ProtocolVersion  int             `json:"protocol_version"`
	RequestID        string          `json:"request_id"`
	GenerationID     string          `json:"generation_id"`
	ClientInstanceID string          `json:"client_instance_id"`
	AuthToken        []byte          `json:"auth_token"`
	Deadline         int64           `json:"deadline_unix_nano"`
	Operation        string          `json:"operation"`
	Payload          json.RawMessage `json:"payload,omitempty"`
}

// Response includes generation identity so a client cannot accidentally use
// a stale/replaced engine socket and mistake it for the selected generation.
type Response struct {
	ProtocolVersion int             `json:"protocol_version"`
	RequestID       string          `json:"request_id"`
	GenerationID    string          `json:"generation_id"`
	InstanceID      string          `json:"instance_id"`
	OK              bool            `json:"ok"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           *ResponseError  `json:"error,omitempty"`
}

type ResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func readFrame(r io.Reader, dst any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 {
		return ErrMalformedFrame
	}
	if size > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(r, data); err != nil {
		return fmt.Errorf("%w: incomplete body", ErrMalformedFrame)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("%w: invalid JSON", ErrMalformedFrame)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing JSON data", ErrMalformedFrame)
	}
	return nil
}

func writeFrame(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, data)
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
