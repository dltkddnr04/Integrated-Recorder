//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !solaris

package storageproto

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os/exec"
	"time"
)

func unixTransport(_ string) *http.Transport {
	return &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("Unix domain sockets are not supported on this platform")
	}}
}

func stopProcess(cmd *exec.Cmd, done <-chan struct{}, parentPipe interface{ Close() error }) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if parentPipe != nil {
		_ = parentPipe.Close()
	}
	select {
	case <-done:
		return nil
	case <-time.After(500 * time.Millisecond):
	}
	_ = cmd.Process.Kill()
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("storage provider process did not exit")
	}
}
