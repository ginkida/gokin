package git

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type gitOutput func(string, ...string) ([]byte, error)

// StartupProbes memoizes bounded optional probes for one builder's startup
// batch. Runtime callers use the fresh package-level helpers instead. Its zero
// value is ready for use; a nil receiver falls back to fresh probes.
type StartupProbes struct {
	probes sync.Map
}

type startupGitProbeKey struct {
	workDir string
	args    string
}

type startupGitProbe struct {
	once   sync.Once
	output []byte
	err    error
}

func (p *StartupProbes) output(workDir string, args ...string) ([]byte, error) {
	if p == nil {
		return startupGitOutput(workDir, args...)
	}
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	encodedArgs, _ := json.Marshal(args)
	key := startupGitProbeKey{workDir: absDir, args: string(encodedArgs)}
	entry, _ := p.probes.LoadOrStore(key, &startupGitProbe{})
	probe := entry.(*startupGitProbe)
	probe.once.Do(func() {
		probe.output, probe.err = startupGitOutput(absDir, args...)
	})
	return bytes.Clone(probe.output), probe.err
}

// IsGitRepo returns the startup snapshot, never authority for isolation.
func (p *StartupProbes) IsGitRepo(workDir string) bool {
	return isGitRepo(workDir, p.output)
}

// GetCurrentBranch returns the branch observed during this startup batch.
func (p *StartupProbes) GetCurrentBranch(workDir string) string {
	return getCurrentBranch(workDir, p.output)
}

// DetectWorktree returns worktree information from this startup batch.
func (p *StartupProbes) DetectWorktree(workDir string) *WorktreeInfo {
	return detectWorktree(workDir, p.output)
}

// GetMainWorktreeRoot returns the main root observed during this startup batch.
func (p *StartupProbes) GetMainWorktreeRoot(workDir string) string {
	return getMainWorktreeRoot(workDir, p.output)
}

// Startup probes are optional context, never a reason to wait indefinitely
// before a headless invocation's own timeout starts.
func startupGitOutput(workDir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workDir
	cmd.WaitDelay = 100 * time.Millisecond
	killProbeGroupOnCancel(cmd)
	output, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return output, err
}
