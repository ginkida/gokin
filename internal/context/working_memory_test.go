package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkingMemory_UpdateFromTurn_WritesStructuredMarkdown(t *testing.T) {
	dir := t.TempDir()
	mgr := NewWorkingMemoryManager(dir)

	updated := mgr.UpdateFromTurn(WorkingMemoryTurn{
		UserMessage:  "harden kimi reliability",
		Response:     "Improved the Kimi retry path in internal/client/retry.go and added regression coverage.\nVerification: go test ./internal/client -count=1 passed.\nIf you want, next I can tune loop guard heuristics too.",
		ToolsUsed:    []string{"read", "edit", "bash"},
		TouchedPaths: []string{"internal/client/retry.go", "internal/client/retry_test.go"},
		Commands:     []string{"go test ./internal/client -count=1"},
	})
	if !updated {
		t.Fatal("UpdateFromTurn() = false, want true")
	}

	content := mgr.GetContent()
	for _, needle := range []string{
		"# Working Memory",
		"## Established",
		"Latest result: Improved the Kimi retry path in internal/client/retry.go and added regression coverage.",
		"Files changed: internal/client/retry.go, internal/client/retry_test.go",
		"Verification already run: go test ./internal/client -count=1",
		"## Next",
		"If you want, next I can tune loop guard heuristics too.",
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("working memory missing %q:\n%s", needle, content)
		}
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gokin", ".working-memory.md"))
	if err != nil {
		t.Fatalf("ReadFile(.working-memory.md) error = %v", err)
	}
	if string(data) != content {
		t.Fatalf("persisted working memory mismatch:\ngot:  %q\nwant: %q", string(data), content)
	}
}

func TestWorkingMemory_LoadFromDisk_RestoresContent(t *testing.T) {
	dir := t.TempDir()
	mgr := NewWorkingMemoryManager(dir)
	mgr.UpdateFromTurn(WorkingMemoryTurn{
		Response:     "Updated the executor guard for repeated reads.",
		TouchedPaths: []string{"internal/tools/executor.go"},
	})
	saved := mgr.GetContent()

	mgr2 := NewWorkingMemoryManager(dir)
	mgr2.LoadFromDisk()
	if mgr2.GetContent() != saved {
		t.Fatalf("LoadFromDisk() mismatch:\ngot:  %q\nwant: %q", mgr2.GetContent(), saved)
	}
}

func TestWorkingMemory_UpdateFromTurn_ExtractsUnknownAndNext(t *testing.T) {
	mgr := NewWorkingMemoryManager(t.TempDir())
	mgr.UpdateFromTurn(WorkingMemoryTurn{
		Response: "Adjusted the retry policy for Kimi.\nCould not verify the Windows-specific path handling yet.\nNext step: run targeted Windows verification before broad rollout.",
	})

	content := mgr.GetContent()
	for _, needle := range []string{
		"## Unknown",
		"Could not verify the Windows-specific path handling yet.",
		"## Next",
		"Next step: run targeted Windows verification before broad rollout.",
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("working memory missing %q:\n%s", needle, content)
		}
	}
}
