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
	DefaultMaxMemoryBytes   = 256 * 1024 * 1024
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
	// pythonExecPaths is the validated runtime-owned process-exec allowlist for
	// platforms whose Python launcher transfers control to another executable.
	// It is deliberately not configurable by users or repository files.
	pythonExecPaths []string
	// GitPath is runtime-owned discovery state, not a user/project setting.
	// Empty disables Git-native inventory and falls back to the bounded matcher.
	GitPath          string
	Backend          Backend
	CellTimeout      time.Duration
	MaxCodeBytes     int
	MaxResponseBytes int
	MaxMemoryBytes   int64
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
	if o.MaxMemoryBytes <= 0 {
		o.MaxMemoryBytes = DefaultMaxMemoryBytes
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
	Generation uint64 `json:"generation"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	Value      string `json:"value,omitempty"`
	// Operations is a compact runtime-generated count of context API calls made
	// by this cell. It deliberately contains neither cell code nor arguments, so
	// operational eval evidence can be journaled without leaking repository data
	// or duplicating model-visible output.
	Operations map[string]int `json:"operations,omitempty"`
	// FileIndexRefreshes is assigned by the Go parent from observed protocol
	// callbacks, rather than trusted from the Python response. Directory-scale
	// scans share a bounded per-scope inventory within one cell, but refresh
	// between cells or after crossing into the mutable orchestration plane.
	FileIndexRefreshes int `json:"file_index_refreshes,omitempty"`
	// Artifact is the primary overflow artifact retained for compatibility.
	// Artifacts preserves every independently bounded channel so a large value
	// cannot hide simultaneously large stdout or stderr.
	Artifact  *ArtifactRef            `json:"artifact,omitempty"`
	Artifacts map[string]*ArtifactRef `json:"artifacts,omitempty"`
	Truncated bool                    `json:"truncated,omitempty"`
	// KernelReset reports that the worker discarded this generation after a
	// fatal resource breach. The result remains useful, but no globals survive.
	KernelReset bool            `json:"kernel_reset,omitempty"`
	Error       *ExecutionError `json:"error,omitempty"`
}

// Stats is a bounded operational snapshot for diagnostics and model-visible
// recovery decisions. It contains no code, prompts, paths, or artifact data.
type Stats struct {
	Generation            uint64    `json:"generation"`
	Running               bool      `json:"running"`
	Restarts              uint64    `json:"restarts"`
	ManualResets          uint64    `json:"manual_resets"`
	Executions            uint64    `json:"executions"`
	TransportFailures     uint64    `json:"transport_failures"`
	Timeouts              uint64    `json:"timeouts"`
	ResourceLimitFailures uint64    `json:"resource_limit_failures"`
	LastError             string    `json:"last_error,omitempty"`
	LastFailureAt         time.Time `json:"last_failure_at,omitempty"`
}

func (r Result) OK() bool { return r.Error == nil }
