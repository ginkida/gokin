package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gokin/internal/logging"
	"gokin/internal/pluginpolicy"
)

const (
	// maxHookOutputBytes is a shared budget across stdout and stderr for one
	// hook invocation. Hook output is surfaced to the model and UI, so letting
	// an arbitrary project command grow a bytes.Buffer without limit can exhaust
	// the agent process before the hook timeout has a chance to fire.
	maxHookOutputBytes = 64 << 10

	// hookPipeWaitDelay bounds cmd.Wait when a hook exits but one of its
	// descendants keeps an inherited stdout/stderr descriptor open. It also
	// gives the small, bounded capture enough time to drain after the process
	// exits normally.
	hookPipeWaitDelay = 250 * time.Millisecond

	hookTerminationGracePeriod = 2 * time.Second
	hookFinalReapWait          = 500 * time.Millisecond
	hookOutputTruncationMarker = "...[hook output truncated after 65536 bytes]..."
)

// hookOutputCapture is a concurrency-safe, shared-budget capture for stdout
// and stderr. os/exec can copy both streams concurrently, so the budget and
// buffers must be guarded together.
type hookOutputCapture struct {
	mu        sync.Mutex
	remaining int
	stdout    bytes.Buffer
	stderr    bytes.Buffer
	truncated bool
}

type hookOutputWriter struct {
	capture *hookOutputCapture
	stderr  bool
}

func newHookOutputCapture(limit int) *hookOutputCapture {
	return &hookOutputCapture{remaining: limit}
}

func (w hookOutputWriter) Write(p []byte) (int, error) {
	originalLen := len(p)
	w.capture.mu.Lock()
	defer w.capture.mu.Unlock()

	if len(p) > w.capture.remaining {
		p = p[:w.capture.remaining]
		w.capture.truncated = true
	}
	if len(p) == 0 {
		if originalLen > 0 {
			w.capture.truncated = true
		}
		return originalLen, nil
	}

	target := &w.capture.stdout
	if w.stderr {
		target = &w.capture.stderr
	}
	_, _ = target.Write(p) // bytes.Buffer.Write never returns an error.
	w.capture.remaining -= len(p)

	// Report the entire input as consumed. The omitted suffix is intentional;
	// returning a short write would make io.Copy/os/exec treat truncation as an
	// execution failure and could stop draining the child's pipe.
	return originalLen, nil
}

func (c *hookOutputCapture) stdoutWriter() hookOutputWriter {
	return hookOutputWriter{capture: c}
}

func (c *hookOutputCapture) stderrWriter() hookOutputWriter {
	return hookOutputWriter{capture: c, stderr: true}
}

func (c *hookOutputCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	output := c.stdout.String()
	if c.stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += c.stderr.String()
	}
	if c.truncated {
		if output != "" && !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		output += hookOutputTruncationMarker
	}
	return output
}

// Result represents the result of running a hook.
type Result struct {
	Hook                                                           *Hook
	Output                                                         string
	Error                                                          error
	Elapsed                                                        time.Duration
	BlockReason, StopReason, AdditionalContext, PermissionDecision string
	UpdatedInput                                                   map[string]any
	SessionTitle, SystemMessage                                    string
	SuppressOriginalPrompt                                         bool
	InitialUserMessage                                             string
	ReloadSkills                                                   bool
	WatchPaths                                                     []string
	skipped                                                        bool
	asyncJob                                                       *asyncJob
}

// Handler is called when a hook produces output or errors.
type Handler func(hook *Hook, output string, err error)

// Manager manages and executes hooks.
type Manager struct {
	enabled bool
	hooks   []*Hook
	workDir string
	timeout time.Duration
	handler Handler

	mu sync.RWMutex

	claudeMu                                                                            sync.Mutex
	claudeConfigured, claudeTrusted, claudeDisabled                                     bool
	claudeProjectDir, claudeUserDir, claudeFingerprint                                  string
	claudeScope                                                                         *Scope
	claudeHTTPPolicy                                                                    httpHookPolicy // guarded by mu
	mcpHookInvoker                                                                      MCPHookInvoker // guarded by mu
	claudeError                                                                         error
	claudeRevision                                                                      uint64
	claudePluginDirs                                                                    []string
	claudeRepositoryDirs                                                                []string // claudeMu
	claudePluginAliases                                                                 pluginpolicy.PluginAliases
	claudeKeptPluginAliases                                                             map[string]string
	claudePinnedPluginFlags                                                             map[string]bool
	claudePinnedPluginConfigs                                                           map[string]json.RawMessage
	claudeRegistryPinned                                                                bool
	claudePinnedRegistry                                                                []byte
	claudePinnedKnownIDs                                                                map[string]bool
	claudePluginDiscovery                                                               bool                                 // guarded by claudeMu
	claudeSkipDependencyChecks                                                          bool                                 // isolated manifest validation only
	claudeSecretOptions                                                                 func(string) (map[string]any, error) // isolated validation override, guarded by claudeMu
	claudeDependencyWarnings                                                            []string                             // guarded by claudeMu
	claudePlugins                                                                       []ClaudePlugin                       // guarded by mu
	claudeExplicitPlugins                                                               bool
	claudePluginActive                                                                  bool
	claudeManagedDir                                                                    string
	claudeManagedPresent, claudeManagedOnly, claudeManagedDisabled, claudeManagedActive bool
}

// NewManager creates a new hooks manager.
func NewManager(enabled bool, workDir string) *Manager {
	return &Manager{
		enabled: enabled,
		hooks:   make([]*Hook, 0),
		workDir: workDir,
		timeout: 30 * time.Second,
	}
}

// SetEnabled enables or disables the hooks system.
func (m *Manager) SetEnabled(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enabled != enabled {
		m.enabled = enabled
		m.claudeRevision++
	}
}

// IsEnabled returns whether hooks are enabled.
func (m *Manager) IsEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.unmanagedHooksEnabledLocked() || m.claudeManagedActive || m.claudePluginActive
}

// SetTimeout sets the execution timeout for hooks.
func (m *Manager) SetTimeout(timeout time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.timeout = timeout
}

// SetHandler sets the output handler.
func (m *Manager) SetHandler(handler Handler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handler = handler
}

// AddHook adds a hook to the manager.
func (m *Manager) AddHook(hook *Hook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hooks = append(m.hooks, hook)
}

// AddHooks adds multiple hooks to the manager.
func (m *Manager) AddHooks(hooks []*Hook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hooks = append(m.hooks, hooks...)
}

// ClearHooks removes all hooks.
func (m *Manager) ClearHooks() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hooks = make([]*Hook, 0)
}

// GetHooks returns a copy of all hooks.
func (m *Manager) GetHooks() []*Hook {
	_ = m.RefreshClaudeSettings()
	m.mu.RLock()
	result := make([]*Hook, len(m.hooks))
	copy(result, m.hooks)
	scope := m.claudeScope
	m.mu.RUnlock()
	result = append(result, scope.Hooks()...)
	return result
}

// CachedClaudeHooks is a cheap discovery probe at application boundaries.
// Execution and a live watcher still refresh policy before using a handler.
func (m *Manager) CachedClaudeHooks(kind Type) []*Hook {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	scope := m.claudeScope
	m.mu.RUnlock()
	return scope.ConfiguredHooks(kind)
}

// Run executes all matching hooks for the given type and context.
// It supports hook chaining (DependsOn), conditions (previousSuccess),
// output capture (CapturedOutput), and FailOnError cancellation.
//
// The manager timeout is applied independently to each hook invocation. The
// supplied context spans the complete chain, so callers that need a total
// chain deadline should put that deadline on ctx.
func (m *Manager) Run(ctx context.Context, hookType Type, hctx *Context) []Result {
	return m.run(ctx, hookType, hctx, true)
}

func (m *Manager) run(ctx context.Context, hookType Type, hctx *Context, includeClaude bool) []Result {
	if err := m.CheckReady(); err != nil {
		return claudeSettingsFailure(hookType, err)
	}
	if err := m.requiredSkillScopeError(ctx); err != nil {
		return skillScopeFailure(hookType, err)
	}
	m.mu.RLock()
	hooks := m.hooks
	unmanagedEnabled := m.unmanagedHooksEnabledLocked()
	if !unmanagedEnabled {
		hooks = nil
	}
	timeout := m.timeout
	handler := m.handler
	settings := m.claudeScope
	m.mu.RUnlock()

	if hctx.WorkDir == "" {
		hctx.WorkDir = m.workDir
	}

	var results []Result
	completedHooks := make(map[string]bool)

	for _, hook := range hooks {
		if !hook.Matches(hookType, hctx.ToolName) {
			continue
		}

		if !hook.ShouldRun(hctx, completedHooks) {
			continue
		}

		result := m.executeHook(ctx, hook, hctx, timeout)
		results = append(results, result)

		// Runtime visibility backstop (deferred round-14 #6): this file used
		// to have ZERO logging and the executor discards Run's results, so a
		// user's configured hook could fail forever — typo'd command, missing
		// binary — with no signal ANYWHERE. Warn on failure (works headless
		// too); Debug on success for diagnosability. The UI toast layer sits
		// on top via the handler below.
		if result.Error != nil {
			logging.Warn("hook failed",
				"type", string(hook.Type), "hook", hook.DisplayName(),
				"error", result.Error, "elapsed", result.Elapsed)
		} else {
			logging.Debug("hook executed",
				"type", string(hook.Type), "hook", hook.DisplayName(), "elapsed", result.Elapsed)
		}

		// Capture output into context for subsequent hooks
		hctx.CapturedOutput = result.Output

		// Track completed hooks for chaining
		if hook.Name != "" {
			completedHooks[hook.Name] = true
		}

		if handler != nil {
			handler(hook, result.Output, result.Error)
		}

		// If FailOnError is set and the hook failed, stop executing further hooks
		if hook.FailOnError && result.Error != nil {
			break
		}
	}

	if includeClaude {
		results = append(results, settings.run(ctx, m, hookType, hctx, handler)...)
		if scope := ScopeFromContext(ctx); scope != nil {
			results = append(results, scope.run(ctx, m, hookType, hctx, handler)...)
		}
	}
	return results
}

// RunPreTool runs pre-tool hooks.
func (m *Manager) RunPreTool(ctx context.Context, toolName string, args map[string]any) []Result {
	return m.RunPreToolInDir(ctx, m.workDir, toolName, args)
}

// RunPreToolInDir runs pre-tool hooks in the effective workspace of a caller
// such as an isolated sub-agent, rather than always using the foreground root.
func (m *Manager) RunPreToolInDir(ctx context.Context, workDir, toolName string, args map[string]any) []Result {
	hctx := NewContext(toolName, args, workDir)
	return m.Run(ctx, PreTool, hctx)
}

// RunScoped runs only conversation registrations. Agent callers use this
// before permission checks for updatedInput, while preserving the native
// configured hooks' established permission-before-hook order.
func (m *Manager) RunScoped(ctx context.Context, kind Type, hctx *Context) []Result {
	if err := m.CheckReady(); err != nil {
		return claudeSettingsFailure(kind, err)
	}
	if err := m.requiredSkillScopeError(ctx); err != nil {
		return skillScopeFailure(kind, err)
	}
	m.mu.RLock()
	handler, settings := m.handler, m.claudeScope
	m.mu.RUnlock()
	results := settings.run(ctx, m, kind, hctx, handler)
	results = append(results, ScopeFromContext(ctx).run(ctx, m, kind, hctx, handler)...)
	return results
}

// A policy change after the model responds must not suppress required skill
// checks at Stop or another hook boundary. Executors also guard before requests.
func (m *Manager) requiredSkillScopeError(ctx context.Context) error {
	scope := ScopeFromContext(ctx)
	if err := scope.RestoreError(); err != nil {
		return fmt.Errorf("cannot restore required skill hooks: %w", err)
	}
	if err := scope.PolicyError(m); err != nil {
		return err
	}
	return nil
}

func skillScopeFailure(kind Type, err error) []Result {
	return []Result{{Hook: &Hook{Name: "required skill hooks", Source: "session", Type: kind}, Error: err, StopReason: err.Error()}}
}

func claudeSettingsFailure(kind Type, err error) []Result {
	return []Result{{Hook: &Hook{Name: "required Claude settings hooks", Source: "claude:settings", Type: kind}, Error: err, StopReason: err.Error()}}
}

// RunPostTool runs post-tool hooks.
func (m *Manager) RunPostTool(ctx context.Context, toolName string, args map[string]any, result string) []Result {
	return m.RunPostToolInDir(ctx, m.workDir, toolName, args, result)
}

func (m *Manager) RunPostToolInDir(ctx context.Context, workDir, toolName string, args map[string]any, result string) []Result {
	hctx := NewContext(toolName, args, workDir)
	hctx.SetResult(result)
	return m.Run(ctx, PostTool, hctx)
}

// RunOnError runs on-error hooks.
func (m *Manager) RunOnError(ctx context.Context, toolName string, args map[string]any, err string) []Result {
	return m.RunOnErrorInDir(ctx, m.workDir, toolName, args, err)
}

func (m *Manager) RunOnErrorInDir(ctx context.Context, workDir, toolName string, args map[string]any, err string) []Result {
	hctx := NewContext(toolName, args, workDir)
	hctx.SetError(err)
	return m.Run(ctx, OnError, hctx)
}

func (m *Manager) RunUserPromptSubmit(ctx context.Context, workDir, prompt string) []Result {
	hctx := NewContext("", nil, workDir)
	hctx.Prompt = prompt
	return m.RunScoped(ctx, UserPromptSubmit, hctx)
}

func (m *Manager) RunSubagentStart(ctx context.Context, workDir string) []Result {
	return m.RunScoped(ctx, SubagentStart, NewContext("", nil, workDir))
}

func (m *Manager) RunSessionStart(ctx context.Context, workDir, source string) []Result {
	hctx := NewContext(source, nil, workDir)
	hctx.Source = source
	return m.RunScoped(ctx, SessionStart, hctx)
}

func (m *Manager) RunFileChanged(ctx context.Context, workDir, path, event string) []Result {
	hctx := NewContext(filepath.Base(path), nil, workDir)
	hctx.FilePath, hctx.FileEvent = path, event
	return m.RunScoped(ctx, FileChanged, hctx)
}

func (m *Manager) RunCwdChanged(ctx context.Context, oldCwd, newCwd string) []Result {
	hctx := NewContext("", nil, newCwd)
	hctx.OldCwd, hctx.NewCwd = oldCwd, newCwd
	return m.RunScoped(ctx, CwdChanged, hctx)
}

func (m *Manager) RunDirectoryAdded(ctx context.Context, workDir, directory, source string) []Result {
	hctx := NewContext(source, nil, workDir)
	hctx.Directory, hctx.Source = directory, source
	return m.RunScoped(ctx, DirectoryAdded, hctx)
}

// RunOnStart runs on-start hooks.
func (m *Manager) RunOnStart(ctx context.Context) []Result {
	hctx := &Context{WorkDir: m.workDir, Extra: make(map[string]string)}
	return m.Run(ctx, OnStart, hctx)
}

// RunOnExit runs on-exit hooks.
func (m *Manager) RunOnExit(ctx context.Context) []Result {
	hctx := &Context{WorkDir: m.workDir, Extra: make(map[string]string)}
	return m.Run(ctx, OnExit, hctx)
}

// killHookProcess attempts graceful shutdown with SIGTERM, then SIGKILL after grace period.
// The done channel should signal when the process has exited (from the caller's cmd.Wait goroutine).
// Signals target the WHOLE process group (see setProcAttrForGroup/killProcessGroup), not just
// the immediate `sh -c` process — a hook command that backgrounds a child (`sleep 9999 &`)
// would otherwise keep the stdout/stderr pipe open forever, hanging cmd.Wait() (and the whole
// synchronous PreToolUse/turn) well past this timeout.
func killHookProcess(cmd *exec.Cmd, gracePeriod time.Duration, done <-chan struct{}) {
	if cmd.Process == nil {
		return
	}

	// Try SIGTERM first for graceful shutdown
	if err := killProcessGroup(cmd, syscall.SIGTERM); err != nil {
		// SIGTERM failed, try SIGKILL immediately
		killProcessGroup(cmd, syscall.SIGKILL)
		return
	}

	// Wait for process exit or grace period expiry
	graceTimer := time.NewTimer(gracePeriod)
	defer graceTimer.Stop()
	select {
	case <-done:
		// cmd.Wait only proves that the shell leader exited. A descendant may
		// have ignored SIGTERM (and may already have closed its inherited pipes),
		// so sweep the process group before returning.
		_ = killProcessGroup(cmd, syscall.SIGKILL)
	case <-graceTimer.C:
		_ = killProcessGroup(cmd, syscall.SIGKILL)
	}
}

// executeHook executes a single hook.
func (m *Manager) executeHook(ctx context.Context, hook *Hook, hctx *Context, timeout time.Duration) Result {
	result := m.executeHandler(ctx, hook, hctx, timeout)
	if result.Error != nil && hook.Claude != nil && hook.Claude.OnFailure == "block" && !hook.Claude.Async && !hook.Claude.AsyncRewake {
		var exitErr *exec.ExitError
		if errors.As(result.Error, &exitErr) && exitErr.ExitCode() == 2 {
			return result
		}
		message := "Hook failed; blocking because onFailure is block: " + result.Error.Error()
		if hook.Type == PreTool || hook.Type == UserPromptSubmit {
			result.BlockReason = message
		}
		if hook.Type == PostTool || hook.Type == OnError {
			result.AdditionalContext = message
		}
	}
	return result
}

func (m *Manager) executeHandler(ctx context.Context, hook *Hook, hctx *Context, timeout time.Duration) Result {
	if hook.Claude != nil && (hook.Claude.Async || hook.Claude.AsyncRewake) && asyncWorkerFromContext(ctx) == nil {
		return m.launchAsyncHook(ctx, hook, hctx)
	}
	if hook.Claude != nil && hook.Claude.Handler == "agent" {
		return m.executeAgentHook(ctx, hook, hctx, timeout)
	}
	if hook.Claude != nil && hook.Claude.Handler == "mcp_tool" {
		return m.executeMCPHook(ctx, hook, hctx, timeout)
	}
	if hook.Claude != nil && hook.Claude.Handler == "prompt" {
		return m.executePromptHook(ctx, hook, hctx, timeout)
	}
	if hook.Claude != nil && hook.Claude.Handler == "http" {
		return m.executeHTTPHook(ctx, hook, hctx, timeout)
	}
	start := time.Now()

	// Expand variables in command
	command := hook.Command
	if hook.Claude == nil {
		command = hctx.ExpandCommand(command)
	}

	// Create context with timeout that respects parent cancellation
	execCtx, cancel := context.WithCancel(ctx)
	if timeout > 0 {
		cancel()
		execCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	if err := execCtx.Err(); err != nil {
		return Result{
			Hook:    hook,
			Error:   fmt.Errorf("hook '%s' cancelled: %w", hook.Name, err),
			Elapsed: time.Since(start),
		}
	}

	// Execute command. Cancellation is managed below instead of by
	// exec.CommandContext so SIGTERM reaches the whole process group before the
	// SIGKILL fallback. WaitDelay is a second line of defense around os/exec's
	// internal stdout/stderr copy goroutines: after the shell exits, a detached
	// descendant may keep an inherited pipe open indefinitely. WaitDelay closes
	// those pipes and makes cmd.Wait return within a bounded interval.
	cmd := exec.Command("sh", "-c", command)
	if hook.Claude != nil {
		input, info, err := claudeInput(ctx, hook, hctx)
		if worker := asyncWorkerFromContext(ctx); worker != nil {
			input, info, err = worker.input, worker.info, nil
		}
		if err != nil {
			return Result{Hook: hook, Error: err}
		}
		if hook.Claude.Args != nil {
			args := append([]string(nil), hook.Claude.Args...)
			for i := range args {
				args[i] = pluginHookVariables(args[i], info, hook.claudePlugin)
			}
			cmd = exec.Command(pluginHookVariables(command, info, hook.claudePlugin), args...)
		}
		cmd.Stdin = bytes.NewReader(input)
		for _, variable := range os.Environ() {
			if !strings.HasPrefix(variable, "CLAUDE_PROJECT_DIR=") && !strings.HasPrefix(variable, "CLAUDE_EFFORT=") && !strings.HasPrefix(variable, "CLAUDE_PLUGIN_ROOT=") && !strings.HasPrefix(variable, "CLAUDE_PLUGIN_DATA=") && !strings.HasPrefix(variable, "CLAUDE_PLUGIN_OPTION_") {
				cmd.Env = append(cmd.Env, variable)
			}
		}
		cmd.Env = append(cmd.Env, "CLAUDE_PROJECT_DIR="+info.ProjectDir)
		if (hook.Type == SessionStart || hook.Type == FileChanged || hook.Type == CwdChanged) && info.EnvFile != "" {
			cmd.Env = append(cmd.Env, "CLAUDE_ENV_FILE="+info.EnvFile)
		}
		if plugin := hook.claudePlugin; plugin != nil {
			if err := os.MkdirAll(plugin.Data, 0o700); err != nil {
				return Result{Hook: hook, Error: fmt.Errorf("create plugin data directory: %w", err), StopReason: "Plugin data directory is unavailable"}
			}
			cmd.Env = append(cmd.Env, "CLAUDE_PLUGIN_ROOT="+filepath.ToSlash(plugin.Root), "CLAUDE_PLUGIN_DATA="+filepath.ToSlash(plugin.Data))
			for key, value := range plugin.Options {
				cmd.Env = append(cmd.Env, "CLAUDE_PLUGIN_OPTION_"+strings.ToUpper(key)+"="+value)
			}
		}
		if info.Effort != "" {
			cmd.Env = append(cmd.Env, "CLAUDE_EFFORT="+info.Effort)
		}
	}
	cmd.WaitDelay = hookPipeWaitDelay
	cmd.Dir = hctx.WorkDir
	// Own process group so killHookProcess can terminate any backgrounded/
	// detached children the hook spawns, not just this immediate shell.
	setProcAttrForGroup(cmd)

	outputCapture := newHookOutputCapture(maxHookOutputBytes)
	cmd.Stdout = outputCapture.stdoutWriter()
	cmd.Stderr = outputCapture.stderrWriter()

	// Start command asynchronously for better cancellation handling
	if err := cmd.Start(); err != nil {
		return Result{
			Hook:    hook,
			Error:   fmt.Errorf("failed to start hook '%s': %w", hook.Name, err),
			Elapsed: time.Since(start),
		}
	}
	if worker := asyncWorkerFromContext(ctx); worker != nil {
		worker.job.started <- nil
	}

	// Keep Wait off the caller goroutine so parent cancellation can initiate
	// process-group teardown. The buffered result lets a very late/unreapable
	// process finish without blocking its goroutine on a caller that has already
	// returned after the final bounded wait below.
	waitResult := make(chan error, 1)
	cmdDone := make(chan struct{})
	go func() {
		defer close(cmdDone)
		defer func() {
			if r := recover(); r != nil {
				waitResult <- fmt.Errorf("hook '%s' wait panicked: %v", hook.Name, r)
			}
		}()
		waitResult <- cmd.Wait()
	}()

	cancelled := false
	waitCompleted := false
	var finalErr error

	select {
	case finalErr = <-waitResult:
		waitCompleted = true
		// Command completed normally
	case <-execCtx.Done():
		// Context cancelled or timeout - kill process with graceful shutdown
		cancelled = true
		killHookProcess(cmd, hookTerminationGracePeriod, cmdDone)

		// WaitDelay should make cmd.Wait complete after SIGKILL even if a
		// descendant kept a pipe open. Keep one final outer bound for unusual OS
		// process states where reaping itself does not return promptly.
		finalTimer := time.NewTimer(hookFinalReapWait)
		select {
		case finalErr = <-waitResult:
			waitCompleted = true
			finalTimer.Stop()
		case <-finalTimer.C:
			_ = killProcessGroup(cmd, syscall.SIGKILL)
		}
	}

	elapsed := time.Since(start)
	output := outputCapture.String()

	// Handle cancellation case
	if cancelled {
		cancelErr := execCtx.Err()
		if !waitCompleted {
			cancelErr = fmt.Errorf("%w (process cleanup exceeded %s)", cancelErr, hookFinalReapWait)
		}
		return Result{
			Hook:    hook,
			Output:  output,
			Error:   fmt.Errorf("hook '%s' cancelled: %w", hook.Name, cancelErr),
			Elapsed: elapsed,
		}
	}

	// ErrWaitDelay means the shell exited but a descendant kept an inherited
	// output pipe open past our drain window. Terminate the entire process group
	// even though the shell leader itself has already been reaped.
	if errors.Is(finalErr, exec.ErrWaitDelay) {
		killHookProcess(cmd, hookTerminationGracePeriod, cmdDone)
	}

	if finalErr != nil {
		finalErr = fmt.Errorf("hook '%s' failed: %w (output: %s)", hook.Name, finalErr, output)
	}

	result := Result{
		Hook:    hook,
		Output:  output,
		Error:   finalErr,
		Elapsed: elapsed,
	}
	if hook.Claude != nil {
		outputCapture.mu.Lock()
		stdout, stderr := outputCapture.stdout.String(), outputCapture.stderr.String()
		outputCapture.mu.Unlock()
		exitCode := 0
		var exitErr *exec.ExitError
		if errors.As(finalErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		if worker := asyncWorkerFromContext(ctx); worker != nil {
			worker.job.stdout, worker.job.stderr, worker.job.exitCode = stdout, stderr, exitCode
			return result
		}
		result = interpretClaudeResult(result, stdout, stderr, exitCode)
		if hook.Claude.OnFailure != "" && exitCode != 2 && strings.HasPrefix(strings.TrimSpace(stdout), "{") && !validHTTPHookOutput([]byte(strings.TrimSpace(stdout))) {
			result = Result{Hook: hook, Elapsed: elapsed, Error: fmt.Errorf("invalid Claude hook JSON output")}
		}
	}
	return result
}

// HasHooksFor checks if there are any hooks for the given type and tool.
func (m *Manager) HasHooksFor(hookType Type, toolName string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.enabled {
		return false
	}

	for _, hook := range m.hooks {
		if hook.Matches(hookType, toolName) {
			return true
		}
	}
	return false
}

// Blocked returns the first result whose hook demanded cancellation
// (FailOnError) and failed. Callers enforce the actual blocking: the
// executor refuses the tool call and returns the hook's output to the
// model as the reason.
func Blocked(results []Result) (Result, bool) {
	for _, r := range results {
		if r.Hook != nil && r.Hook.Claude != nil && (r.Hook.Claude.Async || r.Hook.Claude.AsyncRewake) {
			continue
		}
		if r.BlockReason != "" {
			return r, true
		}
		if r.Hook != nil && r.Hook.FailOnError && r.Error != nil {
			return r, true
		}
	}
	return Result{}, false
}

// RunStop runs end-of-turn hooks. The final response is exposed to hook
// commands as ${RESULT}.
func (m *Manager) RunStop(ctx context.Context, finalResponse string) []Result {
	return m.RunStopInDir(ctx, m.workDir, finalResponse)
}

func (m *Manager) RunStopInDir(ctx context.Context, workDir, finalResponse string) []Result {
	hctx := &Context{WorkDir: workDir, Extra: make(map[string]string)}
	hctx.SetResult(finalResponse)
	return m.Run(ctx, Stop, hctx)
}
