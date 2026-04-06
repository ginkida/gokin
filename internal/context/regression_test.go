package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

// Regression tests for bugs that have been fixed, reverted, and re-fixed.
// See memory/recurring_bugs.md for context on each.

// TestRegression_ProcessIncludes_PathTraversal verifies that relative @include
// directives cannot escape the base directory via ../ traversal.
// Bug: filepath.Join(baseDir, "../../etc/passwd") produces an absolute path,
// so the filepath.IsAbs guard was useless. Fix: wasRelative flag set before Join.
func TestRegression_ProcessIncludes_PathTraversal(t *testing.T) {
	baseDir := t.TempDir()

	// Create a file outside baseDir that an attacker might try to include
	outsideDir := t.TempDir()
	secretFile := filepath.Join(outsideDir, "secret.txt")
	os.WriteFile(secretFile, []byte("SECRET_DATA"), 0644)

	// Compute relative path from baseDir to secretFile
	relPath, err := filepath.Rel(baseDir, secretFile)
	if err != nil {
		t.Fatalf("failed to compute relative path: %v", err)
	}

	input := "@" + relPath
	result := processIncludes(input, baseDir)

	if strings.Contains(result, "SECRET_DATA") {
		t.Errorf("path traversal succeeded — relative include escaped baseDir.\nInput: %s\nResult: %s", input, result)
	}
}

// TestRegression_ProcessIncludes_AbsolutePathAllowed verifies that absolute
// @include paths still work (not blocked by traversal protection).
func TestRegression_ProcessIncludes_AbsolutePathAllowed(t *testing.T) {
	dir := t.TempDir()
	absFile := filepath.Join(dir, "allowed.md")
	os.WriteFile(absFile, []byte("ALLOWED_CONTENT"), 0644)

	input := "@" + absFile
	result := processIncludes(input, "/some/other/base")

	if !strings.Contains(result, "ALLOWED_CONTENT") {
		t.Errorf("absolute include should work, got: %s", result)
	}
}

// TestRegression_ProjectRulesLoaded verifies that .gokin/rules/*.md files
// are loaded even when a GOKIN.md file exists in the project root.
// Bug: both instructionFiles and rules/*.md were in one "project" layer,
// and Load() had `break` after first match — killing rules/*.md.
// Fix: split into "project" (first match) and "project-rules" (all rules) layers.
func TestRegression_ProjectRulesLoaded(t *testing.T) {
	dir := t.TempDir()

	// Create GOKIN.md (will match first in project layer)
	os.WriteFile(filepath.Join(dir, "GOKIN.md"), []byte("main instructions"), 0644)

	// Create rules directory with two rule files
	rulesDir := filepath.Join(dir, ".gokin", "rules")
	os.MkdirAll(rulesDir, 0755)
	os.WriteFile(filepath.Join(rulesDir, "rule1.md"), []byte("RULE_ONE"), 0644)
	os.WriteFile(filepath.Join(rulesDir, "rule2.md"), []byte("RULE_TWO"), 0644)

	pm := NewProjectMemory(dir)
	if err := pm.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	instructions := pm.GetInstructions()
	if !strings.Contains(instructions, "main instructions") {
		t.Error("GOKIN.md content missing")
	}
	if !strings.Contains(instructions, "RULE_ONE") {
		t.Error("rules/rule1.md not loaded — break in project layer kills rules/*.md")
	}
	if !strings.Contains(instructions, "RULE_TWO") {
		t.Error("rules/rule2.md not loaded")
	}
}

// TestRegression_ExtractAsync_CopyProtection verifies that ExtractAsync
// makes a copy of the history slice, so mutations after the call don't affect extraction.
// Bug: Extract was called with a live slice reference in a goroutine.
func TestRegression_ExtractAsync_CopyProtection(t *testing.T) {
	sm := NewSessionMemoryManager(t.TempDir(), SessionMemoryConfig{
		Enabled:         true,
		MinTokensToInit: 0, // always extract
	})

	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "fix the bug in auth.go"}}},
		{Role: "model", Parts: []*genai.Part{{Text: "I'll look at auth.go"}}},
		{Role: "user", Parts: []*genai.Part{{Text: "also check login.go"}}},
		{Role: "model", Parts: []*genai.Part{{Text: "Done"}}},
	}

	// Call ExtractAsync — it should copy the slice internally
	sm.ExtractAsync(history, 50000)

	// Immediately mutate the original slice — this should NOT affect extraction
	history[0] = nil
	history[1] = nil

	// Wait briefly for the goroutine to complete, then verify it didn't panic.
	// The race detector will catch unsynchronized access if copy was missing.
	for i := 0; i < 50; i++ {
		sm.mu.RLock()
		done := sm.initialized
		sm.mu.RUnlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}
