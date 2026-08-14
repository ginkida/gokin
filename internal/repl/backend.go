package repl

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Detect probes secure backends instead of trusting executable presence. Some
// managed environments ship sandbox-exec/bwrap but prohibit their use; auto
// mode must quietly fall back rather than advertise a runtime that fails on the
// first cell.
func Detect(ctx context.Context, workDir string) Availability {
	manager, availability := OpenDetected(ctx, Options{WorkDir: workDir})
	if manager != nil {
		_ = manager.Close()
	}
	return availability
}

// Preflight checks only static prerequisites and never starts Python or the OS
// sandbox. Auto mode uses it before advertising a lazy repl_exec so hosts that
// plainly lack Python/a supported launcher do not spend a model round on a
// capability that cannot work. Available here means "worth probing", not that
// isolation has been verified; OpenDetected remains the fail-closed boundary.
func Preflight(workDir string) Availability {
	_, err := canonicalWorkDir(workDir)
	if err != nil {
		return Availability{Reason: err.Error()}
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		return Availability{Reason: "python3 was not found in PATH"}
	}
	var backend Backend
	switch runtime.GOOS {
	case "darwin":
		backend = BackendSandboxExec
	case "linux":
		backend = BackendBubblewrap
	default:
		return Availability{PythonPath: python, Reason: "no supported sandbox backend for " + runtime.GOOS}
	}
	if err := backendExecutableAvailable(backend); err != nil {
		return Availability{PythonPath: python, Reason: err.Error()}
	}
	return Availability{Available: true, PythonPath: python, Backend: backend}
}

// OpenDetected probes the platform sandbox and returns the already-started,
// verified manager on success. Callers that intend to use the REPL can retain
// it instead of paying for a throwaway probe worker followed by a second
// Python/sandbox startup. The probe itself does not count as a user execution.
func OpenDetected(ctx context.Context, opts Options) (*Manager, Availability) {
	if ctx == nil {
		ctx = context.Background()
	}
	workDir := opts.WorkDir
	root, err := canonicalWorkDir(workDir)
	if err != nil {
		return nil, Availability{Reason: err.Error()}
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		return nil, Availability{Reason: "python3 was not found in PATH"}
	}

	var candidates []Backend
	switch runtime.GOOS {
	case "darwin":
		candidates = []Backend{BackendSandboxExec}
	case "linux":
		candidates = []Backend{BackendBubblewrap}
	default:
		return nil, Availability{PythonPath: python, Reason: "no supported sandbox backend for " + runtime.GOOS}
	}

	reasons := make([]string, 0, len(candidates))
	for _, backend := range candidates {
		if err := backendExecutableAvailable(backend); err != nil {
			reasons = append(reasons, err.Error())
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		candidateOpts := opts
		candidateOpts.WorkDir = root
		candidateOpts.PythonPath = python
		candidateOpts.Backend = backend
		manager, err := newManager(candidateOpts, false)
		if err == nil {
			err = manager.Probe(probeCtx)
		}
		cancel()
		if err == nil {
			return manager, Availability{Available: true, PythonPath: manager.opts.PythonPath, Backend: backend}
		}
		if manager != nil {
			_ = manager.Close()
		}
		reasons = append(reasons, fmt.Sprintf("%s probe failed: %v", backend, err))
	}
	return nil, Availability{PythonPath: python, Reason: strings.Join(reasons, "; ")}
}

func backendExecutableAvailable(backend Backend) error {
	name := ""
	switch backend {
	case BackendSandboxExec:
		name = "sandbox-exec"
	case BackendBubblewrap:
		name = "bwrap"
	case BackendTest:
		return nil
	default:
		return fmt.Errorf("unsupported REPL backend %q", backend)
	}
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s was not found in PATH", name)
	}
	return nil
}

func canonicalWorkDir(workDir string) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return "", fmt.Errorf("workDir cannot be empty")
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve workDir: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve workDir symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat workDir: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workDir is not a directory")
	}
	return resolved, nil
}

func sandboxProfile(workDir, runtimeDir string, pythonPaths ...string) string {
	// Python on macOS needs broad access to system frameworks and its dynamic
	// runtime, whose exact locations vary between Apple, Xcode and Homebrew
	// builds. Start from read-only system access, then carve out user/temp/mount
	// roots and re-allow reads only from this workspace + ephemeral runtime. The
	// parent publishes inventory/Git snapshots there; Python never needs write
	// authority. Unlike an
	// `(allow default)` profile this grants no Mach IPC, network, or unrelated
	// operation classes; arbitrary subprocesses inherit the same boundaries.
	sensitiveRoots := []string{
		"/Users", "/Volumes", "/private/tmp", "/tmp",
		"/private/var/folders", "/var/folders",
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		sensitiveRoots = append(sensitiveRoots, home)
	}
	var deniedReads strings.Builder
	for _, path := range sensitiveRoots {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		fmt.Fprintf(&deniedReads, " (subpath %s)", sandboxString(path))
	}
	var execRule strings.Builder
	if len(pythonPaths) > 0 {
		execRule.WriteString("(allow process-exec")
		for _, path := range pythonPaths {
			fmt.Fprintf(&execRule, " (literal %s)", sandboxString(path))
		}
		execRule.WriteString(")\n")
	}
	return fmt.Sprintf(`(version 1)
(deny default)
%s(allow signal (target self))
(allow sysctl-read)
(allow file-read-metadata)
(allow file-read*)
(deny file-read*%s)
(allow file-read* (subpath %s) (subpath %s) (literal "/dev/null") (literal "/dev/urandom"))
(allow file-write* (literal "/dev/null"))
(deny network*)
`, execRule.String(), deniedReads.String(), sandboxString(workDir), sandboxString(runtimeDir))
}

func sandboxString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func buildWorkerCommand(ctx context.Context, opts Options, runtimeDir, workerPath string, generation uint64) (*exec.Cmd, error) {
	args := []string{
		"-I", "-u", workerPath, opts.WorkDir, fmt.Sprint(generation),
		string(opts.Backend), fmt.Sprint(opts.MaxMemoryBytes),
	}
	var cmd *exec.Cmd
	switch opts.Backend {
	case BackendSandboxExec:
		profilePath := filepath.Join(runtimeDir, "sandbox.sb")
		if err := os.WriteFile(profilePath, []byte(sandboxProfile(opts.WorkDir, runtimeDir, opts.pythonExecPaths...)), 0600); err != nil {
			return nil, fmt.Errorf("write sandbox profile: %w", err)
		}
		sandboxExec, err := exec.LookPath("sandbox-exec")
		if err != nil {
			return nil, err
		}
		cmdArgs := append([]string{"-f", profilePath, opts.PythonPath}, args...)
		cmd = exec.CommandContext(ctx, sandboxExec, cmdArgs...)
	case BackendBubblewrap:
		bwrap, err := exec.LookPath("bwrap")
		if err != nil {
			return nil, err
		}
		seccomp, err := openWorkerSeccompFilter(runtimeDir)
		if err != nil {
			return nil, fmt.Errorf("build worker seccomp filter: %w", err)
		}
		cmd = buildBubblewrapCommand(
			ctx, bwrap, opts.WorkDir, runtimeDir,
			opts.PythonPath, args, seccomp,
		)
	case BackendTest:
		cmd = exec.CommandContext(ctx, opts.PythonPath, args...)
	default:
		return nil, fmt.Errorf("%w: unsupported backend %q", ErrUnavailable, opts.Backend)
	}
	cmd.Dir = opts.WorkDir
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin:/opt/homebrew/bin",
		"HOME=" + runtimeDir,
		"TMPDIR=" + runtimeDir,
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"PYTHONIOENCODING=utf-8",
		"PYTHONDONTWRITEBYTECODE=1",
	}
	configureProcessGroup(cmd)
	return cmd, nil
}

func buildBubblewrapCommand(
	ctx context.Context,
	bwrap, workDir, runtimeDir, executable string,
	args []string,
	seccomp *os.File,
) *exec.Cmd {
	bwrapArgs := []string{
		"--die-with-parent", "--new-session", "--unshare-all",
		"--seccomp", "3",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
		// The worker does not need scratch-write authority: its bytecode cache is
		// disabled and the parent publishes immutable snapshots in runtimeDir.
		// The empty tmpfs is remounted below so native code cannot fill it after
		// bypassing Python's audit hook. It cannot be replaced with a bind of
		// runtimeDir because that directory normally resides underneath /tmp.
		"--ro-bind", workDir, workDir,
		"--ro-bind", runtimeDir, runtimeDir,
		"--chdir", workDir,
		"--setenv", "HOME", runtimeDir,
		"--setenv", "TMPDIR", "/tmp",
	}
	for _, path := range []string{"/usr", "/bin", "/lib", "/lib64", "/usr/local"} {
		if _, err := os.Stat(path); err == nil {
			bwrapArgs = append(bwrapArgs, "--ro-bind", path, path)
		}
	}
	// Bubblewrap builds a fresh tmpfs root. It is usually non-writable to the
	// invoking uid, but root-run containers are common; remount every remaining
	// synthetic filesystem read-only so the hard boundary does not depend on uid
	// ownership. A read-only /dev still permits I/O through existing device nodes
	// such as /dev/null and /dev/urandom.
	bwrapArgs = append(bwrapArgs,
		"--remount-ro", "/",
		"--remount-ro", "/proc",
		"--remount-ro", "/dev",
		"--remount-ro", "/tmp",
	)
	bwrapArgs = append(bwrapArgs, executable)
	bwrapArgs = append(bwrapArgs, args...)
	cmd := exec.CommandContext(ctx, bwrap, bwrapArgs...)
	cmd.ExtraFiles = []*os.File{seccomp}
	return cmd
}
