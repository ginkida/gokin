package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrettyPath_HomeCollapsed(t *testing.T) {
	home, _ := os.UserHomeDir()
	if home == "" {
		t.Skip("no home dir on this platform")
	}

	// Inside home — collapses.
	if got := prettyPath(filepath.Join(home, "github", "gokin")); got != "~/github/gokin" {
		t.Errorf("inside-home: got %q, want ~/github/gokin", got)
	}
	// Exactly home.
	if got := prettyPath(home); got != "~" {
		t.Errorf("exact-home: got %q, want ~", got)
	}
}

func TestPrettyPath_OutsideHomeUnchanged(t *testing.T) {
	if got := prettyPath("/etc/hosts"); got != "/etc/hosts" {
		t.Errorf("got %q, want /etc/hosts", got)
	}
}

func TestPrettyPath_EmptyReturnsDot(t *testing.T) {
	if got := prettyPath(""); got != "." {
		t.Errorf("got %q, want .", got)
	}
}

// Guards against rewriting "/Users/ginkidaX" → "~X" when the real home is
// "/Users/ginkida". Must only collapse when the char after $HOME is a
// path separator (or end-of-string).
func TestPrettyPath_PrefixBoundary(t *testing.T) {
	home, _ := os.UserHomeDir()
	if home == "" {
		t.Skip("no home dir on this platform")
	}
	// Construct a sibling dir that shares the prefix but is not under home.
	// e.g. home=/Users/ginkida → test /Users/ginkidaX/foo
	sibling := home + "X/foo"
	if got := prettyPath(sibling); got != sibling {
		t.Errorf("prefix-boundary bug: got %q, want %q", got, sibling)
	}
}

func TestStatusBarProjectPath_CollapsesHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	if home == "" {
		t.Skip("no home dir on this platform")
	}
	got := statusBarProjectPath(filepath.Join(home, "github", "gokin"))
	if got != "~/github/gokin" {
		t.Errorf("got %q, want ~/github/gokin", got)
	}
}

func TestStatusBarProjectPath_EmptyOrDotSkipped(t *testing.T) {
	if got := statusBarProjectPath(""); got != "" {
		t.Errorf("empty: got %q, want empty", got)
	}
}

func TestStatusBarProjectPath_LongPathShortened(t *testing.T) {
	long := "/Users/ginkida/work/clients/acme/infra/services/api/internal/handlers"
	got := statusBarProjectPath(long)
	if len([]rune(got)) > 36 {
		t.Errorf("expected ≤36 runes, got %d: %q", len([]rune(got)), got)
	}
	// Filename must survive truncation.
	if !strings.HasSuffix(got, "handlers") {
		t.Errorf("filename lost: %q", got)
	}
}

// Integration-ish: baseStatusSegments must include the project path as
// first segment (dim-styled) when workDir is set.
func TestBaseStatusSegments_IncludesProjectPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	if home == "" {
		t.Skip("no home dir on this platform")
	}
	m := NewModel()
	m.workDir = filepath.Join(home, "github", "gokin")
	m.permissionsEnabled = true
	m.sandboxEnabled = true

	parts := m.baseStatusSegments(true)
	if len(parts) == 0 {
		t.Fatal("empty segments")
	}
	// Strip ANSI from the first segment to compare.
	first := stripAnsi(parts[0])
	if first != "~/github/gokin" {
		t.Errorf("first segment = %q, want ~/github/gokin", first)
	}
}

func TestBaseStatusSegments_NoWorkDirSkipsSegment(t *testing.T) {
	m := NewModel()
	m.workDir = ""
	m.permissionsEnabled = true
	m.sandboxEnabled = true

	parts := m.baseStatusSegments(true)
	// The first segment (if any) must not be the dot placeholder — we
	// intentionally return "" from statusBarProjectPath for empty workDir.
	for _, p := range parts {
		if stripAnsi(p) == "." {
			t.Errorf("should not show '.' placeholder, got segments: %v", parts)
		}
	}
}
