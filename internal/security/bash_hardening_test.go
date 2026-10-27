package security

import (
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBashManagedCacheRejectsSymlink(t *testing.T) {
	root, err := canonicalSandboxPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", base)
	hash := sha256.Sum256([]byte(root))
	managed := filepath.Join(base, "gokin", "bash", fmt.Sprintf("%x", hash[:12]))
	if err := os.MkdirAll(filepath.Dir(managed), 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, managed); err != nil {
		t.Fatal(err)
	}
	p := NewSandboxPolicy(root, DefaultSandboxConfig())
	if _, _, err := p.state.workspaceCaches(root, true); err == nil {
		t.Fatal("managed cache accepted an escaping symlink")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cache wrote through symlink: %v %v", entries, err)
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		bin := t.TempDir()
		name := "bwrap"
		if runtime.GOOS == "darwin" {
			name = "sandbox-exec"
		}
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf gokin-bash-probe"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		cmd := exec.CommandContext(t.Context(), "bash", "-c", "true")
		cleanup, err := p.Prepare(t.Context(), cmd, CommandScope{WorkDir: root})
		if err == nil {
			cleanup()
			t.Fatal("unsafe cache downgraded optional isolation to host execution")
		}
		if p.Status(t.Context()).Network != "blocked" {
			t.Fatal("unsafe cache was advertised as a host fallback")
		}
	}
}

func TestBashMissingSecretsResolveHomeAliases(t *testing.T) {
	home, err := canonicalSandboxPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", alias)
	for _, path := range sandboxSecrets(nil) {
		if path == filepath.Join(home, ".ssh") {
			return
		}
	}
	t.Fatal("missing secret under a symlinked HOME was not protected")
}

func TestBashManagedCacheIgnoresRepositoryXDG(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprint(symlink), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", t.TempDir())
			cache := filepath.Join(root, ".cache")
			outside := t.TempDir()
			if symlink {
				if err := os.Symlink(outside, cache); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("XDG_CACHE_HOME", cache)
			p := NewSandboxPolicy(root, DefaultSandboxConfig())
			dir, _, err := p.state.workspaceCaches(root, false)
			if err != nil {
				t.Fatal(err)
			}
			if pathWithin(dir, root) {
				t.Fatalf("managed cache trusts repository path: %s", dir)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("repository cache symlink wrote outside: %v %v", entries, err)
			}
		})
	}
}

func TestBashCanonicalizesLaunchCwd(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	link := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	p := fixedSandboxPolicy(link, SandboxStatus{Status: "enforced"}, true)
	cmd := exec.CommandContext(t.Context(), "bash", "-c", "pwd")
	cmd.Dir = link
	cleanup, err := p.Prepare(t.Context(), cmd, CommandScope{WorkDir: link})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	want, err := canonicalSandboxPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Dir != want {
		t.Fatalf("logical cwd survived: got %s want %s", cmd.Dir, want)
	}
}

func TestBashGitMetadataSymlinkCannotGrantHostDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	policy := fixedSandboxPolicy(root, SandboxStatus{Status: "enforced"}, true)
	cmd := exec.CommandContext(t.Context(), "bash", "-c", "true")
	if cleanup, err := policy.Prepare(t.Context(), cmd, CommandScope{WorkDir: root}); err == nil {
		cleanup()
		t.Fatal("Git symlink became an ungranted writable host directory")
	}
}

func TestBashGoToolchainSelectionOffline(t *testing.T) {
	sandboxTestPathGo(t)
	cache := hostGoModuleCache()
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GOMODCACHE", cache)
	t.Setenv("GOTOOLCHAIN", "auto")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\ngo "+strings.TrimPrefix(runtime.Version(), "go")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	driver := exec.CommandContext(t.Context(), "go", "version")
	goPath := driver.Path
	driver.Dir = root
	policy := fixedSandboxPolicy(root, SandboxStatus{Status: "enforced"}, true)
	cleanup, err := policy.Prepare(t.Context(), driver, CommandScope{WorkDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	// Run the PATH driver with the prepared environment. Real OS-boundary
	// assertions live in the acceptance tests; this works under outer sandboxes.
	check := exec.CommandContext(t.Context(), goPath, "version")
	check.Dir = root
	check.Env = driver.Env
	output, err := check.CombinedOutput()
	if err != nil || !strings.Contains(string(output), runtime.Version()) {
		t.Fatalf("PATH driver could not select cached toolchain offline: %v %s", err, output)
	}
	t.Logf("PATH driver %s selected %s", goPath, strings.TrimSpace(string(output)))
	check = exec.CommandContext(t.Context(), goPath, "version")
	check.Dir = root
	check.Env = append(driver.Env, "GOTOOLCHAIN=local")
	if output, err := check.CombinedOutput(); err == nil && !strings.Contains(string(output), runtime.Version()) {
		t.Logf("negative control: forcing local selects %s", strings.TrimSpace(string(output)))
	}
}

// go test may prepend its own toolchain to PATH. Acceptance runners can pin
// the original shell driver to exercise cached upgrades from an older Go.
func sandboxTestPathGo(t *testing.T) {
	t.Helper()
	if path := os.Getenv("GOKIN_TEST_PATH_GO"); path != "" {
		if !filepath.IsAbs(path) {
			t.Fatal("GOKIN_TEST_PATH_GO must be absolute")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", filepath.Dir(path)+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
}

func TestBashIsolationGitMetadata(t *testing.T) {
	requireSandboxExec(t)
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	config := DefaultSandboxConfig()
	config.RequireSandbox = true
	for _, command := range []string{
		"printf '\n[core]\n fsmonitor = evil\n' >> .git/config",
		"printf evil > .git/hooks/pre-commit",
		"mv .git/config .git/config.old",
		"mv .git/hooks .git/hooks.old",
		"printf evil > .git/info/attributes",
		"mv .git .git.old",
	} {
		cmd, err := NewSandboxedCommand(t.Context(), root, command, config)
		if err != nil {
			t.Fatal(err)
		}
		result := cmd.Run(10 * time.Second)
		if result.Error != nil || result.ExitCode == 0 {
			t.Fatalf("Git metadata writable: %s: %+v", command, result)
		}
	}
	cmd, err := NewSandboxedCommand(t.Context(), root, "printf safe > file; git add file && git -c user.name=Fixture -c user.email=fixture@example.test commit -m fixture && printf changed >> file && git stash", config)
	if err != nil {
		t.Fatal(err)
	}
	if result := cmd.Run(20 * time.Second); result.Error != nil || result.ExitCode != 0 {
		t.Fatalf("Git data writes blocked: %+v stdout=%s stderr=%s", result, result.Stdout, result.Stderr)
	}
}

func TestBashIsolationWorktreeMetadata(t *testing.T) {
	requireSandboxExec(t)
	main, work := t.TempDir(), filepath.Join(t.TempDir(), "worker")
	t.Setenv("HOME", t.TempDir())
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("Git fixture: %v %s", err, out)
		}
	}
	run("init", main)
	run("-C", main, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--allow-empty", "-m", "initial")
	run("-C", main, "worktree", "add", "--detach", work, "HEAD")
	config := DefaultSandboxConfig()
	config.RequireSandbox = true
	cmd, err := NewSandboxedCommand(t.Context(), work, "printf evil >> '"+filepath.Join(main, ".git", "config")+"'", config)
	if err != nil {
		t.Fatal(err)
	}
	if result := cmd.Run(10 * time.Second); result.Error != nil || result.ExitCode == 0 {
		t.Fatalf("external worktree config writable: %+v", result)
	}
	cmd, err = NewSandboxedCommand(t.Context(), work, "printf safe > file; git add file && git -c user.name=Fixture -c user.email=fixture@example.test commit -m worker && printf changed >> file && git stash", config)
	if err != nil {
		t.Fatal(err)
	}
	if result := cmd.Run(20 * time.Second); result.Error != nil || result.ExitCode != 0 {
		t.Fatalf("worktree Git data blocked: %+v stdout=%s stderr=%s", result, result.Stdout, result.Stderr)
	}
}

func TestBashIsolationMissingSecrets(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux mountpoint side effects")
	}
	requireSandboxExec(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	config := DefaultSandboxConfig()
	config.RequireSandbox = true
	config.Scope = CommandScope{WorkDir: root, AllowedDirs: []string{home}}
	cmd, err := NewSandboxedCommand(t.Context(), root, "mkdir -p '"+home+"/.ssh'; printf escaped > '"+home+"/.ssh/authorized_keys'", config)
	if err == nil {
		result := cmd.Run(10 * time.Second)
		if result.ExitCode == 0 {
			t.Fatal("missing credential path became writable")
		}
	} else if !strings.Contains(err.Error(), "narrower") {
		t.Fatalf("missing actionable grant error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh")); !os.IsNotExist(err) {
		t.Fatalf("secret masking changed host: %v", err)
	}
}

func TestBashIsolationUnixSockets(t *testing.T) {
	requireSandboxExec(t)
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	address := filepath.Join(root, "host.sock")
	listener, err := net.Listen("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "probe.test"), data, 0700); err != nil {
		t.Fatal(err)
	}
	for _, network := range []bool{false, true} {
		config := DefaultSandboxConfig()
		config.RequireSandbox = true
		config.AllowNetwork = network
		config.Environment = []string{"GOKIN_UNIX_SOCKET_HELPER=" + address, "GOKIN_UNIX_SOCKET_ALLOWED=" + fmt.Sprint(network)}
		cmd, err := NewSandboxedCommand(t.Context(), root, "./probe.test -test.run '^TestBashIsolationUnixSocketHelper$'", config)
		if err != nil {
			t.Fatal(err)
		}
		if result := cmd.Run(10 * time.Second); result.Error != nil || result.ExitCode != 0 {
			t.Fatalf("unix network=%v: %+v stdout=%s stderr=%s", network, result, result.Stdout, result.Stderr)
		}
	}
	config := DefaultSandboxConfig()
	config.RequireSandbox = true
	config.Environment = []string{"GOKIN_BASH_IPC_HELPER=1"}
	cmd, err := NewSandboxedCommand(t.Context(), root, "./probe.test -test.run '^TestBashIsolationIPCHelper$'", config)
	if err != nil {
		t.Fatal(err)
	}
	if result := cmd.Run(10 * time.Second); result.Error != nil || result.ExitCode != 0 {
		t.Fatalf("socketpair IPC failed: %+v %s", result, result.Stderr)
	}
}

func TestBashIsolationUnixSocketHelper(t *testing.T) {
	address := os.Getenv("GOKIN_UNIX_SOCKET_HELPER")
	if address == "" {
		return
	}
	connection, err := net.DialTimeout("unix", address, time.Second)
	if connection != nil {
		connection.Close()
	}
	if (err == nil) != (os.Getenv("GOKIN_UNIX_SOCKET_ALLOWED") == "true") {
		t.Fatalf("unexpected Unix socket result: %v", err)
	}
}
