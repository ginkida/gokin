package context

import (
	"regexp"
	"testing"
)

// hhmm matches a wall-clock "HH:MM" timestamp like the removed "_Updated: 14:32_".
var hhmm = regexp.MustCompile(`\b\d{1,2}:\d{2}\b`)

// TestWorkingMemoryRenderIsCacheStable pins the v0.86.4 fix: working memory is
// injected into the CACHED system prefix, so its rendering must be byte-stable
// across turns (no wall-clock timestamp) or it busts prompt caching every minute.
func TestWorkingMemoryRenderIsCacheStable(t *testing.T) {
	turn := WorkingMemoryTurn{
		Response:     "Updated the executor guard for repeated reads.",
		TouchedPaths: []string{"internal/tools/executor.go"},
	}

	a := renderWorkingMemory(turn)
	b := renderWorkingMemory(turn)
	if a != b {
		t.Errorf("renderWorkingMemory not byte-stable across calls:\nA: %q\nB: %q", a, b)
	}
	if a == "" {
		t.Fatal("expected non-empty working memory render for a turn with content")
	}
	if hhmm.MatchString(a) {
		t.Errorf("working memory render contains a wall-clock timestamp (cache-buster): %q", a)
	}
}
