//go:build unix

package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func installStartupGitFixture(t *testing.T, script string) string {
	t.Helper()
	root := t.TempDir()
	logPath := filepath.Join(root, "calls")
	script = "#!/bin/sh\nprintf 'probe\\n' >> \"$GOKIN_TEST_GIT_LOG\"\n" + script
	if err := os.WriteFile(filepath.Join(root, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	t.Setenv("GOKIN_TEST_GIT_LOG", logPath)
	return logPath
}

func startupGitCallCount(t *testing.T, logPath string) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "probe\n")
}

func TestStartupGitProbeRunsOnceForConcurrentCallers(t *testing.T) {
	logPath := installStartupGitFixture(t, "/bin/sleep 0.02\nprintf '.git\\n'\n")
	root := t.TempDir()
	memo := &StartupProbes{}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			<-start
			out, err := memo.output(root, "rev-parse", "--git-dir")
			if err != nil || string(out) != ".git\n" {
				t.Errorf("probe = %q, %v", out, err)
			}
		})
	}
	close(start)
	wg.Wait()
	if calls := startupGitCallCount(t, logPath); calls != 1 {
		t.Errorf("git executed %d times for 32 callers, want 1", calls)
	}
	// Cached output must not be shared mutable storage.
	out, err := memo.output(root, "rev-parse", "--git-dir")
	if err != nil || len(out) == 0 {
		t.Fatalf("initial output = %q, %v", out, err)
	}
	out[0] = 'x'
	out, err = memo.output(root+"/.", "rev-parse", "--git-dir")
	if err != nil || string(out) != ".git\n" || startupGitCallCount(t, logPath) != 1 {
		t.Fatalf("cached probe = %q, %v; calls = %d", out, err, startupGitCallCount(t, logPath))
	}
}

func TestStartupGitProbeCacheKeysWorkdirAndArguments(t *testing.T) {
	logPath := installStartupGitFixture(t, "printf '.git\\n'\n")
	a, b := t.TempDir(), t.TempDir()
	memo := &StartupProbes{}
	for range 2 {
		for _, probe := range []struct {
			dir  string
			args []string
		}{
			{a, []string{"rev-parse", "--git-dir"}},
			{b, []string{"rev-parse", "--git-dir"}},
			{a, []string{"a", "b"}},
			{a, []string{"a b"}},
		} {
			if _, err := memo.output(probe.dir, probe.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	if calls := startupGitCallCount(t, logPath); calls != 4 {
		t.Fatalf("git executed %d times, want 4 distinct probes", calls)
	}
}

func TestStartupGitProbeFailureDoesNotAuthorizeNonRepository(t *testing.T) {
	logPath := installStartupGitFixture(t, "echo 'fatal: permission denied' >&2\nexit 128\n")
	root := t.TempDir()
	memo := &StartupProbes{}
	for range 2 {
		if memo.IsGitRepo(root) {
			t.Fatal("failed optional probe reported a repository")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if repo, err := CheckGitRepo(ctx, root); repo || err == nil {
		t.Fatalf("failed authority probe must stay fail-closed: repo=%v, err=%v", repo, err)
	}
	if calls := startupGitCallCount(t, logPath); calls != 2 {
		t.Fatalf("got %d probes, want 1 cached optional + 1 fresh authority probe", calls)
	}
}

func TestStartupGitProbeMemoPreservesTimeout(t *testing.T) {
	logPath := installStartupGitFixture(t, "exec /bin/sleep 30\n")
	root := t.TempDir()
	memo := &StartupProbes{}
	for range 2 {
		if _, err := memo.output(root, "rev-parse", "--git-dir"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("startup timeout = %v, want deadline exceeded", err)
		}
	}
	if calls := startupGitCallCount(t, logPath); calls != 1 {
		t.Fatalf("timed-out probe ran %d times in one batch, want 1", calls)
	}
}

// Each iteration owns a fresh memo so it measures a cold builder's seven
// optional helper calls, including duplicates, rather than a warm cache hit.
func BenchmarkStartupGitProbes(b *testing.B) {
	root, err := filepath.Abs("../..")
	if err != nil {
		b.Fatal(err)
	}
	for range b.N {
		b.StopTimer()
		workDir := filepath.Join(b.TempDir(), "repo")
		if err := os.Symlink(root, workDir); err != nil {
			b.Fatal(err)
		}
		memo := &StartupProbes{}
		b.StartTimer()
		if !memo.IsGitRepo(workDir) || memo.DetectWorktree(workDir) == nil {
			b.Fatal("benchmark requires the source checkout's git repository")
		}
		memo.GetMainWorktreeRoot(workDir)
		memo.GetCurrentBranch(workDir)
	}
}
