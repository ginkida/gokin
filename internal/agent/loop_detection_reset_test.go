package agent

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestResetLoopDetection_ClearsAllState(t *testing.T) {
	a := &Agent{
		callHistory:     map[string]int{"read:x": 4, "tool:read": 9},
		loopEarlyWarned: map[string]bool{"exact:read:x": true},
		loopIntervened:  true,
		loopCooldown:    2,
	}

	a.resetLoopDetection()

	if len(a.callHistory) != 0 {
		t.Errorf("callHistory not cleared: %v", a.callHistory)
	}
	if len(a.loopEarlyWarned) != 0 {
		t.Errorf("loopEarlyWarned not cleared: %v", a.loopEarlyWarned)
	}
	if a.loopIntervened {
		t.Error("loopIntervened not reset to false")
	}
	if a.loopCooldown != 0 {
		t.Errorf("loopCooldown = %d, want 0", a.loopCooldown)
	}
}

// Compaction handoff contract: when compaction injects the continuation hint,
// the stale per-call loop-detection counts (which referenced now-summarized
// calls the model can no longer see) must NOT survive into the new history —
// otherwise a legitimate post-compaction re-read falsely trips the loop guard.
func TestInjectContinuationHintResetsLoopDetection(t *testing.T) {
	key := "read:[[\"file_path\",\"A\"]]"
	a := &Agent{
		// Last message is a model turn so the hint is appended as a fresh entry.
		history:         []*genai.Content{genai.NewContentFromText("partial work", genai.RoleModel)},
		callHistory:     map[string]int{key: 3, "tool:read": 3},
		loopEarlyWarned: map[string]bool{"exact:" + key: true},
		loopIntervened:  true,
		loopCooldown:    1,
		originalPrompt:  "fix the bug",
	}

	a.injectContinuationHint()

	if len(a.callHistory) != 0 || len(a.loopEarlyWarned) != 0 {
		t.Errorf("loop counters survived compaction: callHistory=%v warned=%v", a.callHistory, a.loopEarlyWarned)
	}
	if a.loopIntervened || a.loopCooldown != 0 {
		t.Errorf("intervention state survived compaction: intervened=%v cooldown=%d", a.loopIntervened, a.loopCooldown)
	}

	var allText string
	for _, c := range a.history {
		for _, p := range c.Parts {
			allText += p.Text
		}
	}
	if !strings.Contains(allText, "compacted") {
		t.Errorf("continuation hint not injected; history text = %q", allText)
	}

	// Post-reset: a single re-read of the previously-hot key starts from 1, not
	// 4 — so it can't immediately trip the >3 exact-loop guard.
	a.callHistoryMu.Lock()
	a.callHistory[key]++
	got := a.callHistory[key]
	a.callHistoryMu.Unlock()
	if got != 1 {
		t.Errorf("post-compaction re-read count = %d, want 1 (clean slate)", got)
	}
}
