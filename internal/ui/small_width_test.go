package ui

import (
	"testing"
	"time"
)

// TestAgentTreePanelSmallWidthNoPanic pins the v0.85.11 fix: the summary
// footer rendered strings.Repeat("─", width-4) with no floor, so any width < 4
// (notably width 0 before the first WindowSizeMsg) panicked with a negative
// repeat count. The fix clamps via max(0, width-4).
func TestAgentTreePanelSmallWidthNoPanic(t *testing.T) {
	p := NewAgentTreePanel(DefaultStyles())
	// ≥2 nodes auto-shows the panel and makes buildSummary() non-empty,
	// so the summary-footer separator line is reached.
	p.UpdateTree([]AgentTreeNode{
		{ID: "a", AgentType: "explore", Status: "running", StartTime: time.Now()},
		{ID: "b", AgentType: "build", Status: "pending"},
	})

	for _, w := range []int{0, 1, 2, 3, 4, 7} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("AgentTreePanel.View(%d) panicked: %v", w, r)
				}
			}()
			_ = p.View(w)
		}()
	}
}
