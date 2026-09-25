package adapterhost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
)

const defaultCallTimeout = 15 * time.Second

type process struct {
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	requests     chan struct{}
	responses    chan readResult
	done         chan struct{}
	killOnce     sync.Once
	shutdownOnce sync.Once
	sequence     atomic.Uint64
	stderr       *boundedCapture
}

type readResult struct {
	response adapterproto.Response
	err      error
}

type boundedCapture struct {
	mu    sync.Mutex
	bytes []byte
	limit int
}

func (b *boundedCapture) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(b.bytes) < b.limit {
		remaining := b.limit - len(b.bytes)
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.bytes = append(b.bytes, p...)
	}
	return n, nil
}

func startProcess(path string) (*process, error) {
	cmd := exec.Command(path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &boundedCapture{limit: 4096}
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	p := &process{cmd: cmd, stdin: stdin, requests: make(chan struct{}, 1), responses: make(chan readResult, 1), done: make(chan struct{}), stderr: stderr}
	p.requests <- struct{}{}
	go p.readLoop(bufio.NewReaderSize(stdout, 32<<10))
	go func() { _ = cmd.Wait(); close(p.done) }()
	return p, nil
}

func (p *process) readLoop(reader *bufio.Reader) {
	for {
		response, err := adapterproto.ReadResponse(reader)
		select {
		case p.responses <- readResult{response: response, err: err}:
		case <-p.done:
			return
		}
		if err != nil {
			return
		}
	}
}

func (p *process) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultCallTimeout)
		defer cancel()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.done:
		return nil, errors.New("adapter process exited")
	case <-p.requests:
	}
	defer func() {
		select {
		case p.requests <- struct{}{}:
		default:
		}
	}()
	id := fmt.Sprintf("%d", p.sequence.Add(1))
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	request := adapterproto.Request{ProtocolVersion: adapterproto.Version, ID: id, Method: method, Params: paramsJSON}
	if err = adapterproto.WriteRequest(p.stdin, request); err != nil {
		p.kill()
		return nil, errors.New("adapter request failed")
	}
	select {
	case <-ctx.Done():
		p.kill()
		return nil, fmt.Errorf("adapter request timed out or was canceled")
	case <-p.done:
		return nil, errors.New("adapter process exited")
	case result := <-p.responses:
		if result.err != nil {
			p.kill()
			if errors.Is(result.err, io.EOF) {
				return nil, errors.New("adapter closed protocol output")
			}
			return nil, errors.New("adapter returned malformed protocol output")
		}
		if result.response.ID != id {
			p.kill()
			return nil, errors.New("adapter response id mismatch")
		}
		if result.response.ProtocolVersion != adapterproto.Version {
			p.kill()
			return nil, errors.New("adapter protocol version mismatch")
		}
		if result.response.Error != nil {
			// Adapter-controlled error fields can echo values from request params.
			// Keep every field private and return only a stable generic error.
			return nil, errors.New("adapter returned an error")
		}
		return result.response.Result, nil
	}
}

func (p *process) shutdown() {
	p.shutdownOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = p.call(ctx, adapterproto.MethodShutdown, map[string]any{})
		_ = p.stdin.Close()
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
			p.kill()
			<-p.done
		}
	})
}

func (p *process) kill() {
	p.killOnce.Do(func() {
		_ = p.stdin.Close()
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
	})
}
