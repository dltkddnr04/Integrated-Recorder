// Package supervisor owns the stable Runtime Host listener and the independent
// application child processes associated with immutable generations. It does
// not own generation lifecycle persistence; callers use runtimehost/generation
// for that state machine.
package supervisor

import (
	"context"
	"errors"
	"net/url"
	"time"
)

type Role string

const (
	RoleControl Role = "control"
	RoleEngine  Role = "engine"
)

type ProcessState string

const (
	ProcessStarting ProcessState = "starting"
	ProcessReady    ProcessState = "ready"
	ProcessStopping ProcessState = "stopping"
	ProcessExited   ProcessState = "exited"
	ProcessFailed   ProcessState = "failed"
)

var (
	ErrInvalidConfig       = errors.New("invalid runtime supervisor configuration")
	ErrInvalidProcess      = errors.New("invalid runtime process specification")
	ErrGenerationExists    = errors.New("runtime generation process already exists")
	ErrGenerationNotFound  = errors.New("runtime generation process not found")
	ErrGenerationLimit     = errors.New("runtime generation process limit reached")
	ErrCandidateNotReady   = errors.New("candidate generation is not ready")
	ErrControlIsActive     = errors.New("active control process cannot be stopped directly")
	ErrEngineStillDefault  = errors.New("default engine generation cannot be retired")
	ErrEngineNotDrained    = errors.New("engine still owns active recordings")
	ErrControlStillRunning = errors.New("engine control process must be stopped before retirement")
	ErrControlDrainPending = errors.New("control route switched; previous control is still draining")
	ErrSupervisorClosed    = errors.New("runtime supervisor is closed")
	ErrListenerAlreadySet  = errors.New("runtime supervisor listener is already set")
)

// ProcessSpec is trusted Runtime Host launch configuration sourced from an
// immutable installation. It is never accepted from an external HTTP request.
// Control processes must start passive; ControlLifecycle enables their
// background work only after the Runtime Host has switched routing. Endpoint
// is a private readiness endpoint interpreted only by Readiness.
type ProcessSpec struct {
	GenerationID string
	Role         Role
	Executable   string
	Args         []string
	Env          []string
	Dir          string
	Endpoint     string
}

// GenerationSpec stages the two independent processes of one application
// generation. ControlTarget must be a loopback HTTP endpoint owned by that
// Control process; the public listener remains owned by Runtime Host.
type GenerationSpec struct {
	ID            string
	Engine        ProcessSpec
	Control       ProcessSpec
	ControlTarget *url.URL
}

type Launcher interface {
	Start(context.Context, ProcessSpec) (Child, error)
}

// Child represents one independently supervised OS process. Done closes once
// the process exits; Err contains a private local exit error, never a public
// snapshot field.
type Child interface {
	Done() <-chan struct{}
	Err() error
	Stop(context.Context) error
	Kill() error
}

// Readiness must perform role-specific readiness checks. It is invoked after
// process start and before that role can be considered ready or routed to.
type Readiness interface {
	WaitReady(context.Context, ProcessSpec, Child) error
}

type ReadinessFunc func(context.Context, ProcessSpec, Child) error

func (f ReadinessFunc) WaitReady(ctx context.Context, spec ProcessSpec, child Child) error {
	return f(ctx, spec, child)
}

// EngineDrain atomically fences new recording admission and reports the
// current active recording inventory for a generation. BeginDrain must be
// idempotent. RetireEngine stops the process only after this proof reports 0.
type EngineDrain interface {
	BeginDrain(context.Context, string) error
	ActiveRecordings(context.Context, string) (int, error)
}

// ControlLifecycle coordinates active ownership outside this process manager.
// Candidate Control processes start passive. PrepareHandoff fences/drains old
// Control mutations; PrepareActivation then constructs the candidate's
// generation-owned services while it still cannot mutate or run background
// work. Durable ownership switches before routing and Activate enables the
// candidate after the route changes. Rollback restores a single active owner.
type ControlLifecycle interface {
	PrepareHandoff(context.Context, string, string) error
	PrepareActivation(context.Context, string) error
	Activate(context.Context, string) error
	Rollback(context.Context, string, string) error
}

// ProcessExitObserver runs after a managed child has exited. It receives only
// the trusted launch specification and a process-local error; callers must
// redact diagnostics before logging or exposing them. It is useful for
// reclaiming host-owned permits only after the corresponding process is dead.
type ProcessExitObserver func(ProcessSpec, error)

type Options struct {
	Launcher         Launcher
	Readiness        Readiness
	ControlLifecycle ControlLifecycle
	OnProcessExit    ProcessExitObserver
	ShutdownTimeout  time.Duration
	CleanupTimeout   time.Duration
	MaxGenerations   int
}

type ProcessSnapshot struct {
	State ProcessState `json:"state,omitempty"`
}

type GenerationSnapshot struct {
	ID            string          `json:"id"`
	Control       ProcessSnapshot `json:"control"`
	Engine        ProcessSnapshot `json:"engine"`
	ControlActive bool            `json:"control_active"`
}

// Snapshot deliberately omits process IDs, executable paths, addresses,
// environment values, and child diagnostics.
type Snapshot struct {
	Serving                 bool                 `json:"serving"`
	Closed                  bool                 `json:"closed"`
	ActiveControlGeneration string               `json:"active_control_generation,omitempty"`
	Generations             []GenerationSnapshot `json:"generations"`
}
