package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

// --- InstructionFileNames ---

func TestInstructionFileNames(t *testing.T) {
	files := InstructionFileNames()
	if len(files) == 0 {
		t.Fatal("InstructionFileNames should return at least one file")
	}
	// Should include GOKIN.md and CLAUDE.md
	found := map[string]bool{}
	for _, f := range files {
		found[f] = true
	}
	if !found["GOKIN.md"] {
		t.Error("InstructionFileNames should include GOKIN.md")
	}
	if !found["CLAUDE.md"] {
		t.Error("InstructionFileNames should include CLAUDE.md")
	}
}

func TestInstructionFileNames_ReturnsCopy(t *testing.T) {
	a := InstructionFileNames()
	a[0] = "MUTATED"
	b := InstructionFileNames()
	if b[0] == "MUTATED" {
		t.Error("InstructionFileNames should return a copy, not the internal slice")
	}
}

// --- NewProjectMemory + HasInstructions ---

func TestNewProjectMemory(t *testing.T) {
	pm := NewProjectMemory("/tmp")
	if pm == nil {
		t.Fatal("NewProjectMemory returned nil")
	}
	if pm.HasInstructions() {
		t.Error("new ProjectMemory should have no instructions")
	}
}

func TestHasInstructions_Empty(t *testing.T) {
	pm := NewProjectMemory("/tmp")
	if pm.HasInstructions() {
		t.Error("empty ProjectMemory should not have instructions")
	}
}

func TestHasInstructions_AfterLoad(t *testing.T) {
	dir := t.TempDir()
	// Create a GOKIN.md instruction file
	if err := os.WriteFile(filepath.Join(dir, "GOKIN.md"), []byte("# Test Instructions\nSome rules."), 0644); err != nil {
		t.Fatal(err)
	}
	pm := NewProjectMemory(dir)
	if err := pm.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !pm.HasInstructions() {
		t.Error("should have instructions after loading from GOKIN.md")
	}
}

func TestHasInstructions_NoFiles(t *testing.T) {
	dir := t.TempDir()
	pm := NewProjectMemory(dir)
	if err := pm.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if pm.HasInstructions() {
		t.Error("should not have instructions when no files exist")
	}
}

// --- GetInstructions / GetSourcePath ---

func TestGetInstructions_Empty(t *testing.T) {
	pm := NewProjectMemory("/tmp")
	if pm.GetInstructions() != "" {
		t.Error("empty ProjectMemory should return empty instructions")
	}
}

func TestGetInstructions_AfterLoad(t *testing.T) {
	dir := t.TempDir()
	content := "# My Rules\nDo good things."
	if err := os.WriteFile(filepath.Join(dir, "GOKIN.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	pm := NewProjectMemory(dir)
	if err := pm.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	got := pm.GetInstructions()
	// Load appends a trailing newline; compare trimmed
	if strings.TrimSpace(got) != content {
		t.Errorf("GetInstructions = %q, want %q", got, content)
	}
}

func TestGetSourcePath_Empty(t *testing.T) {
	pm := NewProjectMemory("/tmp")
	if pm.GetSourcePath() != "" {
		t.Error("empty ProjectMemory should return empty sourcePath")
	}
}

func TestGetSourcePath_AfterLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "GOKIN.md"), []byte("rules"), 0644); err != nil {
		t.Fatal(err)
	}
	pm := NewProjectMemory(dir)
	if err := pm.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	src := pm.GetSourcePath()
	if src == "" {
		t.Error("sourcePath should not be empty after loading")
	}
}

// --- Load multi-layer ---

func TestLoad_MergesMultipleLayers(t *testing.T) {
	dir := t.TempDir()

	// Project layer
	if err := os.WriteFile(filepath.Join(dir, "GOKIN.md"), []byte("Project rules"), 0644); err != nil {
		t.Fatal(err)
	}
	// Local layer (highest priority)
	if err := os.WriteFile(filepath.Join(dir, "GOKIN.local.md"), []byte("Local rules"), 0644); err != nil {
		t.Fatal(err)
	}

	pm := NewProjectMemory(dir)
	if err := pm.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	got := pm.GetInstructions()
	if got == "" {
		t.Fatal("expected merged instructions")
	}
	// Both layers should be present
	if !containsStr(got, "Project rules") {
		t.Errorf("expected 'Project rules' in merged instructions: %q", got)
	}
	if !containsStr(got, "Local rules") {
		t.Errorf("expected 'Local rules' in merged instructions: %q", got)
	}
}

func TestLoad_EmptyFileSkipped(t *testing.T) {
	dir := t.TempDir()
	// Empty file should be skipped
	if err := os.WriteFile(filepath.Join(dir, "GOKIN.md"), []byte("   "), 0644); err != nil {
		t.Fatal(err)
	}
	pm := NewProjectMemory(dir)
	if err := pm.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if pm.HasInstructions() {
		t.Error("empty file should not produce instructions")
	}
}

func TestLoad_ProjectMemoryLayer(t *testing.T) {
	dir := t.TempDir()
	// Agent-managed project memory layer
	memDir := filepath.Join(dir, ".gokin")
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "project-memory.md"), []byte("Learned facts"), 0644); err != nil {
		t.Fatal(err)
	}
	pm := NewProjectMemory(dir)
	if err := pm.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	got := pm.GetInstructions()
	if !containsStr(got, "Learned facts") {
		t.Errorf("expected 'Learned facts' from project-memory layer: %q", got)
	}
}

// --- Reload ---

func TestReload(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "GOKIN.md"), []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	pm := NewProjectMemory(dir)
	if err := pm.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if strings.TrimSpace(pm.GetInstructions()) != "v1" {
		t.Errorf("initial = %q, want 'v1'", pm.GetInstructions())
	}

	// Change file and reload
	if err := os.WriteFile(filepath.Join(dir, "GOKIN.md"), []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := pm.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if strings.TrimSpace(pm.GetInstructions()) != "v2" {
		t.Errorf("after reload = %q, want 'v2'", pm.GetInstructions())
	}
}

func TestReload_ClearsWhenFileDeleted(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "GOKIN.md")
	if err := os.WriteFile(fpath, []byte("rules"), 0644); err != nil {
		t.Fatal(err)
	}
	pm := NewProjectMemory(dir)
	if err := pm.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !pm.HasInstructions() {
		t.Fatal("should have instructions before delete")
	}
	if err := os.Remove(fpath); err != nil {
		t.Fatal(err)
	}
	if err := pm.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if pm.HasInstructions() {
		t.Error("should not have instructions after file deleted + reloaded")
	}
}

// --- processIncludes ---

func TestProcessIncludes_NoIncludes(t *testing.T) {
	text := "some text without includes"
	got := processIncludes(text, "/tmp")
	// processIncludes appends trailing newline; compare trimmed
	if strings.TrimSpace(got) != text {
		t.Errorf("text without includes should be unchanged: %q", got)
	}
}

func TestProcessIncludes_WithFile(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	included := "included content"
	if err := os.WriteFile(filepath.Join(dir, "included.md"), []byte(included), 0644); err != nil {
		t.Fatal(err)
	}
	// The @include directive format is "@<path>" (no space). "@include foo" would
	// be parsed as the path "include foo". Use "@./included.md" or "@included.md".
	text := "before\n@./included.md\nafter"
	got := processIncludes(text, dir)
	if !containsStr(got, "included content") {
		t.Errorf("expected included content: %q", got)
	}
	if !containsStr(got, "before") || !containsStr(got, "after") {
		t.Errorf("surrounding text should be preserved: %q", got)
	}
}

func TestProcessIncludes_MissingFile(t *testing.T) {
	dir := t.TempDir()
	text := "before\n@include nonexistent.md\nafter"
	got := processIncludes(text, dir)
	// Missing file should not crash; include directive may remain or be removed
	if !containsStr(got, "before") {
		t.Errorf("surrounding text should be preserved: %q", got)
	}
}

// --- OnReload ---

func TestProjectMemory_OnReload(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "GOKIN.md")
	if err := os.WriteFile(fpath, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}

	pm := NewProjectMemory(dir)
	pm.OnReload(func() {})

	if err := pm.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Reload should work without panic
	if err := os.WriteFile(fpath, []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := pm.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	// Note: onReload is called by the file watcher, not by Reload() directly.
	// This test verifies OnReload can be set without panic.
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStrHelper(s, sub))
}

func containsStrHelper(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
