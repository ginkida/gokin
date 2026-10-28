//go:build darwin

package security

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestBashSandboxProfileAllowsDyldRootRead(t *testing.T) {
	profile := bashSandboxProfile(nil, nil, false)
	if !strings.Contains(profile, `(allow file-read* (literal "/"))`) {
		t.Fatal("dyld requires read access to the root directory itself")
	}
}

func TestBashSandboxProfileAllowsRuntimeToolchainReadOnly(t *testing.T) {
	root := runtime.GOROOT()
	// Redirecting the module cache must not hide an auto-downloaded toolchain
	// from the original module cache that is running this process.
	t.Setenv("GOMODCACHE", t.TempDir())
	profile := bashSandboxProfile(nil, nil, false)
	if !strings.Contains(profile, "(allow file-read* (subpath "+strconv.Quote(root)+"))") {
		t.Fatalf("runtime toolchain is not readable: %s", root)
	}
	if strings.Contains(profile, "(allow file-write* (subpath "+strconv.Quote(root)+"))") {
		t.Fatal("runtime toolchain must remain read-only")
	}
}

func TestBashSandboxProfileScopesLoopbackByDirection(t *testing.T) {
	profile := bashSandboxProfile(nil, nil, false)
	for _, rule := range []string{
		`(deny network-outbound (remote unix-socket))`,
		`(allow network-outbound (remote ip "localhost:*"))`,
		`(allow network-inbound (local ip "localhost:*"))`,
		`(allow network-bind (local ip "localhost:*"))`,
	} {
		if !strings.Contains(profile, rule) {
			t.Fatalf("loopback rule missing: %s", rule)
		}
	}
}

func TestBashSandboxDarwinStatusReportsHostLocalAccess(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	bin := t.TempDir()
	// Exercise status construction without relying on this test runner's
	// sandbox-exec capability. Boundary tests verify real OS enforcement.
	if err := os.WriteFile(filepath.Join(bin, "sandbox-exec"), []byte("#!/bin/sh\nprintf gokin-bash-probe\n"), 0700); err != nil {
		t.Fatal(err)
	}
	useFakeSandboxLauncher(t, filepath.Join(bin, "sandbox-exec"))
	for _, network := range []bool{false, true} {
		t.Run(map[bool]string{false: "host_local", true: "allow_network"}[network], func(t *testing.T) {
			config := DefaultSandboxConfig()
			config.AllowNetwork = network
			status := NewSandboxPolicy(root, config).Status(t.Context())
			if status.Status != "enforced" {
				t.Fatalf("fake launcher probe failed: %+v", status)
			}
			if network {
				if status.Network != "host" || status.Reason != "" {
					t.Fatalf("network opt-in advertised restrictions: %+v", status)
				}
			} else if status.Network != "loopback-host" || status.Reason != "sandbox-exec allows connections to all addresses of this host (loopback and local interfaces); other hosts are blocked" {
				t.Fatalf("host-local boundary was misreported: %+v", status)
			}
		})
	}
}

// The launcher must come from the system location, not the caller's PATH: a
// stripped PATH used to fall back to UNSANDBOXED bash, and an earlier PATH
// entry could substitute its own sandbox-exec.
func TestBashSandboxDarwinLauncherIgnoresPATH(t *testing.T) {
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("system sandbox-exec missing")
	}
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "hijacked")
	fake := "#!/bin/sh\ntouch " + marker + "\nprintf gokin-bash-probe\n"
	if err := os.WriteFile(filepath.Join(bin, "sandbox-exec"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	if got := sandboxLauncherPath("sandbox-exec", darwinSandboxLaunchers, false); got != "/usr/bin/sandbox-exec" {
		t.Fatalf("launcher = %q, want /usr/bin/sandbox-exec", got)
	}
	t.Setenv("PATH", bin)
	if got := sandboxLauncherPath("sandbox-exec", darwinSandboxLaunchers, false); got != "/usr/bin/sandbox-exec" {
		t.Fatalf("PATH-shadowing launcher selected: %q", got)
	}
	t.Setenv("PATH", "")
	if got := sandboxLauncherPath("sandbox-exec", darwinSandboxLaunchers, false); got != "/usr/bin/sandbox-exec" {
		t.Fatalf("empty PATH lost the system launcher: %q", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("fake launcher on PATH was executed")
	}
}
