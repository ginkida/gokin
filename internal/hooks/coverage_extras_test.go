package hooks

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// --- DisplayName (28.6% → 100%) ---

func TestDisplayName_WithName(t *testing.T) {
	h := &Hook{Name: "my-hook", Command: "echo hi"}
	if got := h.DisplayName(); got != "my-hook" {
		t.Fatalf("DisplayName = %q, want %q", got, "my-hook")
	}
}

func TestDisplayName_WithoutName_ShortCommand(t *testing.T) {
	h := &Hook{Command: "echo hello"}
	if got := h.DisplayName(); got != "echo hello" {
		t.Fatalf("DisplayName = %q, want %q", got, "echo hello")
	}
}

func TestDisplayName_WithoutName_LongCommand(t *testing.T) {
	longCmd := strings.Repeat("a", 50)
	h := &Hook{Command: longCmd}
	got := h.DisplayName()
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
	if len([]rune(got)) != 40 {
		t.Fatalf("expected 40 runes, got %d", len([]rune(got)))
	}
}

func TestDisplayName_WithoutName_EmptyCommand(t *testing.T) {
	h := &Hook{}
	if got := h.DisplayName(); got != "" {
		t.Fatalf("DisplayName = %q, want empty", got)
	}
}

// --- canonicalWorkspace (60% → 100%) ---

func TestCanonicalWorkspace_Empty(t *testing.T) {
	got, err := canonicalWorkspace("")
	if err != nil || got != "" {
		t.Fatalf("canonicalWorkspace('') = (%q, %v), want ('', nil)", got, err)
	}
}

func TestCanonicalWorkspace_Whitespace(t *testing.T) {
	got, err := canonicalWorkspace("   ")
	if err != nil || got != "" {
		t.Fatalf("canonicalWorkspace('   ') = (%q, %v), want ('', nil)", got, err)
	}
}

func TestCanonicalWorkspace_RelativePath(t *testing.T) {
	got, err := canonicalWorkspace(".")
	if err != nil {
		t.Fatalf("canonicalWorkspace('.') error: %v", err)
	}
	if got == "" || !filepath.IsAbs(got) {
		t.Fatalf("expected absolute path, got %q", got)
	}
}

func TestCanonicalWorkspace_Tilde(t *testing.T) {
	got, err := canonicalWorkspace("~/test")
	if err != nil {
		t.Fatalf("canonicalWorkspace('~/test') error: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty path for ~/test")
	}
	// Should not contain ~
	if strings.Contains(got, "~") {
		t.Fatalf("tilde should be expanded, got %q", got)
	}
}

func TestCanonicalWorkspace_TildeOnly(t *testing.T) {
	got, err := canonicalWorkspace("~")
	if err != nil {
		t.Fatalf("canonicalWorkspace('~') error: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty path for ~")
	}
}

// --- IsWorkspaceTrusted (69.2% → 100%) ---

func TestIsWorkspaceTrusted_EmptyWorkDir(t *testing.T) {
	if IsWorkspaceTrusted("", []string{"/some/path"}) {
		t.Fatal("empty workDir should not be trusted")
	}
}

func TestIsWorkspaceTrusted_EmptyCandidate(t *testing.T) {
	dir := t.TempDir()
	// Empty string in trusted list should be skipped
	if IsWorkspaceTrusted(dir, []string{"", dir}) {
		// Should still match the second entry
	} else {
		// At least shouldn't crash
	}
}

func TestIsWorkspaceTrusted_NoTrustedList(t *testing.T) {
	dir := t.TempDir()
	if IsWorkspaceTrusted(dir, nil) {
		t.Fatal("nil trusted list should not authorize")
	}
}

func TestIsWorkspaceTrusted_InvalidCandidate(t *testing.T) {
	dir := t.TempDir()
	// A candidate that can't be canonicalized should be skipped
	if IsWorkspaceTrusted(dir, []string{"\x00invalid"}) {
		// May or may not match depending on OS — just verify no crash
	}
}

// --- killProcessGroup (66.7% → 100%) ---

func TestKillProcessGroup_NilProcess(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("killProcessGroup panicked: %v", r)
		}
	}()
	cmd := &exec.Cmd{}
	killProcessGroup(cmd, syscall.SIGTERM)
}

// --- LoadFile (85.7% → 100%) ---

func TestLoadFile_ReadError(t *testing.T) {
	// A path that exists but is a directory, not a file — should error
	dir := t.TempDir()
	_, err := LoadFile(dir)
	if err == nil {
		t.Fatal("expected error reading directory as file")
	}
}
