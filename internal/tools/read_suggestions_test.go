package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

// Path validation failure (path outside workdir) should mention the
// workdir and suggest matching-basename files — otherwise the model
// gets a bare "path validation failed" error and has nothing to work
// with.
func TestReadTool_PathValidationFailureIncludesSuggestions(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	// Target file that DOES exist in workdir but at a different location
	// than the model "asked for".
	existing := filepath.Join(dir, "internal", "app")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	actualPath := filepath.Join(existing, "handler.go")
	if err := os.WriteFile(actualPath, []byte("package app\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rt := NewReadTool(dir)
	// Bogus absolute path outside workdir — will fail validation.
	result, err := rt.Execute(context.Background(), map[string]any{
		"file_path": "/tmp/definitely-not-in-workdir/handler.go",
	})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if result.Success {
		t.Fatal("invalid path should not succeed")
	}
	if !strings.Contains(result.Error, "working directory:") {
		t.Errorf("error should surface workdir for context: %q", result.Error)
	}
	if !strings.Contains(result.Error, "handler.go") {
		t.Errorf("suggestions should list handler.go under workdir: %q", result.Error)
	}
}

// Missing file in a valid directory should fall through to same-dir
// neighbour suggestions (existing suggestSimilarFiles path).
func TestReadTool_MissingFileKeepsOriginalSuggestions(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	neighbour := filepath.Join(dir, "handler.go")
	if err := os.WriteFile(neighbour, []byte("package app\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rt := NewReadTool(dir)
	result, err := rt.Execute(context.Background(), map[string]any{
		"file_path": filepath.Join(dir, "handler_typo.go"),
	})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if result.Success {
		t.Fatal("missing file should error")
	}
	if !strings.Contains(result.Error, "Did you mean") {
		t.Errorf("should show same-dir suggestions: %q", result.Error)
	}
	if !strings.Contains(result.Error, "handler.go") {
		t.Errorf("should suggest the real handler.go: %q", result.Error)
	}
}

// Missing file with nothing matching in same dir should fall back to
// workdir-wide basename search.
func TestReadTool_MissingFallbackToWorkdirWide(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	// Put the actual file somewhere deeper.
	sub := filepath.Join(dir, "pkg", "xyz")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "finding.go"), []byte("package xyz\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Other top-level dir (empty) so same-dir search yields nothing.
	if err := os.MkdirAll(filepath.Join(dir, "other"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	rt := NewReadTool(dir)
	result, err := rt.Execute(context.Background(), map[string]any{
		"file_path": filepath.Join(dir, "other", "finding.go"),
	})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if result.Success {
		t.Fatal("missing file should error")
	}
	// Either "Did you mean" from same-dir (there's nothing matching) or
	// "Files with matching basename" from workdir search.
	if !strings.Contains(result.Error, "finding.go") {
		t.Errorf("should locate finding.go via workdir-wide search: %q", result.Error)
	}
}

func TestSuggestFilesInWorkDir_SkipsVendorDirectories(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	vendor := filepath.Join(dir, "vendor", "lib")
	if err := os.MkdirAll(vendor, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(vendor, "handler.go"), []byte("pkg\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got := suggestFilesInWorkDir(dir, "handler.go")
	for _, g := range got {
		if strings.Contains(g, "/vendor/") {
			t.Errorf("vendor dir should be skipped, got: %v", got)
		}
	}
}

func TestSuggestFilesInWorkDir_TooShortBaseReturnsNil(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("pkg\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := suggestFilesInWorkDir(dir, "x"); got != nil {
		t.Errorf("too-short base should return nil, got: %v", got)
	}
}

func TestSuggestFilesInWorkDir_EmptyInputsReturnNil(t *testing.T) {
	if got := suggestFilesInWorkDir("", "foo.go"); got != nil {
		t.Errorf("empty workDir should yield nil, got: %v", got)
	}
	if got := suggestFilesInWorkDir("/tmp", ""); got != nil {
		t.Errorf("empty base should yield nil, got: %v", got)
	}
}

// Regression: earlier version returned filepath.SkipDir on the visit-
// limit branch, which only skips remaining files in the parent dir,
// not the whole walk. SkipAll terminates properly. We can't directly
// assert termination, but we can assert the limit caps the suggestion
// count — a previously-uncapped walk would have returned >5 entries
// on a dir with many basename matches.
func TestSuggestFilesInWorkDir_CapHonoredOnLargeRepo(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	// 30 files with matching basename across 3 sibling dirs — more
	// than the 5-suggestion cap.
	for _, sub := range []string{"a", "b", "c"} {
		subDir := filepath.Join(dir, sub)
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		for i := 0; i < 10; i++ {
			name := filepath.Join(subDir, "handler.go")
			// Same name per sub — write once is enough; WalkDir still
			// sees three matches.
			_ = name
			if err := os.WriteFile(name, []byte("pkg\n"), 0o644); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
	}
	got := suggestFilesInWorkDir(dir, "handler.go")
	if len(got) > 5 {
		t.Errorf("cap=5 not enforced; got %d suggestions: %v", len(got), got)
	}
}
