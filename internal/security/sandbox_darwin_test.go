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
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
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
