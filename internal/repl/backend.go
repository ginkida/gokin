package repl

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"gokin/internal/git"
)

// Detect probes secure backends instead of trusting executable presence. Some
// managed environments ship sandbox-exec/bwrap but prohibit their use; auto
// mode must quietly fall back rather than advertise a runtime that fails on the
// first cell.
func Detect(ctx context.Context, workDir string) Availability {
	root, err := canonicalWorkDir(workDir)
	if err != nil {
		return Availability{Reason: err.Error()}
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		return Availability{Reason: "python3 was not found in PATH"}
	}

	var candidates []Backend
	switch runtime.GOOS {
	case "darwin":
		candidates = []Backend{BackendSandboxExec}
	case "linux":
		candidates = []Backend{BackendBubblewrap}
	default:
		return Availability{PythonPath: python, Reason: "no supported sandbox backend for " + runtime.GOOS}
	}

	reasons := make([]string, 0, len(candidates))
	for _, backend := range candidates {
		if err := backendExecutableAvailable(backend); err != nil {
			reasons = append(reasons, err.Error())
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		manager, err := newManager(Options{
			WorkDir: root, PythonPath: python, Backend: backend,
			CellTimeout: 3 * time.Second,
		}, false)
		if err == nil {
			_, err = manager.Execute(probeCtx, "1 + 1")
			_ = manager.Close()
		}
		cancel()
		if err == nil {
			return Availability{Available: true, PythonPath: python, Backend: backend}
		}
		reasons = append(reasons, fmt.Sprintf("%s probe failed: %v", backend, err))
	}
	return Availability{PythonPath: python, Reason: strings.Join(reasons, "; ")}
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

func sandboxProfile(workDir, runtimeDir string) string {
	// Python on macOS needs broad access to system frameworks and its dynamic
	// runtime, whose exact locations vary between Apple, Xcode and Homebrew
	// builds. Start from read-only system access, then carve out user/temp/mount
	// roots and re-allow only this workspace + this ephemeral runtime. Unlike an
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
	return fmt.Sprintf(`(version 1)
(deny default)
(allow process*)
(allow signal (target self))
(allow sysctl-read)
(allow file-read-metadata)
(allow file-read*)
(deny file-read*%s)
(allow file-read* (subpath %s) (subpath %s) (literal "/dev/null") (literal "/dev/urandom"))
(allow file-write* (subpath %s) (literal "/dev/null"))
(deny network*)
`, deniedReads.String(), sandboxString(workDir), sandboxString(runtimeDir), sandboxString(runtimeDir))
}

func sandboxString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

// ignoredTopLevelDirs returns workspace-relative top-level directories that
// .gitignore excludes. It reuses the repository's own matcher rather than
// re-deriving ignore semantics in Python, and stays TOP-LEVEL on purpose: the
// hazard is large vendored/build trees, one shallow readdir keeps worker
// startup cheap, and a narrow rule cannot accidentally hide source the user
// wanted analysed. Failure to read .gitignore is not fatal — the walker simply
// keeps its previous behaviour.
func ignoredTopLevelDirs(workDir string) []string {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return nil
	}
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return nil
	}
	matcher := git.NewGitIgnore(workDir)
	if err := matcher.Load(); err != nil {
		return nil
	}
	var ignored []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if matcher.IsIgnored(filepath.Join(workDir, name)) {
			ignored = append(ignored, name)
		}
	}
	sort.Strings(ignored)
	return ignored
}

func buildWorkerCommand(ctx context.Context, opts Options, runtimeDir, workerPath string, generation uint64) (*exec.Cmd, error) {
	args := []string{"-I", "-u", workerPath, opts.WorkDir, fmt.Sprint(generation), opts.GitPath, string(opts.Backend)}
	var cmd *exec.Cmd
	switch opts.Backend {
	case BackendSandboxExec:
		profilePath := filepath.Join(runtimeDir, "sandbox.sb")
		if err := os.WriteFile(profilePath, []byte(sandboxProfile(opts.WorkDir, runtimeDir)), 0600); err != nil {
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
		bwrapArgs := []string{
			"--die-with-parent", "--new-session", "--unshare-all",
			"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
			"--ro-bind", opts.WorkDir, opts.WorkDir,
			"--bind", runtimeDir, runtimeDir,
			"--chdir", opts.WorkDir,
			"--setenv", "HOME", runtimeDir,
			"--setenv", "TMPDIR", "/tmp",
		}
		for _, path := range []string{"/usr", "/bin", "/lib", "/lib64", "/usr/local"} {
			if _, err := os.Stat(path); err == nil {
				bwrapArgs = append(bwrapArgs, "--ro-bind", path, path)
			}
		}
		bwrapArgs = append(bwrapArgs, opts.PythonPath)
		bwrapArgs = append(bwrapArgs, args...)
		cmd = exec.CommandContext(ctx, bwrap, bwrapArgs...)
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
	// Analysis that silently includes ignored trees produces confidently wrong
	// answers: in this very repository a gitignored 266 MB Go toolchain cache
	// made a "which packages have the most TODOs" ranking return the standard
	// library — reported with truncated=False, so nothing in the answer hinted
	// at it. Grep's noise is visible in the transcript; the walker's is not.
	if ignored := ignoredTopLevelDirs(opts.WorkDir); len(ignored) > 0 {
		cmd.Env = append(cmd.Env, "GOKIN_REPL_IGNORE_DIRS="+strings.Join(ignored, string(os.PathListSeparator)))
	}
	configureProcessGroup(cmd)
	return cmd, nil
}
