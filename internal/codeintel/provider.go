// Package codeintel provides managed, workspace-scoped code intelligence
// services. It is deliberately independent from user-configured MCP servers:
// callers own one provider for the application lifetime and close it on app
// shutdown.
package codeintel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gokin/internal/mcp"
	"gokin/internal/security"
)

const (
	defaultStartupTimeout  = 5 * time.Second
	defaultCallTimeout     = 15 * time.Second
	defaultShutdownTimeout = 2 * time.Second
	defaultMaxRequestBytes = 256 << 10
	defaultMaxOutputBytes  = 1 << 20
	defaultMaxStderrBytes  = 64 << 10
	maxConfiguredBytes     = 16 << 20

	// DiagnosticsSource is the stable source label exposed to completion gates.
	DiagnosticsSource = "gopls-mcp"
)

var (
	ErrClosed                = errors.New("gopls MCP provider is closed")
	ErrToolNotReadOnly       = errors.New("gopls MCP tool is not approved for read-only use")
	ErrCapabilityUnavailable = errors.New("gopls MCP capability is unavailable")
	ErrRequestTooLarge       = errors.New("gopls MCP request exceeds the configured limit")
	ErrResponseTooLarge      = errors.New("gopls MCP response exceeds the configured limit")
)

// ProcessSpec describes the fixed managed child process requested from a
// Dialer. Dir is always the canonical workspace root.
type ProcessSpec struct {
	Command         string
	Args            []string
	Dir             string
	Env             []string
	MaxMessageBytes int
	MaxStderrBytes  int
}

// Connection is the small MCP transport surface the provider manages. Close
// must honor ctx and terminate its underlying process/transport.
type Connection interface {
	Send(*mcp.JSONRPCMessage) error
	Receive() (*mcp.JSONRPCMessage, error)
	Close(context.Context) error
	Diagnostic() string
}

// Dialer starts one connection. It exists primarily for deterministic tests;
// production uses a workspace-scoped gopls subprocess.
type Dialer func(context.Context, ProcessSpec) (Connection, error)

// Options bounds every external interaction. Zero values select conservative
// defaults. Command/Args are intended for tests and controlled embedding; the
// production default is "gopls mcp".
type Options struct {
	Command         string
	Args            []string
	StartupTimeout  time.Duration
	CallTimeout     time.Duration
	ShutdownTimeout time.Duration
	MaxRequestBytes int
	MaxOutputBytes  int
	MaxStderrBytes  int
	Dialer          Dialer
}

// Capability is an immutable snapshot of one discovered gopls MCP tool.
// ReadOnly states whether CallReadOnly will permit it.
type Capability struct {
	Name        string
	Description string
	InputSchema *mcp.JSONSchema
	ReadOnly    bool
}

// DiagnosticsReport is the high-level completion-gate result. Content is the
// bounded textual MCP response; Clean is true only for gopls' explicit clean
// result, never merely because output was empty.
type DiagnosticsReport struct {
	Content string
	Clean   bool
	Source  string
}

// ReadOnlyProvider is the consumer-facing API used by semantic-tool/app
// adapters. Implementations are safe for concurrent callers but intentionally
// serialize gopls requests as foreground work.
type ReadOnlyProvider interface {
	Capabilities(context.Context) ([]Capability, error)
	CallReadOnly(context.Context, string, map[string]any) (*mcp.CallToolResult, error)
	Diagnose(context.Context, []string) (DiagnosticsReport, error)
	Close() error
}

// GoplsProvider lazily owns at most one gopls MCP process. Any transport-level
// failure permits exactly one controlled restart and retry of the read-only
// operation that observed it.
type GoplsProvider struct {
	workspace string
	opts      Options

	mu           sync.Mutex // serializes foreground operations and session state
	closed       atomic.Bool
	lifetime     context.Context
	cancel       context.CancelFunc
	session      *rpcSession
	capabilities []Capability
}

var _ ReadOnlyProvider = (*GoplsProvider)(nil)

// NewGoplsProvider validates the workspace but does not start gopls.
func NewGoplsProvider(workspace string, opts Options) (*GoplsProvider, error) {
	root, err := canonicalWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeOptions(opts)
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &GoplsProvider{
		workspace: root, opts: normalized, lifetime: lifetime, cancel: cancel,
	}, nil
}

// Capabilities lazily starts gopls and returns a deep-cloned discovery
// snapshot. A broken transport is restarted at most once.
func (p *GoplsProvider) Capabilities(ctx context.Context) ([]Capability, error) {
	ctx, cancelOperation := p.operationContext(ctx)
	defer cancelOperation()
	if p.closed.Load() {
		return nil, ErrClosed
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed.Load() {
		return nil, ErrClosed
	}
	if p.capabilities != nil {
		return cloneCapabilities(p.capabilities)
	}

	for attempt := 0; attempt < 2; attempt++ {
		if err := p.ensureSessionLocked(ctx); err != nil {
			if !p.canRetry(ctx, err, attempt) {
				return nil, err
			}
			p.stopLocked()
			continue
		}
		caps, err := p.discoverLocked(ctx)
		if err == nil {
			p.capabilities = caps
			return cloneCapabilities(caps)
		}
		if isRetryable(err) {
			p.stopLocked()
		}
		if !p.canRetry(ctx, err, attempt) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("discover gopls MCP capabilities: retry exhausted")
}

// CallReadOnly invokes only an explicitly allowlisted read-only gopls tool.
// Mutation-capable capabilities (notably go_rename_symbol) are rejected before
// lazy startup, even if the server advertises them.
func (p *GoplsProvider) CallReadOnly(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	ctx, cancelOperation := p.operationContext(ctx)
	defer cancelOperation()
	name = strings.TrimSpace(name)
	if !IsReadOnlyTool(name) {
		return nil, fmt.Errorf("%w: %q", ErrToolNotReadOnly, name)
	}
	clonedArgs, err := cloneArguments(args, p.opts.MaxRequestBytes)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed.Load() {
		return nil, ErrClosed
	}

	for attempt := 0; attempt < 2; attempt++ {
		if err := p.ensureSessionLocked(ctx); err != nil {
			if !p.canRetry(ctx, err, attempt) {
				return nil, err
			}
			p.stopLocked()
			continue
		}
		if p.capabilities == nil {
			caps, err := p.discoverLocked(ctx)
			if err != nil {
				if isRetryable(err) {
					p.stopLocked()
				}
				if !p.canRetry(ctx, err, attempt) {
					return nil, err
				}
				continue
			}
			p.capabilities = caps
		}
		if !hasCapability(p.capabilities, name) {
			return nil, fmt.Errorf("%w: %q", ErrCapabilityUnavailable, name)
		}

		attemptCtx, cancel := boundedContext(ctx, p.opts.CallTimeout)
		var result mcp.CallToolResult
		err := p.session.request(attemptCtx, mcp.MethodToolsCall, &mcp.CallToolParams{
			Name: name, Arguments: clonedArgs,
		}, &result, p.opts.MaxOutputBytes)
		cancel()
		if err == nil {
			return cloneCallResult(&result)
		}
		err = withSessionDiagnostic(err, p.session)
		if isRetryable(err) {
			p.stopLocked()
		}
		if !p.canRetry(ctx, err, attempt) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("call gopls MCP tool %q: retry exhausted", name)
}

// Diagnose runs gopls' workspace diagnostics tool for the supplied workspace
// files. Relative paths are resolved beneath the provider workspace.
func (p *GoplsProvider) Diagnose(ctx context.Context, files []string) (DiagnosticsReport, error) {
	report := DiagnosticsReport{Source: DiagnosticsSource}
	normalized, err := p.normalizeDiagnosticFiles(files)
	if err != nil {
		return report, err
	}
	result, err := p.CallReadOnly(ctx, "go_diagnostics", map[string]any{"files": normalized})
	if err != nil {
		return report, err
	}
	report.Content = callResultText(result)
	if result.IsError {
		return report, fmt.Errorf("gopls diagnostics failed: %s", nonEmpty(report.Content, "unknown MCP tool error"))
	}
	report.Clean = strings.TrimSpace(report.Content) == "No diagnostics."
	return report, nil
}

// Close idempotently shuts down the managed process. Graceful stdin closure is
// bounded; the production connection kills gopls when it does not exit before
// ShutdownTimeout.
func (p *GoplsProvider) Close() error {
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	// Cancel before waiting for the foreground mutex: an in-flight Receive
	// returns immediately, closes/kills its session, and releases ownership.
	p.cancel()
	p.mu.Lock()
	session := p.session
	p.session = nil
	p.capabilities = nil
	p.mu.Unlock()
	if session == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.opts.ShutdownTimeout)
	defer cancel()
	return session.close(ctx)
}

func (p *GoplsProvider) ensureSessionLocked(ctx context.Context) error {
	if p.session != nil {
		return nil
	}
	startCtx, cancel := boundedContext(ctx, p.opts.StartupTimeout)
	defer cancel()
	spec := ProcessSpec{
		Command:         p.opts.Command,
		Args:            append([]string(nil), p.opts.Args...),
		Dir:             p.workspace,
		Env:             managedGoEnv(),
		MaxMessageBytes: p.opts.MaxOutputBytes + (64 << 10),
		MaxStderrBytes:  p.opts.MaxStderrBytes,
	}
	conn, err := p.opts.Dialer(startCtx, spec)
	if err != nil {
		return retryable(fmt.Errorf("start managed gopls MCP: %w", err))
	}
	session := &rpcSession{conn: conn}
	var initialized mcp.InitializeResult
	err = session.request(startCtx, mcp.MethodInitialize, &mcp.InitializeParams{
		ProtocolVersion: mcp.ProtocolVersion,
		ClientInfo:      &mcp.ClientInfo{Name: "gokin-codeintel", Version: "1.0.0"},
		Capabilities:    map[string]any{},
	}, &initialized, p.opts.MaxOutputBytes)
	if err == nil {
		err = session.notify(startCtx, mcp.MethodInitialized, nil)
	}
	if err != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), p.opts.ShutdownTimeout)
		_ = session.close(closeCtx)
		closeCancel()
		return fmt.Errorf("initialize managed gopls MCP: %w%s", err, diagnosticSuffix(conn.Diagnostic()))
	}
	p.session = session
	return nil
}

func (p *GoplsProvider) discoverLocked(ctx context.Context) ([]Capability, error) {
	attemptCtx, cancel := boundedContext(ctx, p.opts.CallTimeout)
	defer cancel()
	var listed mcp.ListToolsResult
	if err := p.session.request(attemptCtx, mcp.MethodToolsList, map[string]any{}, &listed, p.opts.MaxOutputBytes); err != nil {
		return nil, fmt.Errorf("discover managed gopls MCP tools: %w", withSessionDiagnostic(err, p.session))
	}
	if len(listed.Tools) > 128 {
		return nil, retryable(fmt.Errorf("%w: discovered %d tools", ErrResponseTooLarge, len(listed.Tools)))
	}
	seen := make(map[string]struct{}, len(listed.Tools))
	caps := make([]Capability, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		if tool == nil || strings.TrimSpace(tool.Name) == "" {
			return nil, retryable(fmt.Errorf("invalid empty gopls MCP capability"))
		}
		if _, duplicate := seen[tool.Name]; duplicate {
			return nil, retryable(fmt.Errorf("duplicate gopls MCP capability %q", tool.Name))
		}
		seen[tool.Name] = struct{}{}
		caps = append(caps, Capability{
			Name: tool.Name, Description: tool.Description,
			InputSchema: tool.InputSchema, ReadOnly: IsReadOnlyTool(tool.Name),
		})
	}
	return cloneCapabilities(caps)
}

func (p *GoplsProvider) canRetry(parent context.Context, err error, attempt int) bool {
	return attempt == 0 && parent.Err() == nil && isRetryable(err)
}

func (p *GoplsProvider) stopLocked() {
	session := p.session
	p.session = nil
	p.capabilities = nil
	if session == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.opts.ShutdownTimeout)
	_ = session.close(ctx)
	cancel()
}

func (p *GoplsProvider) normalizeDiagnosticFiles(files []string) ([]string, error) {
	out := make([]string, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		if !filepath.IsAbs(file) {
			file = filepath.Join(p.workspace, file)
		}
		file = filepath.Clean(file)
		within, err := security.IsPathWithinAny(file, []string{p.workspace})
		if err != nil || !within {
			return nil, fmt.Errorf("diagnostics file %q is outside workspace", file)
		}
		if _, ok := seen[file]; ok {
			continue
		}
		seen[file] = struct{}{}
		out = append(out, file)
	}
	return out, nil
}

func (p *GoplsProvider) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(nonNilContext(parent))
	stop := context.AfterFunc(p.lifetime, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// IsReadOnlyTool is intentionally fail-closed. New gopls tools remain blocked
// until their behavior is reviewed.
func IsReadOnlyTool(name string) bool {
	switch name {
	case "go_workspace", "go_package_api", "go_diagnostics",
		"go_symbol_references", "go_search", "go_file_context", "go_vulncheck",
		"go_context", "go_file_diagnostics", "go_file_metadata", "go_references":
		return true
	default:
		return false
	}
}

func canonicalWorkspace(workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return "", fmt.Errorf("code-intelligence workspace is required")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve code-intelligence workspace: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("open code-intelligence workspace: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("code-intelligence workspace is not a directory: %s", abs)
	}
	return filepath.Clean(abs), nil
}

func normalizeOptions(opts Options) (Options, error) {
	if strings.TrimSpace(opts.Command) == "" {
		opts.Command = "gopls"
	}
	if len(opts.Args) == 0 {
		opts.Args = []string{"mcp"}
	} else {
		opts.Args = append([]string(nil), opts.Args...)
	}
	if opts.StartupTimeout <= 0 {
		opts.StartupTimeout = defaultStartupTimeout
	}
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = defaultCallTimeout
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = defaultShutdownTimeout
	}
	if opts.MaxRequestBytes <= 0 {
		opts.MaxRequestBytes = defaultMaxRequestBytes
	}
	if opts.MaxOutputBytes <= 0 {
		opts.MaxOutputBytes = defaultMaxOutputBytes
	}
	if opts.MaxStderrBytes <= 0 {
		opts.MaxStderrBytes = defaultMaxStderrBytes
	}
	for name, value := range map[string]int{
		"max request bytes": opts.MaxRequestBytes,
		"max output bytes":  opts.MaxOutputBytes,
		"max stderr bytes":  opts.MaxStderrBytes,
	} {
		if value > maxConfiguredBytes {
			return Options{}, fmt.Errorf("%s exceeds hard limit %d", name, maxConfiguredBytes)
		}
	}
	if opts.Dialer == nil {
		opts.Dialer = dialProcess
	}
	return opts, nil
}

func cloneArguments(args map[string]any, limit int) (map[string]any, error) {
	if args == nil {
		return nil, nil
	}
	data, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode gopls MCP arguments: %w", err)
	}
	if len(data) > limit {
		return nil, fmt.Errorf("%w: %d > %d bytes", ErrRequestTooLarge, len(data), limit)
	}
	var cloned map[string]any
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil, fmt.Errorf("clone gopls MCP arguments: %w", err)
	}
	return cloned, nil
}

func cloneCapabilities(in []Capability) ([]Capability, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("clone gopls MCP capabilities: %w", err)
	}
	var out []Capability
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("clone gopls MCP capabilities: %w", err)
	}
	return out, nil
}

func cloneCallResult(in *mcp.CallToolResult) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("clone gopls MCP result: %w", err)
	}
	var out mcp.CallToolResult
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("clone gopls MCP result: %w", err)
	}
	return &out, nil
}

func hasCapability(caps []Capability, name string) bool {
	for _, capability := range caps {
		if capability.Name == name && capability.ReadOnly {
			return true
		}
	}
	return false
}

func callResultText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		if block != nil && block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func boundedContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	parent = nonNilContext(parent)
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= timeout {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func diagnosticSuffix(diagnostic string) string {
	diagnostic = strings.TrimSpace(diagnostic)
	if diagnostic == "" {
		return ""
	}
	return "; stderr: " + diagnostic
}

func withSessionDiagnostic(err error, session *rpcSession) error {
	if err == nil || session == nil || session.conn == nil {
		return err
	}
	suffix := diagnosticSuffix(session.conn.Diagnostic())
	if suffix == "" {
		return err
	}
	return fmt.Errorf("%w%s", err, suffix)
}

type retryableFailure struct{ err error }

func (e *retryableFailure) Error() string { return e.err.Error() }
func (e *retryableFailure) Unwrap() error { return e.err }

func retryable(err error) error {
	if err == nil {
		return nil
	}
	var marked *retryableFailure
	if errors.As(err, &marked) {
		return err
	}
	return &retryableFailure{err: err}
}

func isRetryable(err error) bool {
	var marked *retryableFailure
	return errors.As(err, &marked)
}
