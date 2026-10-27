package security

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fixedSandboxPolicy(root string, status SandboxStatus, required bool) *SandboxPolicy {
	config := DefaultSandboxConfig()
	config.RequireSandbox = required
	p := NewSandboxPolicy(root, config)
	p.state.once.Do(func() { p.state.status = status })
	return p
}

func TestBashSandboxUnavailableDecision(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(map[bool]string{false: "optional", true: "required"}[required], func(t *testing.T) {
			root := t.TempDir()
			p := fixedSandboxPolicy(root, SandboxStatus{Status: "unavailable", Backend: "test", Network: "host", Reason: "user namespaces disabled"}, required)
			var warnings bytes.Buffer
			p.Warn(t.Context(), &warnings)
			p.Warn(t.Context(), &warnings)
			cmd := exec.CommandContext(t.Context(), "bash", "-c", "printf fallback")
			cleanup, err := p.Prepare(t.Context(), cmd, CommandScope{WorkDir: root})
			if required {
				if err == nil || !strings.Contains(err.Error(), "required bash sandbox unavailable") {
					t.Fatalf("required sandbox: %v", err)
				}
				if warnings.Len() != 0 {
					t.Fatal("required sandbox advertised unsandboxed execution")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			output, err := cmd.Output()
			if err != nil || string(output) != "fallback" {
				t.Fatalf("optional execution: %s %v", output, err)
			}
			if strings.Count(warnings.String(), "UNSANDBOXED") != 1 {
				t.Fatalf("warning must fire once: %q", warnings.String())
			}
		})
	}
}

func TestBashSandboxLauncherFailureNeverFallsBack(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	root := t.TempDir()
	p := fixedSandboxPolicy(root, SandboxStatus{Status: "enforced", Backend: "test", Network: "loopback"}, false)
	p.state.launcher = filepath.Join(root, "missing-launcher")
	// Avoid any host cache writes; this test verifies the launch decision only.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	marker := filepath.Join(root, "must-not-run")
	cmd := exec.CommandContext(t.Context(), "bash", "-c", "touch '"+marker+"'")
	cmd.Dir = root
	cleanup, err := p.Prepare(t.Context(), cmd, CommandScope{WorkDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := cmd.Run(); err == nil {
		t.Fatal("missing launcher unexpectedly succeeded")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("command retried on host")
	}
	if status := p.Status(context.Background()); status.Status != "enforced" {
		t.Fatalf("launch failure changed run decision: %+v", status)
	}
}

func TestBashSandboxSnapshotPinsPolicy(t *testing.T) {
	root := t.TempDir()
	config := DefaultSandboxConfig()
	p := NewSandboxPolicy(root, config)
	snapshot := p.Snapshot()
	identity := snapshot.Identity()
	config.AllowNetwork = true
	p.Configure(root, config)
	if snapshot.Config().AllowNetwork || snapshot.Identity() != identity || p.Identity() == identity {
		t.Fatal("live update altered immutable invocation policy")
	}
}

func TestBashSandboxGoEnvironmentDoesNotMixToolchains(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	toolchain := runtime.GOROOT()
	t.Setenv("GOROOT", toolchain)
	t.Setenv("GOTOOLCHAIN", "go1.25.13+auto")
	t.Setenv("GOMODCACHE", t.TempDir())
	for _, inherit := range []bool{false, true} {
		t.Run(map[bool]string{false: "sanitized", true: "runner_environment"}[inherit], func(t *testing.T) {
			policy := fixedSandboxPolicy(root, SandboxStatus{Status: "enforced", Backend: "test", Network: "loopback"}, true)
			cmd := exec.CommandContext(t.Context(), "bash", "-c", "go test ./...")
			cmd.Dir = root
			if inherit {
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GOROOT=" + toolchain, "GOTOOLCHAIN=auto"}
			}
			cleanup, err := policy.Prepare(t.Context(), cmd, CommandScope{WorkDir: root})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			for _, entry := range cmd.Env {
				if strings.HasPrefix(entry, "GOROOT=") {
					t.Fatalf("inherited GOROOT can mismatch the Go binary on PATH: %s", entry)
				}
			}
			want := "GOTOOLCHAIN=go1.25.13+auto"
			if inherit {
				want = "GOTOOLCHAIN=auto"
			}
			if !strings.Contains(strings.Join(cmd.Env, "\n"), want) || strings.Contains(strings.Join(cmd.Env, "\n"), "GOTOOLCHAIN=local") {
				t.Fatal("sandbox changed the user's toolchain selection")
			}
			found := false
			for _, path := range sandboxSystemPaths() {
				if path == toolchain {
					found = true
				}
			}
			if !found {
				t.Fatalf("redirected module cache hid runtime toolchain %s", toolchain)
			}
		})
	}
}

func TestSandboxGitfileCannotGrantArbitraryHostDirectory(t *testing.T) {
	root := t.TempDir()
	for _, target := range []string{"/", t.TempDir()} {
		if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+target+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if paths := worktreeMetadata(root); len(paths) != 0 {
			t.Fatalf("gitfile became arbitrary host grant: %v", paths)
		}
	}
}

func TestSandboxLinkedWorktreeMetadataIsNarrow(t *testing.T) {
	root, err := canonicalSandboxPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(t.TempDir(), ".git")
	commonParent, err := canonicalSandboxPath(filepath.Dir(common))
	if err != nil {
		t.Fatal(err)
	}
	common = filepath.Join(commonParent, ".git")
	gitdir := filepath.Join(common, "worktrees", "worker")
	for _, dir := range []string{gitdir, filepath.Join(common, "objects"), filepath.Join(common, "hooks")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(root, ".git"):        "gitdir: " + gitdir + "\n",
		filepath.Join(gitdir, "commondir"): "../..\n",
		filepath.Join(gitdir, "gitdir"):    filepath.Join(root, ".git") + "\n",
		filepath.Join(common, "HEAD"):      "ref: refs/heads/main\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	paths := worktreeMetadata(root)
	if len(paths) != 3 {
		t.Fatalf("linked worktree metadata missing: %v", paths)
	}
	for _, path := range paths {
		if path == common || strings.Contains(path, "hooks") {
			t.Fatalf("overbroad external metadata grant: %s", path)
		}
	}
}
