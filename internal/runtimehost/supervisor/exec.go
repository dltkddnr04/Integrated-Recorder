package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

const (
	maxArguments       = 128
	maxEnvironmentVars = 256
	maxProcessText     = 32 << 10
	maxEnvironmentSize = 128 << 10
)

var environmentKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ExecLauncher starts an immutable installed executable directly. It never
// uses a shell and does not bind child lifetime to the Start context.
type ExecLauncher struct{}

func (ExecLauncher) Start(ctx context.Context, spec ProcessSpec) (Child, error) {
	if ctx == nil {
		return nil, ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateProcessSpec(spec); err != nil {
		return nil, err
	}
	cmd := exec.Command(spec.Executable, spec.Args...)
	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	}
	env, err := childEnvironment(spec.Env)
	if err != nil {
		return nil, err
	}
	cmd.Env = env
	// Child logs may contain source or credential material. The Host does not
	// capture unbounded output or relay it to Runtime Host logs.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, errors.New("runtime child process could not be started")
	}
	child := &execChild{cmd: cmd, done: make(chan struct{}), stopGate: make(chan struct{}, 1)}
	go child.wait()
	return child, nil
}

func runtimeExecutableNeedsModeCheck() bool { return runtime.GOOS != "windows" }

func validateProcessSpec(spec ProcessSpec) error {
	if !validGenerationID(spec.GenerationID) || (spec.Role != RoleControl && spec.Role != RoleEngine) {
		return ErrInvalidProcess
	}
	if len(spec.Executable) == 0 || len(spec.Executable) > 4096 || !filepath.IsAbs(spec.Executable) || strings.ContainsRune(spec.Executable, 0) {
		return ErrInvalidProcess
	}
	info, err := os.Lstat(spec.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidProcess
	}
	if runtimeExecutableNeedsModeCheck() && info.Mode().Perm()&0111 == 0 {
		return ErrInvalidProcess
	}
	if spec.Dir != "" {
		if len(spec.Dir) > 4096 || !filepath.IsAbs(spec.Dir) || strings.ContainsRune(spec.Dir, 0) {
			return ErrInvalidProcess
		}
		dirInfo, statErr := os.Lstat(spec.Dir)
		if statErr != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidProcess
		}
	}
	if len(spec.Args) > maxArguments {
		return ErrInvalidProcess
	}
	total := 0
	for _, arg := range spec.Args {
		if len(arg) > maxProcessText || strings.ContainsRune(arg, 0) {
			return ErrInvalidProcess
		}
		total += len(arg)
		if total > maxEnvironmentSize {
			return ErrInvalidProcess
		}
	}
	if len(spec.Endpoint) > 2048 || strings.ContainsRune(spec.Endpoint, 0) {
		return ErrInvalidProcess
	}
	if _, err := childEnvironment(spec.Env); err != nil {
		return err
	}
	return nil
}

func childEnvironment(overrides []string) ([]string, error) {
	if len(overrides) > maxEnvironmentVars {
		return nil, ErrInvalidProcess
	}
	// A generation process receives only the environment declared by the
	// Runtime Host. Inheriting the Host's full environment could disclose
	// signing credentials or deployment secrets to an application generation.
	values := make(map[string]string, len(overrides))
	seenOverrides := make(map[string]struct{}, len(overrides))
	for _, item := range overrides {
		key, value, ok := strings.Cut(item, "=")
		if !ok || !environmentKey.MatchString(key) || len(value) > maxProcessText || strings.ContainsRune(value, 0) {
			return nil, ErrInvalidProcess
		}
		if _, duplicate := seenOverrides[key]; duplicate {
			return nil, ErrInvalidProcess
		}
		seenOverrides[key] = struct{}{}
		values[key] = value
	}
	out := make([]string, 0, len(values))
	total := 0
	for key, value := range values {
		entry := key + "=" + value
		total += len(entry)
		if total > maxEnvironmentSize {
			return nil, ErrInvalidProcess
		}
		out = append(out, entry)
	}
	return out, nil
}

type execChild struct {
	cmd      *exec.Cmd
	done     chan struct{}
	stopGate chan struct{}
	once     sync.Once
	mu       sync.RWMutex
	err      error
}

func (c *execChild) wait() {
	err := c.cmd.Wait()
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
	c.once.Do(func() { close(c.done) })
}

func (c *execChild) Done() <-chan struct{} { return c.done }

func (c *execChild) Err() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.err
}

func (c *execChild) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case c.stopGate <- struct{}{}:
		defer func() { <-c.stopGate }()
	case <-ctx.Done():
		_ = c.Kill()
		return ctx.Err()
	}
	select {
	case <-c.done:
		return nil
	default:
	}
	if c.cmd.Process == nil {
		return errors.New("runtime child process is unavailable")
	}
	if err := c.cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		_ = c.Kill()
		return fmt.Errorf("signal runtime child: %w", err)
	}
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		select {
		case <-c.done:
			return nil
		default:
		}
		_ = c.Kill()
		return ctx.Err()
	}
}

func (c *execChild) Kill() error {
	select {
	case <-c.done:
		return nil
	default:
	}
	if c.cmd.Process == nil {
		return nil
	}
	err := c.cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
