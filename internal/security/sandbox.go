package security

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"gokin/internal/logging"
)

// SandboxConfig holds sandbox configuration
type SandboxConfig struct {
	// Enabled determines if sandboxing is active
	Enabled bool
	// RootDir is the root directory for chroot (empty = use current workDir)
	RootDir string
	// EnableSeccomp enables seccomp-bpf syscall filtering (Linux only)
	EnableSeccomp bool
	// ReadOnly makes the sandbox filesystem read-only
	ReadOnly bool
}

// DefaultSandboxConfig returns the default sandbox configuration
func DefaultSandboxConfig() SandboxConfig {
	return SandboxConfig{
		Enabled:       true,
		EnableSeccomp: false, // Disabled by default (requires libseccomp)
		ReadOnly:      false,
	}
}

// SandboxResult represents the result of a sandboxed command execution
type SandboxResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	Error    error
}

// SandboxedCommand represents a command that will be executed in a sandbox
type SandboxedCommand struct {
	cmd    *exec.Cmd
	ctx    context.Context
	config SandboxConfig
}

// NewSandboxedCommand creates a new sandboxed command
// Note: Full chroot and seccomp require Linux and specific permissions
// This implementation provides basic isolation with safety checks
func NewSandboxedCommand(ctx context.Context, workDir string, command string, config SandboxConfig) (*SandboxedCommand, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context cannot be nil")
	}

	// Validate workDir before doing anything
	if workDir == "" {
		return nil, fmt.Errorf("workDir cannot be empty")
	}

	// Resolve absolute path to prevent directory traversal
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workDir: %w", err)
	}

	// Check if directory exists
	if _, err := os.Stat(absWorkDir); err != nil {
		return nil, fmt.Errorf("workDir does not exist: %s", absWorkDir)
	}

	// Cancellation is owned explicitly by Run. exec.CommandContext kills only
	// the bash leader, allowing descendants to survive after Wait returns.
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = absWorkDir

	// Set safe environment variables (limited set)
	cmd.Env = safeEnvironment(absWorkDir)

	sandboxed := &SandboxedCommand{
		cmd:    cmd,
		ctx:    ctx,
		config: config,
	}
	// A process-tree handle is required even when namespace sandboxing is
	// disabled (tests, unsupported hosts, or an explicitly disabled config).
	configureSandboxProcessGroup(cmd)

	// Apply sandboxing if enabled
	if config.Enabled {
		if err := sandboxed.applySandbox(absWorkDir); err != nil {
			return nil, fmt.Errorf("failed to apply sandbox: %w", err)
		}
	}

	return sandboxed, nil
}

// sandboxPATH returns a safe PATH that includes platform-specific directories.
func sandboxPATH() string {
	base := "/usr/local/bin:/usr/bin:/bin"
	if runtime.GOOS == "darwin" {
		// Homebrew on Apple Silicon installs to /opt/homebrew/bin
		return "/opt/homebrew/bin:" + base
	}
	return base
}

// safeEnvironment returns a sanitized environment with safe defaults
func safeEnvironment(workDir string) []string {
	// Safe environment variables whitelist
	safeVars := map[string]string{
		"PATH":        sandboxPATH(),
		"HOME":        workDir,
		"USER":        os.Getenv("USER"),
		"TERM":        "xterm",
		"LANG":        "en_US.UTF-8",
		"LC_ALL":      "en_US.UTF-8",
		"PWD":         workDir,
		"TMPDIR":      filepath.Join(workDir, "tmp"),
		"SHELL":       "/bin/bash",
		"GOPATH":      os.Getenv("GOPATH"),
		"GOROOT":      os.Getenv("GOROOT"),
		"GOPROXY":     os.Getenv("GOPROXY"),
		"NODE_PATH":   os.Getenv("NODE_PATH"),
		"PYTHONPATH":  os.Getenv("PYTHONPATH"),
		"VIRTUAL_ENV": os.Getenv("VIRTUAL_ENV"),
		"EDITOR":      os.Getenv("EDITOR"),
		"VISUAL":      os.Getenv("VISUAL"),
	}

	// Build environment array
	env := make([]string, 0, len(safeVars))
	for k, v := range safeVars {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}

	return env
}

// Run runs the sandboxed command and returns the result
func (sc *SandboxedCommand) Run(timeout time.Duration) *SandboxResult {
	result := &SandboxResult{ExitCode: -1}
	if err := sc.ctx.Err(); err != nil {
		result.Error = err
		return result
	}

	// Capture stdout and stderr
	stdout, err := sc.cmd.StdoutPipe()
	if err != nil {
		result.Error = fmt.Errorf("failed to create stdout pipe: %w", err)
		return result
	}

	stderr, err := sc.cmd.StderrPipe()
	if err != nil {
		result.Error = fmt.Errorf("failed to create stderr pipe: %w", err)
		return result
	}

	// Start the command
	if err := sc.cmd.Start(); err != nil {
		if ctxErr := sc.ctx.Err(); ctxErr != nil {
			result.Error = ctxErr
			return result
		}
		result.Error = fmt.Errorf("failed to start command: %w", err)
		return result
	}

	// Read each pipe in exactly one goroutine. Cancellation is handled by
	// terminating the process tree below, which closes both pipes; nested
	// per-pipe timeout goroutines used to outlive Run.
	type pipeResult struct {
		data []byte
		err  error
	}
	stdoutCh := make(chan pipeResult, 1)
	stderrCh := make(chan pipeResult, 1)

	go func() {
		data, err := io.ReadAll(stdout)
		stdoutCh <- pipeResult{data, err}
	}()
	go func() {
		data, err := io.ReadAll(stderr)
		stderrCh <- pipeResult{data, err}
	}()

	waitCh := make(chan error, 1)
	go func() {
		// os/exec documents Wait as RACY with StdoutPipe readers: Wait closes
		// the pipes the moment the process exits, discarding any not-yet-read
		// tail (CI-reproduced: ExitCode 0 with EMPTY stdout for a command
		// that printed right before exiting). Start Wait only after BOTH
		// readers hit EOF — natural on a clean exit, and forced on the
		// timeout/cancel paths because terminateSandboxProcessTree kills the
		// write ends. The results are re-buffered (channels have capacity 1)
		// for the post-select reads below.
		stdoutRes := <-stdoutCh
		stderrRes := <-stderrCh
		stdoutCh <- stdoutRes
		stderrCh <- stderrRes
		waitCh <- sc.cmd.Wait()
	}()

	var timeoutTimer *time.Timer
	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timeoutTimer = time.NewTimer(timeout)
		timeoutCh = timeoutTimer.C
		defer timeoutTimer.Stop()
	}

	var waitErr error
	var lifecycleErr error
	select {
	case waitErr = <-waitCh:
		// If completion raced with cancellation, still clean descendants that
		// may have outlived the bash leader.
		if err := sc.ctx.Err(); err != nil {
			lifecycleErr = err
			terminateSandboxProcessTree(sc.cmd)
		} else {
			// Normal exit: sweep group survivors a backgrounded child left
			// behind (the orphaned-`yes` leak) — ESRCH-safe no-op when the
			// group is already gone.
			terminateSandboxProcessTree(sc.cmd)
		}
	case <-sc.ctx.Done():
		lifecycleErr = sc.ctx.Err()
		terminateSandboxProcessTree(sc.cmd)
		waitErr = <-waitCh // Always reap the shell leader.
	case <-timeoutCh:
		lifecycleErr = fmt.Errorf("command timed out after %v", timeout)
		terminateSandboxProcessTree(sc.cmd)
		waitErr = <-waitCh // Always reap the shell leader.
	}

	// Wait for both readers after Wait/reap. On cancellation the process-tree
	// termination above closes inherited pipes, so neither goroutine survives
	// the Run call.
	stdoutRes := <-stdoutCh
	stderrRes := <-stderrCh
	result.Stdout = stdoutRes.data
	result.Stderr = stderrRes.data
	if stdoutRes.err != nil {
		logging.Debug("failed to read sandbox stdout", "error", stdoutRes.err)
	}
	if stderrRes.err != nil {
		logging.Debug("failed to read sandbox stderr", "error", stderrRes.err)
	}

	if lifecycleErr != nil {
		result.Error = lifecycleErr
		return result
	}

	// Get exit code
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			result.Error = nil // Exit code is in result.ExitCode
		} else {
			result.Error = waitErr
		}
	} else {
		result.ExitCode = 0
	}

	return result
}

// readWithTimeout reads from a pipe with a timeout.
// It reads all available data from the pipe until EOF or timeout.
func readWithTimeout(pipe any, timeout time.Duration) ([]byte, error) {
	reader, ok := pipe.(io.Reader)
	if !ok {
		return nil, fmt.Errorf("pipe is not an io.Reader")
	}
	if timeout <= 0 {
		return io.ReadAll(reader)
	}

	// Create a channel for the read result
	type readResult struct {
		data []byte
		err  error
	}
	resultChan := make(chan readResult, 1)

	// Read in a goroutine
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Error("panic in sandbox reader", "error", r)
				resultChan <- readResult{err: fmt.Errorf("panic in sandbox reader: %v", r)}
			}
		}()
		data, err := io.ReadAll(reader)
		resultChan <- readResult{data: data, err: err}
	}()

	// Wait for either completion or timeout
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-timer.C:
		// Pipes are closable: close them so the read goroutine cannot outlive
		// this helper. Generic non-closable readers retain the legacy timeout
		// behavior, but SandboxedCommand.Run no longer uses this helper.
		if closer, ok := reader.(io.Closer); ok {
			_ = closer.Close()
			<-resultChan
		}
		return nil, fmt.Errorf("read timeout after %v", timeout)
	case result := <-resultChan:
		return result.data, result.err
	}
}

// IsSandboxSupported checks if the current system supports sandboxing features
func IsSandboxSupported() (chroot, seccomp bool) {
	// Check if running on Linux
	return runtime.GOOS == "linux", runtime.GOOS == "linux"
}

// IsLinux checks if the current OS is Linux
func IsLinux() bool {
	return runtime.GOOS == "linux"
}
