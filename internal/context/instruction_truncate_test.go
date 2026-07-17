package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

// v0.100.101 field report: a CLAUDE.md a few KB over maxInstructionFileBytes
// was SKIPPED ENTIRELY ("skipping unsafe or invalid instruction file"), so the
// agent silently ran with ZERO project instructions. An oversized instruction
// file must degrade gracefully: keep the head (rune-safe) + disclose the cut.
func TestReadInstructionFile_OversizedTruncatesInsteadOfSkipping(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	path := filepath.Join(dir, "CLAUDE.md")

	head := "# Project rules\nRule one is load-bearing.\n"
	// Pad well past the limit; end with a multi-byte rune near the boundary so
	// the rune-safe cut is exercised.
	pad := strings.Repeat("я", maxInstructionFileBytes)
	if err := os.WriteFile(path, []byte(head+pad), 0o644); err != nil {
		t.Fatal(err)
	}

	content, resolved, err := readInstructionFile(path, dir, true)
	if err != nil {
		t.Fatalf("readInstructionFile() error = %v, want truncated content (not a skip)", err)
	}
	if resolved == "" {
		t.Fatal("resolved path is empty")
	}
	if !strings.HasPrefix(content, head) {
		t.Fatalf("content must keep the file head, got prefix %q", content[:60])
	}
	if !strings.Contains(content, "was truncated") {
		t.Fatal("truncation must be disclosed in the content")
	}
	if len(content) > maxInstructionFileBytes+400 {
		t.Fatalf("content length %d exceeds limit+note headroom", len(content))
	}
	// Rune-safe: the whole content must remain valid UTF-8.
	if strings.ToValidUTF8(content, "") != content {
		t.Fatal("truncated content is not valid UTF-8")
	}
}

// An in-limit file loads verbatim with no truncation note.
func TestReadInstructionFile_InLimitLoadsVerbatim(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	path := filepath.Join(dir, "CLAUDE.md")
	body := "# Rules\nshort file\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	content, _, err := readInstructionFile(path, dir, true)
	if err != nil {
		t.Fatalf("readInstructionFile() error = %v", err)
	}
	if content != body {
		t.Fatalf("in-limit file must load verbatim, got %q", content)
	}
}
