package security

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func resolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", p, err)
	}
	return r
}

func TestIsPathWithinAny(t *testing.T) {
	base := resolved(t, t.TempDir())
	other := resolved(t, t.TempDir())
	sub := filepath.Join(base, "pkg")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		target string
		dirs   []string
		want   bool
	}{
		{"inside", filepath.Join(sub, "f.go"), []string{base}, true},
		{"exact dir", base, []string{base}, true},
		{"outside", filepath.Join(other, "f.go"), []string{base}, false},
		{"traversal escape", filepath.Join(base, "..", "escape.go"), []string{base}, false},
		{"new file under allowed", filepath.Join(sub, "does-not-exist-yet.go"), []string{base}, true},
		{"within any of several", filepath.Join(other, "f.go"), []string{base, other}, true},
		{"empty dirs means unrestricted", filepath.Join(other, "f.go"), nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IsPathWithinAny(tc.target, tc.dirs)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != tc.want {
				t.Errorf("IsPathWithinAny(%s, %v) = %v, want %v", tc.target, tc.dirs, got, tc.want)
			}
		})
	}
}

func TestIsPathWithinAnyRejectsBadTarget(t *testing.T) {
	if _, err := IsPathWithinAny("", []string{"/tmp"}); err == nil {
		t.Error("empty target should error")
	}
	if _, err := IsPathWithinAny("a\x00b", []string{"/tmp"}); err == nil {
		t.Error("null-byte target should error")
	}
}

func TestIsGrantableDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix path assumptions")
	}
	ok := resolved(t, t.TempDir())

	// A normal temp dir is grantable.
	if err := IsGrantableDir(ok); err != nil {
		t.Errorf("normal dir should be grantable: %v", err)
	}

	// Filesystem root and system dirs are refused.
	for _, bad := range []string{"/", "/etc", "/etc/ssl", "/sys", "/proc", "/dev"} {
		if err := IsGrantableDir(bad); err == nil {
			t.Errorf("expected %s to be non-grantable", bad)
		}
	}

	// A .git directory is refused.
	gitDir := filepath.Join(ok, ".git")
	if err := os.MkdirAll(gitDir, 0755); err == nil {
		if err := IsGrantableDir(gitDir); err == nil {
			t.Error("a .git dir must be non-grantable")
		}
	}

	// Secret home dirs are refused; HOME itself stays grantable.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if err := IsGrantableDir(filepath.Join(home, ".ssh")); err == nil {
			t.Error("~/.ssh must be non-grantable")
		}
		if err := IsGrantableDir(filepath.Join(home, ".aws", "x")); err == nil {
			t.Error("inside ~/.aws must be non-grantable")
		}
		// HOME itself is allowed (Claude-Code parity) — only its secret subdirs are blocked.
		if err := IsGrantableDir(home); err != nil {
			t.Errorf("HOME should be grantable (only secret subdirs blocked): %v", err)
		}
	}
}
