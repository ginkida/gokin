// Package repl implements the stateful, local computation plane used by the
// hybrid engine. The package deliberately does not depend on internal/tools:
// filesystem mutations and other capabilities are added later through the Go
// control plane rather than becoming ambient Python authority.
package repl

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	DefaultCellTimeout      = 30 * time.Second
	DefaultMaxCodeBytes     = 64 * 1024
	DefaultMaxResponseBytes = 1024 * 1024
	DefaultMaxCallbacks     = 16
)

// ErrUnavailable means the hybrid runtime cannot be enabled safely on this
// host. Auto mode treats this as a normal fallback to structured tools.
var ErrUnavailable = errors.New("stateful repl unavailable")

// Backend is the OS isolation mechanism protecting a Python worker.
type Backend string

const (
	BackendNone        Backend = "none"
	BackendSandboxExec Backend = "sandbox-exec"
	BackendBubblewrap  Backend = "bubblewrap"
	// BackendTest is intentionally unavailable through automatic detection. It
	// exists only for hermetic lifecycle tests which already run in a temporary
	// workspace and must not be exposed by production configuration.
	BackendTest Backend = "test-unrestricted"
)

// Options controls one session-scoped kernel manager.
type Options struct {
	WorkDir    string
	PythonPath string
	// GitPath is runtime-owned discovery state, not a user/project setting.
	// Empty disables context.git_status/git_diff without disabling the kernel.
	GitPath          string
	Backend          Backend
	CellTimeout      time.Duration
	MaxCodeBytes     int
	MaxResponseBytes int
	MaxCallbacks     int
}

func (o Options) withDefaults() Options {
	if o.CellTimeout <= 0 {
		o.CellTimeout = DefaultCellTimeout
	}
	if o.MaxCodeBytes <= 0 {
		o.MaxCodeBytes = DefaultMaxCodeBytes
	}
	if o.MaxResponseBytes <= 0 {
		o.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if o.MaxCallbacks <= 0 {
		o.MaxCallbacks = DefaultMaxCallbacks
	}
	return o
}

// Call is a typed worker-to-orchestrator request. The worker can only emit
// methods for which the App installs a handler; it does not gain a generic Go
// tool dispatcher or ambient capability.
type Call struct {
	ID     string         `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

type CallHandler func(context.Context, Call) (any, error)

// Availability is the result of a fail-closed runtime probe.
type Availability struct {
	Available  bool
	PythonPath string
	Backend    Backend
	Reason     string
}

func (a Availability) Error() error {
	if a.Available {
		return nil
	}
	reason := a.Reason
	if reason == "" {
		reason = "no supported secure runtime"
	}
	return fmt.Errorf("%w: %s", ErrUnavailable, reason)
}

// ArtifactRef identifies output retained inside the worker instead of copied
// through every model round.
type ArtifactRef struct {
	ID        string `json:"id"`
	Size      int    `json:"size"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ExecutionError is a sanitized Python exception. Traceback is bounded by the
// worker before it crosses the protocol boundary.
type ExecutionError struct {
	Type      string `json:"type"`
	Message   string `json:"message"`
	Traceback string `json:"traceback,omitempty"`
}

// Result is one completed cell evaluation.
type Result struct {
	Generation uint64          `json:"generation"`
	Stdout     string          `json:"stdout,omitempty"`
	Stderr     string          `json:"stderr,omitempty"`
	Value      string          `json:"value,omitempty"`
	Artifact   *ArtifactRef    `json:"artifact,omitempty"`
	Truncated  bool            `json:"truncated,omitempty"`
	Error      *ExecutionError `json:"error,omitempty"`
}

// Stats is a bounded operational snapshot for diagnostics and model-visible
// recovery decisions. It contains no code, prompts, paths, or artifact data.
type Stats struct {
	Generation        uint64    `json:"generation"`
	Running           bool      `json:"running"`
	Restarts          uint64    `json:"restarts"`
	ManualResets      uint64    `json:"manual_resets"`
	Executions        uint64    `json:"executions"`
	TransportFailures uint64    `json:"transport_failures"`
	Timeouts          uint64    `json:"timeouts"`
	LastError         string    `json:"last_error,omitempty"`
	LastFailureAt     time.Time `json:"last_failure_at,omitempty"`
}

func (r Result) OK() bool { return r.Error == nil }
