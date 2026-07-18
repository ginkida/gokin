package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gokin/internal/logging"
	"gokin/internal/security"
	"gokin/internal/tasks"

	"google.golang.org/genai"
)

// SafeEnvVars is the whitelist of environment variables passed to bash commands.
// This prevents leaking sensitive environment variables like API keys.
var SafeEnvVars = []string{
	"PATH",
	"HOME",
	"USER",
	"SHELL",
	"TERM",
	"LANG",
	"LC_ALL",
	"LC_CTYPE",
	"TMPDIR",
	"TMP",
	"TEMP",
	"EDITOR",
	"VISUAL",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_CACHE_HOME",
	"XDG_RUNTIME_DIR",
	// Go-specific
	"GOPATH",
	"GOROOT",
	"GOPROXY",
	"GOPRIVATE",
	"GOFLAGS",
	// Node/npm
	"NODE_PATH",
	"NPM_CONFIG_PREFIX",
	// Python
	"PYTHONPATH",
	"VIRTUAL_ENV",
	// Git
	"GIT_AUTHOR_NAME",
	"GIT_AUTHOR_EMAIL",
	"GIT_COMMITTER_NAME",
	"GIT_COMMITTER_EMAIL",
}

const (
	// DefaultBashTimeout is the default timeout for bash commands
	DefaultBashTimeout = 30 * time.Second
	// ProgressInterval is the interval for sending progress updates during long-running commands
	ProgressInterval = 5 * time.Second
	// StreamingFlushInterval is the interval for flushing partial output during foreground execution
	StreamingFlushInterval = 100 * time.Millisecond
)

// dangerousEnvVars is a blocklist of environment variables that can be used
// for code injection or privilege escalation.
var dangerousEnvVars = map[string]bool{
	"LD_PRELOAD":            true,
	"LD_LIBRARY_PATH":       true,
	"DYLD_INSERT_LIBRARIES": true,
	"DYLD_LIBRARY_PATH":     true,
	"BASH_ENV":              true,
	"ENV":                   true,
	"PROMPT_COMMAND":        true,
	"IFS":                   true,
	"CDPATH":                true,
	"SHELLOPTS":             true,
	"BASHOPTS":              true,
	"BASH_FUNC_":            true,
	"PS4":                   true,
}

// BashSession maintains persistent state across bash command invocations.
// It tracks the working directory and environment variables so that
// sequential commands behave as if they run in the same shell session.
type BashSession struct {
	workDir string            // persistent working directory
	env     map[string]string // environment variables set during session
	mu      sync.Mutex        // for thread safety
}

// NewBashSession creates a new BashSession with the given initial working directory.
func NewBashSession(workDir string) *BashSession {
	return &BashSession{
		workDir: workDir,
		env:     make(map[string]string),
	}
}

// WorkDir returns the current working directory of the session.
func (s *BashSession) WorkDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workDir
}

// SetWorkDir updates the working directory of the session.
func (s *BashSession) SetWorkDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workDir = dir
}

// SetEnv sets an environment variable in the session.
// Returns an error if the variable is in the dangerous blocklist.
func (s *BashSession) SetEnv(key, value string) error {
	// Check against blocklist (exact match and prefix match for BASH_FUNC_)
	upperKey := strings.ToUpper(key)
	if dangerousEnvVars[upperKey] {
		return fmt.Errorf("environment variable %q is blocked for security reasons", key)
	}
	for blocked := range dangerousEnvVars {
		if strings.HasPrefix(upperKey, blocked) {
			return fmt.Errorf("environment variable %q is blocked for security reasons", key)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.env[key] = value
	return nil
}

// Env returns a copy of the session environment variables.
func (s *BashSession) Env() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make(map[string]string, len(s.env))
	for k, v := range s.env {
		cp[k] = v
	}
	return cp
}

// BashTool executes bash commands.
type BashTool struct {
	workDir                   string
	session                   *BashSession
	taskManager               *tasks.Manager
	timeout                   time.Duration // Explicit timeout for commands
	sandboxEnabled            bool          // Enable sandboxing for bash commands
	unrestrictedMode          bool          // Skip command validation when both sandbox and permissions are off
	workspaceRoot             string
	workspaceBoundaryEnabled  bool
	managedWorkspaceApplyBack bool
	backgroundAllowed         bool

	// executionGate serializes the persistent shell session while still allowing
	// a queued invocation to honor context cancellation. Each invocation runs on
	// an immutable policy/session snapshot, so policy toggles do not have to hold
	// a write lock against a long-running command (which can deadlock with UI
	// progress callbacks) and cannot change the mode of an in-flight command.
	executionOnce sync.Once
	executionGate chan struct{}

	// policyMu protects the live policy used for future invocations. The
	// monotonically increasing revision prevents an old execution from committing
	// its resulting cwd after a concurrent policy/workspace change.
	policyMu       sync.RWMutex
	policyRevision uint64
}

// NewBashTool creates a new BashTool instance.
func NewBashTool(workDir string) *BashTool {
	return &BashTool{
		workDir:           workDir,
		session:           NewBashSession(workDir),
		timeout:           DefaultBashTimeout, // Set default timeout
		sandboxEnabled:    false,              // Sandbox disabled by default (requires root)
		backgroundAllowed: true,
		policyRevision:    1,
	}
}

// buildSafeEnv creates a sanitized environment for command execution.
// Only whitelisted environment variables are passed through.
func buildSafeEnv() []string {
	env := make([]string, 0, len(SafeEnvVars))
	for _, key := range SafeEnvVars {
		if val := os.Getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}
	// Always set a safe PATH if not already set
	hasPath := false
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			hasPath = true
			break
		}
	}
	if !hasPath {
		env = append(env, "PATH=/usr/local/bin:/usr/bin:/bin")
	}
	// Set TERM for proper terminal handling
	hasTerm := false
	for _, e := range env {
		if strings.HasPrefix(e, "TERM=") {
			hasTerm = true
			break
		}
	}
	if !hasTerm {
		env = append(env, "TERM=xterm-256color")
	}
	return env
}

// SetTimeout sets the timeout for bash commands.
func (t *BashTool) SetTimeout(timeout time.Duration) {
	t.policyMu.Lock()
	defer t.policyMu.Unlock()
	if t.timeout != timeout {
		t.policyRevision++
	}
	t.timeout = timeout
}

// SetTaskManager sets the task manager for background execution.
func (t *BashTool) SetTaskManager(manager *tasks.Manager) {
	t.policyMu.Lock()
	defer t.policyMu.Unlock()
	if t.taskManager != manager {
		t.policyRevision++
	}
	t.taskManager = manager
}

// SetBackgroundAllowed controls whether this tool may detach commands from the
// owning turn. Headless one-shot execution disables it so a successful final
// result cannot race an unfinished command that is cancelled at process exit.
func (t *BashTool) SetBackgroundAllowed(allowed bool) {
	t.policyMu.Lock()
	defer t.policyMu.Unlock()
	if t.backgroundAllowed != allowed {
		t.policyRevision++
	}
	t.backgroundAllowed = allowed
}

// SetSandboxEnabled enables or disables sandbox mode.
// When enabled, commands run in a Linux namespace sandbox (requires root).
// When disabled, commands run directly without isolation.
func (t *BashTool) SetSandboxEnabled(enabled bool) {
	t.policyMu.Lock()
	defer t.policyMu.Unlock()
	if t.sandboxEnabled != enabled {
		t.policyRevision++
	}
	t.sandboxEnabled = enabled
}

// SetUnrestrictedMode enables or disables unrestricted mode.
// When enabled (both sandbox and permissions are off), command validation is skipped.
func (t *BashTool) SetUnrestrictedMode(enabled bool) {
	t.policyMu.Lock()
	defer t.policyMu.Unlock()
	if t.unrestrictedMode != enabled {
		t.policyRevision++
	}
	t.unrestrictedMode = enabled
}

// SetWorkspaceBoundary constrains the persistent shell session to stay rooted at
// the provided workspace. It does not fully sandbox commands, but prevents the
// session from drifting outside the repo across turns.
func (t *BashTool) SetWorkspaceBoundary(root string) {
	t.policyMu.Lock()
	defer t.policyMu.Unlock()
	t.setWorkspaceBoundaryLocked(root)
}

// setWorkspaceBoundaryLocked updates the boundary while policyMu is held.
func (t *BashTool) setWorkspaceBoundaryLocked(root string) {
	root = strings.TrimSpace(root)
	if root == "" {
		if t.workspaceRoot != "" || t.workspaceBoundaryEnabled {
			t.policyRevision++
		}
		t.workspaceRoot = ""
		t.workspaceBoundaryEnabled = false
		return
	}

	cleanRoot := filepath.Clean(root)
	if t.workspaceRoot != cleanRoot || !t.workspaceBoundaryEnabled {
		t.policyRevision++
	}
	t.workspaceRoot = cleanRoot
	t.workspaceBoundaryEnabled = true
	if !t.isWithinWorkspace(t.session.WorkDir()) {
		t.session.SetWorkDir(cleanRoot)
	}
}

// EnableManagedWorkspaceApplyBackMode enables stricter containment used for
// isolated apply-back workspaces. Background tasks are disabled and commands
// that mutate git history are blocked because they cannot be replayed safely.
func (t *BashTool) EnableManagedWorkspaceApplyBackMode(root string) {
	t.policyMu.Lock()
	defer t.policyMu.Unlock()
	if !t.managedWorkspaceApplyBack {
		t.policyRevision++
	}
	t.managedWorkspaceApplyBack = true
	t.setWorkspaceBoundaryLocked(root)
}

// ManagedWorkspaceApplyBackModeEnabled reports whether stricter apply-back
// containment is enabled.
func (t *BashTool) ManagedWorkspaceApplyBackModeEnabled() bool {
	t.policyMu.RLock()
	defer t.policyMu.RUnlock()
	return t.managedWorkspaceApplyBack
}

func (t *BashTool) Name() string {
	return "bash"
}

func (t *BashTool) Description() string {
	t.policyMu.RLock()
	backgroundAllowed := t.backgroundAllowed
	t.policyMu.RUnlock()
	description := `Executes a bash command and returns the output. Use for system operations, git commands, running tests, etc.

PARAMETERS:
- command (required): The bash command to execute
- description (optional): Brief description of what the command does
- stdin (optional): Content to pipe as stdin to the command
- run_in_background (optional): If true, run in background and return task ID

TIMEOUT:
- Default: 30 seconds
- Long commands: Use run_in_background=true
- Check background tasks: Use task_output tool with task_id

BLOCKED COMMANDS (safety):
- rm -rf /
- mkfs
- Fork bombs
- Direct device writes

COMMON USE CASES:
- Build: "go build ./...", "npm run build"
- Test: "go test ./...", "pytest", "npm test"
- Git: "git status", "git diff", "git log --oneline -10"
- Install: "go mod tidy", "npm install"
- Run: "go run cmd/main.go", "node app.js"

OUTPUT:
- stdout and stderr are captured
- Output >30000 chars is truncated
- Exit codes are reported on failure

AFTER RUNNING - YOU MUST:
1. Explain what the command did
2. Summarize the output (don't just dump it)
3. Highlight errors or warnings
4. Suggest fixes if command failed
5. Recommend next steps`
	if !backgroundAllowed {
		description = strings.Replace(description,
			"- run_in_background (optional): If true, run in background and return task ID",
			"- Background execution is disabled; every command must finish within this turn", 1)
		description = strings.Replace(description,
			"- Long commands: Use run_in_background=true\n- Check background tasks: Use task_output tool with task_id",
			"- Long commands must run in the foreground and honor the configured timeout", 1)
	}
	return description
}

func (t *BashTool) Declaration() *genai.FunctionDeclaration {
	t.policyMu.RLock()
	backgroundAllowed := t.backgroundAllowed
	t.policyMu.RUnlock()
	properties := map[string]*genai.Schema{
		"command": {
			Type:        genai.TypeString,
			Description: "The bash command to execute",
		},
		"description": {
			Type:        genai.TypeString,
			Description: "A brief description of what the command does",
		},
		"stdin": {
			Type:        genai.TypeString,
			Description: "Content to pipe as stdin to the command",
		},
	}
	if backgroundAllowed {
		properties["run_in_background"] = &genai.Schema{
			Type:        genai.TypeBoolean,
			Description: "If true, run the command in background and return task ID immediately",
		}
	}
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type:       genai.TypeObject,
			Properties: properties,
			Required:   []string{"command"},
		},
	}
}

func (t *BashTool) Validate(args map[string]any) error {
	t.policyMu.RLock()
	defer t.policyMu.RUnlock()
	return t.validateLocked(args)
}

func (t *BashTool) validateLocked(args map[string]any) error {
	command, ok := GetString(args, "command")
	if !ok || command == "" {
		return NewValidationError("command", "is required")
	}

	// Command safety blocklist always applies regardless of mode — fork bombs,
	// rm -rf /, reverse shells etc. must never execute.
	result := security.ValidateCommand(command)
	if !result.Valid {
		return NewValidationError("command", fmt.Sprintf("blocked: %s", result.Reason))
	}
	if GetBoolDefault(args, "run_in_background", false) && !t.backgroundAllowed {
		return NewValidationError("run_in_background", "is disabled for this execution mode; run the command in the foreground")
	}

	// Skip permission-level validation in unrestricted mode (sandbox=off + permissions=off)
	if t.unrestrictedMode {
		return nil
	}

	if err := t.validateManagedWorkspaceCommand(command); err != nil {
		return NewValidationError("command", err.Error())
	}

	return nil
}

func (t *BashTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if err := t.acquireExecution(ctx); err != nil {
		return startCommandErrorResult(ctx, err), nil
	}
	defer t.releaseExecution()

	t.policyMu.RLock()
	view, revision := t.executionViewLocked()
	t.policyMu.RUnlock()

	result, err := view.executeLocked(ctx, args)
	t.commitExecutionSession(view, revision)
	return result, err
}

// executeLocked runs against an invocation-local immutable policy/session view.
// Permission-bound callers use the same core after verifying their snapshot.
func (t *BashTool) executeLocked(ctx context.Context, args map[string]any) (ToolResult, error) {
	command, _ := GetString(args, "command")
	stdinContent, _ := GetString(args, "stdin")

	if err := t.validateManagedWorkspaceCommand(command); err != nil {
		return NewErrorResult(err.Error()), nil
	}

	// Check if should run in background
	runInBackground, _ := args["run_in_background"].(bool)

	if runInBackground {
		if !t.backgroundAllowed {
			return NewPolicyBlockedResult(PolicyBlockSafety,
				"run_in_background is disabled for deterministic one-shot execution; run the command in the foreground"), nil
		}
		if t.managedWorkspaceApplyBack {
			return NewErrorResult("run_in_background is not supported in isolated apply-back bash mode"), nil
		}
		if stdinContent != "" {
			return NewErrorResult("stdin is not supported with run_in_background=true"), nil
		}
		return t.executeBackground(ctx, command)
	}

	return t.executeForeground(ctx, command, stdinContent)
}

// executeBackground starts a command in background and returns task ID.
func (t *BashTool) executeBackground(ctx context.Context, command string) (ToolResult, error) {
	if t.taskManager == nil {
		return NewErrorResult("background tasks not configured"), nil
	}

	// Detach from caller's context so the task survives tool timeout.
	// WithoutCancel preserves context values but removes cancellation.
	// Task.Cancel() via task_stop still works (task has its own cancelFunc).
	bgCtx := context.WithoutCancel(ctx)

	taskID, err := t.taskManager.Start(bgCtx, command)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("failed to start background task: %s", err)), nil
	}

	return NewSuccessResultWithData(
		fmt.Sprintf("Started background task: %s\nUse task_output tool with task_id=\"%s\" to check status and get output.", taskID, taskID),
		map[string]any{
			"task_id":    taskID,
			"background": true,
		},
	), nil
}

// buildSessionEnv creates a sanitized environment with session env vars injected.
func (t *BashTool) buildSessionEnv() []string {
	env := buildSafeEnv()

	if t.managedWorkspaceApplyBack && t.workspaceRoot != "" {
		tmpDir := filepath.Join(t.workspaceRoot, ".gokin-tmp")
		if err := os.MkdirAll(tmpDir, 0755); err != nil {
			logging.Debug("failed to create managed bash tmp dir", "dir", tmpDir, "error", err)
		}
		env = upsertEnvVar(env, "HOME", t.workspaceRoot)
		env = upsertEnvVar(env, "PWD", t.sessionWorkDir())
		env = upsertEnvVar(env, "TMPDIR", tmpDir)
		env = upsertEnvVar(env, "TMP", tmpDir)
		env = upsertEnvVar(env, "TEMP", tmpDir)
	}

	// Inject session environment variables
	sessionEnv := t.session.Env()
	for key, val := range sessionEnv {
		if t.managedWorkspaceApplyBack && isManagedBashProtectedEnv(key) {
			continue
		}
		// For PATH, append instead of replacing to prevent hijacking
		if strings.ToUpper(key) == "PATH" {
			for i, e := range env {
				if strings.HasPrefix(e, "PATH=") {
					env[i] = e + string(os.PathListSeparator) + val
					break
				}
			}
			continue
		}

		// Override existing entry or append
		found := false
		prefix := key + "="
		for i, e := range env {
			if strings.HasPrefix(e, prefix) {
				env[i] = key + "=" + val
				found = true
				break
			}
		}
		if !found {
			env = append(env, key+"="+val)
		}
	}

	return env
}

// pwdMarker is appended to commands to reliably detect the final working directory.
const pwdMarker = "___GOKIN_PWD___"

// wrapCommandWithPWD appends a pwd probe to the command so we can reliably
// track working directory changes across compound commands, subshells, cd -, etc.
func wrapCommandWithPWD(command string) string {
	return command + "\n__gokin_rc=$?; echo '" + pwdMarker + "'$(pwd); exit $__gokin_rc"
}

// extractPWDFromOutput finds the pwd marker in output, extracts the real pwd,
// and returns the cleaned output (with marker removed) and the detected directory.
func extractPWDFromOutput(output string) (string, string) {
	idx := strings.LastIndex(output, pwdMarker)
	if idx < 0 {
		return output, ""
	}

	// Extract the directory path after the marker
	afterMarker := output[idx+len(pwdMarker):]
	detectedDir := strings.TrimSpace(strings.SplitN(afterMarker, "\n", 2)[0])

	// Remove the marker line from output
	cleaned := output[:idx]
	// Also remove trailing newline before marker if present
	cleaned = strings.TrimRight(cleaned, "\n")
	// Restore one trailing newline for clean output
	if cleaned != "" {
		cleaned += "\n"
	}

	return cleaned, detectedDir
}

// updateSessionAfterCommand uses the detected pwd from the command output
// to update the session working directory. Falls back to heuristic parsing.
func (t *BashTool) updateSessionAfterCommand(command string) {
	// Fallback: legacy heuristic for simple cd commands (used when pwd detection unavailable)
	t.updateSessionAfterCommandLegacy(command)
}

// updateSessionFromPWD updates the session working directory from detected pwd.
func (t *BashTool) updateSessionFromPWD(detectedDir string) {
	if detectedDir == "" {
		return
	}
	detectedDir = filepath.Clean(detectedDir)
	if t.workspaceBoundaryEnabled && !t.isWithinWorkspace(detectedDir) {
		logging.Debug("bash session attempted to leave workspace boundary", "detected_dir", detectedDir, "workspace_root", t.workspaceRoot)
		t.session.SetWorkDir(t.workspaceRoot)
		return
	}
	if info, err := os.Stat(detectedDir); err == nil && info.IsDir() {
		t.session.SetWorkDir(detectedDir)
	}
}

// updateSessionAfterCommandLegacy is the original heuristic cd-tracking.
func (t *BashTool) updateSessionAfterCommandLegacy(command string) {
	trimmed := strings.TrimSpace(command)

	if trimmed == "cd" || trimmed == "cd~" || trimmed == "cd ~" {
		if home, err := os.UserHomeDir(); err == nil {
			t.session.SetWorkDir(home)
		}
		return
	}

	if trimmed == "cd -" {
		return
	}

	if !strings.HasPrefix(trimmed, "cd ") {
		return
	}

	rest := strings.TrimPrefix(trimmed, "cd ")
	rest = strings.TrimSpace(rest)

	for _, sep := range []string{"&&", "||", ";", "|"} {
		if strings.Contains(rest, sep) {
			return
		}
	}

	if (strings.HasPrefix(rest, "\"") && strings.HasSuffix(rest, "\"")) ||
		(strings.HasPrefix(rest, "'") && strings.HasSuffix(rest, "'")) {
		rest = rest[1 : len(rest)-1]
	}

	if strings.HasPrefix(rest, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			rest = home + rest[1:]
		}
	}

	if rest == "" {
		return
	}

	currentDir := t.session.WorkDir()
	var target string
	if filepath.IsAbs(rest) {
		target = rest
	} else {
		target = filepath.Join(currentDir, rest)
	}

	target = filepath.Clean(target)
	if t.workspaceBoundaryEnabled && !t.isWithinWorkspace(target) {
		t.session.SetWorkDir(t.workspaceRoot)
		return
	}

	if info, err := os.Stat(target); err == nil && info.IsDir() {
		t.session.SetWorkDir(target)
	}
}

// startCommandErrorResult turns a cmd.Start() failure into an honest result.
// When an already-expired or cancelled context is the real cause, the generic
// "could not run command (check that the command exists and is executable)"
// message is actively misleading — git/ls/etc. exist fine, the run simply never
// got to start. That happens when the tool timeout elapsed/was misconfigured
// (e.g. tools.timeout serialized as 0s) or the user cancelled (Esc). Classify
// those explicitly so neither the user nor the model chases a phantom
// "bad command" problem.
func startCommandErrorResult(execCtx context.Context, err error) ToolResult {
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(execCtx.Err(), context.DeadlineExceeded):
		return NewErrorResult("command did not start: the tool timeout elapsed before execution began (the tool timeout may be misconfigured — check tools.timeout)")
	case errors.Is(err, context.Canceled) || errors.Is(execCtx.Err(), context.Canceled):
		return NewErrorResult("command did not start: cancelled before execution began")
	default:
		return NewErrorResult(fmt.Sprintf("could not run command: %s (check that the command exists and is executable)", err))
	}
}

func interruptedCommandResult(execCtx context.Context, configuredTimeout time.Duration) ToolResult {
	switch {
	case errors.Is(execCtx.Err(), context.Canceled):
		return NewErrorResult("command cancelled")
	case configuredTimeout > 0:
		return NewErrorResult(fmt.Sprintf(
			"command timed out after %v. For long-running commands, use run_in_background=true",
			configuredTimeout,
		))
	default:
		return NewErrorResult("command deadline exceeded")
	}
}

// executeForeground runs a command and waits for completion.
func (t *BashTool) executeForeground(ctx context.Context, command string, stdinContent string) (ToolResult, error) {
	// Create context with explicit timeout to prevent indefinite hangs
	execCtx := ctx
	if t.timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, t.timeout)
		defer cancel()
	}

	// Apply sandboxing if enabled
	if t.sandboxEnabled {
		if stdinContent != "" {
			return NewErrorResult("stdin is not supported in sandbox mode"), nil
		}
		// Use sandbox wrapper for command execution
		return t.executeSandboxed(execCtx, command)
	}

	// Use session working directory
	workDir := t.sessionWorkDir()

	// Wrap command with pwd probe for reliable directory tracking
	if t.managedWorkspaceApplyBack {
		command = t.wrapManagedWorkspaceCommand(command)
	}
	wrappedCommand := wrapCommandWithPWD(command)

	// Own cancellation explicitly below. exec.CommandContext kills only the
	// shell leader when the context fires; its descendants remain in the
	// process group and can keep mutating files after Wait returns. A plain Cmd
	// lets the select below terminate the whole process tree before returning.
	if err := execCtx.Err(); err != nil {
		return startCommandErrorResult(execCtx, err), nil
	}
	cmd := exec.Command("bash", "-c", wrappedCommand)
	cmd.Dir = workDir

	// Use sanitized environment with session env vars injected
	cmd.Env = t.buildSessionEnv()

	// Set up process group for proper cleanup of child processes
	setBashProcAttr(cmd)

	// Set stdin if provided
	if stdinContent != "" {
		cmd.Stdin = strings.NewReader(stdinContent)
	}

	// Get progress callback for streaming output
	onProgress := GetProgressCallback(ctx)

	// Set up output capture with optional streaming
	var stdout, stderr bytes.Buffer
	if onProgress != nil {
		// Use pipes for streaming output
		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			return NewErrorResult(fmt.Sprintf("could not capture command output: %s", err)), nil
		}
		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			return NewErrorResult(fmt.Sprintf("could not capture command output: %s", err)), nil
		}

		// Start command
		if err := cmd.Start(); err != nil {
			return startCommandErrorResult(execCtx, err), nil
		}

		// Read stdout and stderr in goroutines
		var readerWg sync.WaitGroup
		var stdoutMu, stderrMu sync.Mutex

		var totalBytesRead int64
		var totalBytesMu sync.Mutex

		readerWg.Add(2)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logging.Error("panic in bash stdout reader",
						"error", r,
						"stack", logging.PanicStack())
				}
			}()
			defer readerWg.Done()
			buf := make([]byte, 4096)
			for {
				n, err := stdoutPipe.Read(buf)
				if n > 0 {
					stdoutMu.Lock()
					stdout.Write(buf[:n])
					stdoutMu.Unlock()

					totalBytesMu.Lock()
					totalBytesRead += int64(n)
					total := totalBytesRead
					totalBytesMu.Unlock()

					// Report output progress for long-running commands
					if onProgress != nil && total > 0 && total%(32*1024) < 4096 {
						onProgress(-1, fmt.Sprintf("Output: %d KB", total/1024))
					}
				}
				if err != nil {
					break
				}
			}
		}()
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logging.Error("panic in bash stderr reader",
						"error", r,
						"stack", logging.PanicStack())
				}
			}()
			defer readerWg.Done()
			buf := make([]byte, 4096)
			for {
				n, err := stderrPipe.Read(buf)
				if n > 0 {
					stderrMu.Lock()
					stderr.Write(buf[:n])
					stderrMu.Unlock()
				}
				if err != nil {
					break
				}
			}
		}()

		// Periodically flush partial output to the progress callback
		streamStop := make(chan struct{})
		streamDone := make(chan struct{})
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logging.Error("panic in bash streaming flush",
						"error", r,
						"stack", logging.PanicStack())
				}
			}()
			defer close(streamDone)
			ticker := time.NewTicker(StreamingFlushInterval)
			defer ticker.Stop()
			lastSentLen := 0
			for {
				select {
				case <-ticker.C:
					stdoutMu.Lock()
					current := stdout.String()
					stdoutMu.Unlock()
					if len(current) > lastSentLen {
						partial := current[lastSentLen:]
						onProgress(0, partial)
						lastSentLen = len(current)
					}
				case <-streamStop:
					return
				}
			}
		}()

		// Wait for command completion
		var cmdErr error
		cmdDone := make(chan struct{})
		go func() {
			defer close(cmdDone) // Guarantees close on any exit path (including panic)
			defer func() {
				if r := recover(); r != nil {
					logging.Error("panic in bash cmd.Wait",
						"error", r,
						"stack", logging.PanicStack())
				}
			}()
			cmdErr = cmd.Wait()
		}()

		timedOut := false
		select {
		case <-cmdDone:
			// A context can become ready at the same instant as Wait. Treat it as
			// cancellation and clean any descendants that outlived the shell.
			if execCtx.Err() != nil {
				timedOut = true
				killBashProcessGroup(cmd, 5*time.Second, cmdDone)
			} else {
				// Normal exit: sweep group survivors a backgrounded child
				// (`x &`) left behind — they'd be orphaned forever otherwise.
				reapLeftoverBashDescendants(cmd)
			}
		case <-execCtx.Done():
			timedOut = true
			killBashProcessGroup(cmd, 5*time.Second, cmdDone)
		}
		if timedOut {
			<-cmdDone // Always reap the shell leader before returning.
		}

		// Wait for readers to drain
		readerWg.Wait()
		// Stop the streaming goroutine and wait for it to exit
		close(streamStop)
		<-streamDone

		if timedOut {
			return interruptedCommandResult(execCtx, t.timeout), nil
		}

		// Extract real pwd from output and update session
		rawOutput := stdout.String()
		cleanOutput, detectedDir := extractPWDFromOutput(rawOutput)
		if detectedDir != "" {
			t.updateSessionFromPWD(detectedDir)
		} else if cmdErr == nil {
			t.updateSessionAfterCommand(command)
		}

		if cmdErr != nil {
			exitErr, ok := cmdErr.(*exec.ExitError)
			if ok {
				return t.buildExitResult(command, cleanOutput, stderr.String(), exitErr.ExitCode()), nil
			}
			return NewErrorResult(fmt.Sprintf("command error: %s", cmdErr)), nil
		}

		return t.buildResult(cleanOutput, stderr.String()), nil
	}

	// Non-streaming path: capture output directly
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Start command
	err := cmd.Start()
	if err != nil {
		return startCommandErrorResult(execCtx, err), nil
	}

	// Use WaitGroup to safely track command completion and avoid race condition
	// between context cancellation and command completion
	var wg sync.WaitGroup
	var cmdErr error
	var cmdErrMu sync.Mutex
	cmdDone := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				logging.Error("panic in bash cmd.Wait (timeout path)",
					"panic", r,
					"stack", logging.PanicStack())
				close(cmdDone)
			}
		}()
		waitErr := cmd.Wait()
		cmdErrMu.Lock()
		cmdErr = waitErr
		cmdErrMu.Unlock()
		close(cmdDone)
	}()

	// Track if we timed out to provide proper error message
	timedOut := false

	select {
	case <-cmdDone:
		// A context can become ready at the same instant as Wait. Treat it as
		// cancellation and clean any descendants that outlived the shell.
		if execCtx.Err() != nil {
			timedOut = true
			killBashProcessGroup(cmd, 5*time.Second, cmdDone)
		} else {
			// Normal exit: sweep group survivors a backgrounded child
			// (`x &`) left behind — they'd be orphaned forever otherwise.
			reapLeftoverBashDescendants(cmd)
		}
	case <-execCtx.Done():
		// Context was cancelled or timed out
		timedOut = true
		// Kill the process group with graceful shutdown (5 second grace period)
		killBashProcessGroup(cmd, 5*time.Second, cmdDone)
	}
	// Always reap the shell leader. On cancellation this also guarantees the
	// Wait goroutine cannot leak after the tool returns.
	wg.Wait()

	// At this point, command has definitely finished (either completed or killed)
	// Safely read the error
	cmdErrMu.Lock()
	finalErr := cmdErr
	cmdErrMu.Unlock()

	// Handle timeout case
	if timedOut {
		return interruptedCommandResult(execCtx, t.timeout), nil
	}

	// Extract real pwd from output and update session
	rawOutput := stdout.String()
	cleanOutput, detectedDir := extractPWDFromOutput(rawOutput)
	if detectedDir != "" {
		t.updateSessionFromPWD(detectedDir)
	} else if finalErr == nil {
		t.updateSessionAfterCommand(command)
	}

	// Handle command error
	if finalErr != nil {
		exitErr, ok := finalErr.(*exec.ExitError)
		if ok {
			return t.buildExitResult(command, cleanOutput, stderr.String(), exitErr.ExitCode()), nil
		}
		return NewErrorResult(fmt.Sprintf("command failed: %s", finalErr)), nil
	}

	return t.buildResult(cleanOutput, stderr.String()), nil
}

// buildExitResult turns a non-zero command exit into a ToolResult. A benign
// non-zero exit (grep/rg/ag finding no matches, diff finding differences —
// exit 1 with no stderr) is NOT a failure: it returns a normal success result
// so the UI shows a quiet collapsed line ("(no matches)") instead of a red
// error card with retry/undo. Genuine failures keep the error result, with
// stderr appended so compile/test diagnostics reach the model.
func (t *BashTool) buildExitResult(command, stdout, stderr string, exitCode int) ToolResult {
	if benignNonZeroExit(command, exitCode, stderr) {
		res := t.buildResult(stdout, "")
		if res.Content == "ok" || res.Content == "Command completed successfully (no output)." {
			// buildResult's empty-output fallback. For a search/compare that
			// produced nothing, a class-aware label reads far better than "ok".
			res.Content = benignEmptyLabel(command)
		}
		return res
	}
	content := bashFailureSummary(command, exitCode, stdout, stderr)
	if stdout != "" {
		content += "\n\n" + stdout
	}
	if stderr != "" {
		if content != "" {
			content += "\n"
		}
		content += "STDERR:\n" + stderr
	}
	return ToolResult{
		Content: content,
		Error:   fmt.Sprintf("command exited with code %d", exitCode),
		Success: false,
	}
}

func bashFailureSummary(command string, exitCode int, stdout, stderr string) string {
	var b strings.Builder
	b.WriteString("Actionable summary:\n")
	fmt.Fprintf(&b, "- Command failed with exit code %d: %s\n", exitCode, command)
	if strings.TrimSpace(stderr) != "" {
		b.WriteString("- Primary diagnostics are in STDERR below.\n")
	} else if strings.TrimSpace(stdout) != "" {
		b.WriteString("- Primary diagnostics are in stdout below.\n")
	} else {
		b.WriteString("- The command produced no diagnostics; check the command, working directory, and required files.\n")
	}
	if commandLooksLikeValidation(command) {
		b.WriteString("- Next: fix the reported issue, then rerun this same validation command or the narrowest failing subset.\n")
	} else {
		b.WriteString("- Next: inspect the diagnostic lines, fix the root cause, and rerun a focused verification command.\n")
	}
	return b.String()
}

func commandLooksLikeValidation(command string) bool {
	lower := strings.ToLower(command)
	patterns := []string{
		"go test", "go build", "go vet", "pytest", "npm test", "npm run test",
		"npm run lint", "pnpm test", "yarn test", "cargo test", "cargo check",
		"make test", "make check", "mvn test", "gradle test", "ruff", "mypy",
		"tsc", "eslint",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// benignNonZeroExit reports whether a non-zero exit is an expected, non-error
// outcome rather than a real failure — chiefly grep/rg/ag finding no matches
// (exit 1) and diff/cmp finding differences (exit 1). A real error from these
// writes to stderr and/or exits >1, so we require exit==1 AND empty stderr.
func benignNonZeroExit(command string, exitCode int, stderr string) bool {
	if exitCode != 1 || strings.TrimSpace(stderr) != "" {
		return false
	}
	prog, sub := exitDeterminingProgram(command)
	if prog == "git" && sub == "grep" {
		return true
	}
	switch prog {
	case "grep", "egrep", "fgrep", "rg", "ag", "ack", "diff", "cmp":
		return true
	}
	return false
}

// exitDeterminingProgram returns the (basename) program — and its first arg —
// whose exit code the shell reports for `command`. It strips a leading
// "cd <path> &&" prefix (models prepend this constantly) but deliberately does
// NOT split on "|": a grep alternation regex (grep 'a|b' file) contains a pipe
// and must not be mis-parsed. Pipelines that end in a different program simply
// fall through to the normal error path — conservative and correct.
func exitDeterminingProgram(command string) (prog, sub string) {
	c := strings.TrimSpace(command)
	for strings.HasPrefix(c, "cd ") {
		idx := strings.Index(c, "&&")
		if idx < 0 {
			break
		}
		c = strings.TrimSpace(c[idx+2:])
	}
	// A pipeline's exit status is its LAST command's, so a no-match
	// `ps aux | grep foo` exits 1 from grep. Split on a space-padded " | "
	// to find that trailing command, but NOT on a bare "|": a grep alternation
	// regex like 'a|b' (no surrounding spaces, usually quoted) must stay intact.
	if idx := strings.LastIndex(c, " | "); idx >= 0 {
		c = strings.TrimSpace(c[idx+3:])
	}
	fields := strings.Fields(c)
	if len(fields) == 0 {
		return "", ""
	}
	prog = filepath.Base(fields[0])
	if len(fields) > 1 {
		sub = fields[1]
	}
	return prog, sub
}

// benignEmptyLabel is the body shown for a benign non-zero exit that produced
// no stdout — tool-class aware so a no-match search reads "(no matches)" while
// a silent compare (cmp -s, diff with output suppressed) reads "(differs)".
func benignEmptyLabel(command string) string {
	if prog, _ := exitDeterminingProgram(command); prog == "diff" || prog == "cmp" {
		return "(differs)"
	}
	return "(no matches)"
}

// buildResult constructs a ToolResult from stdout and stderr output.
func (t *BashTool) buildResult(stdoutStr, stderrStr string) ToolResult {
	var output strings.Builder

	if len(stdoutStr) > 0 {
		output.WriteString(stdoutStr)
	}

	if len(stderrStr) > 0 {
		if output.Len() > 0 {
			output.WriteString("\n")
		}
		output.WriteString("STDERR:\n")
		output.WriteString(stderrStr)
	}

	// Smart filter: deduplicate, remove noise, collapse blanks (before truncation)
	result := FilterBashOutput(output.String())
	const maxLen = 30000
	const headSize = 10000
	const tailSize = 20000
	if runes := []rune(result); len(runes) > maxLen {
		head := string(runes[:headSize])
		tail := string(runes[len(runes)-tailSize:])
		omitted := string(runes[headSize : len(runes)-tailSize])
		omittedLines := strings.Count(omitted, "\n")
		result = head +
			fmt.Sprintf("\n\n... [%d lines, %d chars truncated — showing first %d and last %d chars] ...\n\n", omittedLines, len(omitted), headSize, tailSize) +
			tail
	}

	if result == "" {
		result = "Command completed successfully (no output)."
	}

	return NewSuccessResult(result)
}

// executeSandboxed executes the command with sandbox isolation
func (t *BashTool) executeSandboxed(ctx context.Context, command string) (ToolResult, error) {
	// Create sandbox configuration
	sandboxConfig := security.DefaultSandboxConfig()
	sandboxConfig.Enabled = true

	// Create sandboxed command — use sessionWorkDir so that cd changes made in
	// earlier sandboxed commands are respected, matching non-sandboxed behaviour.
	sandboxed, err := security.NewSandboxedCommand(ctx, t.sessionWorkDir(), command, sandboxConfig)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("sandbox setup failed: %s", err)), nil
	}

	// Run the sandboxed command
	result := sandboxed.Run(t.timeout)

	// Handle errors
	if result.Error != nil {
		return NewErrorResult(fmt.Sprintf("command failed in sandbox: %s", result.Error)), nil
	}

	// Build output
	var output strings.Builder
	if len(result.Stdout) > 0 {
		output.Write(result.Stdout)
	}
	if len(result.Stderr) > 0 {
		if output.Len() > 0 {
			output.WriteString("\n")
		}
		output.WriteString("STDERR:\n")
		output.Write(result.Stderr)
	}

	// Check exit code
	if result.ExitCode != 0 {
		return t.buildExitResult(command, string(result.Stdout), string(result.Stderr), result.ExitCode), nil
	}

	return NewSuccessResult(output.String()), nil
}

var managedWorkspaceBlockedFragments = []struct {
	fragment string
	reason   string
}{
	{"git commit", "git commits inside isolated workspaces cannot be applied back safely"},
	{"git checkout", "git checkouts inside isolated workspaces can rewrite HEAD and break apply-back"},
	{"git switch", "git switches inside isolated workspaces can rewrite HEAD and break apply-back"},
	{"git merge", "git merges inside isolated workspaces cannot be replayed safely"},
	{"git rebase", "git rebases inside isolated workspaces cannot be replayed safely"},
	{"git cherry-pick", "git cherry-picks inside isolated workspaces cannot be replayed safely"},
	{"git pull", "git pull changes repository state outside the apply-back flow"},
	{"git fetch", "git fetch changes repository state outside the apply-back flow"},
	{"git push", "git push has external side effects"},
	{"git branch", "git branch mutations inside isolated workspaces cannot be applied back safely"},
	{"git stash", "git stash mutates git state outside the apply-back flow"},
	{"git reset", "git reset can rewrite git state outside the apply-back flow"},
	{"git clean", "git clean can delete files outside the apply-back review flow"},
	{"git tag", "git tag mutates git refs outside the apply-back flow"},
}

func (t *BashTool) validateManagedWorkspaceCommand(command string) error {
	if !t.managedWorkspaceApplyBack {
		return nil
	}

	normalized := strings.Join(strings.Fields(strings.ToLower(command)), " ")
	for _, blocked := range managedWorkspaceBlockedFragments {
		if strings.Contains(normalized, blocked.fragment) {
			return fmt.Errorf("blocked in isolated apply-back mode: %s", blocked.reason)
		}
	}

	return nil
}

func (t *BashTool) sessionWorkDir() string {
	workDir := t.session.WorkDir()
	if t.workspaceBoundaryEnabled && !t.isWithinWorkspace(workDir) {
		t.session.SetWorkDir(t.workspaceRoot)
		return t.workspaceRoot
	}
	return workDir
}

func (t *BashTool) isWithinWorkspace(path string) bool {
	if !t.workspaceBoundaryEnabled || strings.TrimSpace(t.workspaceRoot) == "" {
		return true
	}

	cleanPath := filepath.Clean(path)
	rel, err := filepath.Rel(t.workspaceRoot, cleanPath)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func (t *BashTool) wrapManagedWorkspaceCommand(command string) string {
	root := shellSingleQuote(t.workspaceRoot)
	return fmt.Sprintf(`workspace_root=%s
__gokin_assert_workspace() {
  current_dir="$(pwd -P)"
  case "$current_dir" in
    "$workspace_root"|"$workspace_root"/*) return 0 ;;
  esac
  printf 'workspace boundary violation: %%s\n' "$current_dir" >&2
  return 98
}
cd() {
  if [ "$#" -eq 0 ]; then
    builtin cd "$HOME" || return $?
  else
    builtin cd "$@" || return $?
  fi
  __gokin_assert_workspace
}
pushd() {
  builtin pushd "$@" >/dev/null || return $?
  __gokin_assert_workspace
}
popd() {
  builtin popd "$@" >/dev/null || return $?
  __gokin_assert_workspace
}
builtin cd "$workspace_root" || exit 1
__gokin_assert_workspace || exit $?
%s`, root, command)
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func isManagedBashProtectedEnv(key string) bool {
	switch strings.ToUpper(strings.TrimSpace(key)) {
	case "PATH", "HOME", "PWD", "TMPDIR", "TMP", "TEMP":
		return true
	default:
		return false
	}
}

func upsertEnvVar(env []string, key, value string) []string {
	prefix := key + "="
	for i, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			env[i] = key + "=" + value
			return env
		}
	}
	return append(env, key+"="+value)
}
