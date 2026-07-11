package context

import (
	"strings"
	"testing"
)

// The standing memory-maintenance block is the WRITE half of the memory
// discipline (the Claude-Code behavior): without it the agent only updated
// its memory when a /loop iteration prompt told it to — normal interactive
// sessions never got smarter. It must be present in the base system prompt
// and STATIC (byte-stable for the cached prefix — pinned separately by
// TestBuildIsStableAcrossWorkingMemoryChanges).
func TestBuildIncludesMemoryMaintenanceDiscipline(t *testing.T) {
	b := NewPromptBuilder("/tmp/work", &ProjectInfo{Type: ProjectTypeGo})
	prompt := b.Build()

	if !strings.Contains(prompt, "## Memory Maintenance") {
		t.Fatal("base prompt must carry the standing memory-maintenance section")
	}
	if !strings.Contains(prompt, "memorize it in the same turn") {
		t.Fatal("proactive same-turn memorize instruction missing")
	}
	if !strings.Contains(prompt, "type=forget") {
		t.Fatal("the correction half (type=forget) must be coached")
	}

	// Static: two builds are byte-identical (no timestamps/dynamics inside).
	if p2 := NewPromptBuilder("/tmp/work", &ProjectInfo{Type: ProjectTypeGo}).Build(); p2 != prompt {
		t.Fatal("prompt must be deterministic across builders with identical inputs")
	}
}
