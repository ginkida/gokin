package security

import (
	"archive/zip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBashIsolationCachedModulesOffline(t *testing.T) {
	sandboxTestPathGo(t)
	requireSandboxExec(t)
	root := t.TempDir()
	proxy := t.TempDir()
	cache := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("GOMODCACHE", cache)
	t.Setenv("GOFLAGS", "-modcacherw")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOTOOLCHAIN", "auto")
	// Seed an already-downloaded toolchain into this synthetic module cache.
	// The driver still comes from PATH, as run_tests and foreground bash do.
	if toolchain := runtime.GOROOT(); strings.HasPrefix(filepath.Base(toolchain), "toolchain@") {
		if err := os.MkdirAll(filepath.Join(cache, "golang.org"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(toolchain, filepath.Join(cache, "golang.org", filepath.Base(toolchain))); err != nil {
			t.Fatal(err)
		}
	}
	version := strings.TrimPrefix(runtime.Version(), "go")
	module := "module example.test/cached\n\ngo 1.25\n"
	versionDir := filepath.Join(proxy, "example.test", "cached", "@v")
	if err := os.MkdirAll(versionDir, 0700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(versionDir, "v1.0.0.mod"):  module,
		filepath.Join(versionDir, "v1.0.0.info"): `{"Version":"v1.0.0","Time":"2025-01-01T00:00:00Z"}`,
		filepath.Join(root, "go.mod"):            "module example.test/main\n\ngo 1.25\n\nrequire example.test/cached v1.0.0\n",
		filepath.Join(root, "cache_test.go"):     "package fixture\nimport (\"testing\"; dep \"example.test/cached\")\nfunc TestCached(t *testing.T) { if dep.Value!=42 { t.Fatal(dep.Value) } }\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	file, err := os.Create(filepath.Join(versionDir, "v1.0.0.zip"))
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for name, content := range map[string]string{"go.mod": module, "dep.go": "package cached\nconst Value=42\n"} {
		entry, err := archive.Create("example.test/cached@v1.0.0/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	// Populate a synthetic downloaded module from a file proxy, never a network.
	setup := exec.CommandContext(t.Context(), "go", "mod", "download", "example.test/cached")
	setup.Dir = root
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GOROOT=") {
			setup.Env = append(setup.Env, entry)
		}
	}
	setup.Env = append(setup.Env, "GOPROXY=file://"+filepath.ToSlash(proxy), "GOSUMDB=off")
	if output, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("seed local module: %v %s", err, output)
	}
	// Only the sandboxed workload requires the newer, already-cached Go.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/main\n\ngo "+version+"\n\nrequire example.test/cached v1.0.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, network := range []bool{false, true} {
		config := DefaultSandboxConfig()
		config.RequireSandbox = true
		config.AllowNetwork = network
		command, err := NewSandboxedCommand(t.Context(), root, "go test ./...", config)
		if err != nil {
			t.Fatal(err)
		}
		// A fresh managed GOCACHE rebuilds the standard library on small CI VMs.
		result := command.Run(3 * time.Minute)
		if result.Error != nil || result.ExitCode != 0 {
			t.Fatalf("cached module network=%t: exit=%d error=%v stdout=%s stderr=%s", network, result.ExitCode, result.Error, result.Stdout, result.Stderr)
		}
	}
}
