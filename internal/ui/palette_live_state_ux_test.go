package ui

import (
	"strings"
	"testing"
)

func paletteActionForTest(t *testing.T, m *Model, id string) EnhancedPaletteCommand {
	t.Helper()
	for _, command := range m.commandPalette.commands {
		if command.ActionID == id {
			return command
		}
	}
	t.Fatalf("palette action %q not found", id)
	return EnhancedPaletteCommand{}
}

func TestCommandPaletteSnapshotsLiveActionAvailability(t *testing.T) {
	m := NewModel()
	m.ShowCommandPalette()

	for id, wantReason := range map[string]string{
		paletteActionSettings:     "settings provider",
		paletteActionPlanningMode: "session mode controller",
		paletteActionPlanPanel:    "no active plan",
	} {
		action := paletteActionForTest(t, m, id)
		if action.Enabled || !strings.Contains(strings.ToLower(action.Reason), wantReason) {
			t.Fatalf("action %q availability=%v reason=%q, want disabled reason containing %q", id, action.Enabled, action.Reason, wantReason)
		}
	}

	m.commandPalette.Hide()
	m.SetOpenSettingsCallback(func() {})
	m.SetSessionModeCycleCallback(func() {})
	m.StartPlanExecution("plan-1", "Improve UX", "", []PlanStepInfo{{ID: 1, Title: "Audit"}})
	m.ShowCommandPalette()
	for _, id := range []string{paletteActionSettings, paletteActionPlanningMode, paletteActionPlanPanel} {
		action := paletteActionForTest(t, m, id)
		if !action.Enabled || action.Reason != "" {
			t.Fatalf("wired action %q availability=%v reason=%q, want enabled", id, action.Enabled, action.Reason)
		}
	}
}

func TestCommandPaletteDescriptionsTrackPanelDirection(t *testing.T) {
	m := NewModel()
	m.SetOpenSettingsCallback(func() {})
	m.SetSessionModeCycleCallback(func() {})
	m.todosVisible = true
	m.liveDetailExpanded = true
	m.activityFeed.ShowExplicit()
	m.agentTreePanel.Toggle()
	m.SetCompactMode(true)
	m.StartPlanExecution("plan-1", "Long plan", "", []PlanStepInfo{
		{ID: 1, Title: "One"}, {ID: 2, Title: "Two"}, {ID: 3, Title: "Three"},
		{ID: 4, Title: "Four"}, {ID: 5, Title: "Five"},
	})
	m.ShowCommandPalette()

	wants := map[string]string{
		paletteActionTodos:        "currently shown — hide",
		paletteActionLiveDetail:   "currently detailed — return to minimal",
		paletteActionActivityFeed: "currently shown — hide",
		paletteActionAgentTree:    "currently shown — hide",
		paletteActionCompactMode:  "currently compact — restore",
		paletteActionPlanPanel:    "currently compact — expand",
	}
	for id, want := range wants {
		description := strings.ToLower(paletteActionForTest(t, m, id).Description)
		if !strings.Contains(description, want) {
			t.Fatalf("action %q description=%q, want %q", id, description, want)
		}
	}

	// Change state while the palette is closed. The next open must rebuild its
	// action snapshot instead of showing the stale direction from last time.
	m.commandPalette.Hide()
	m.todosVisible = false
	m.liveDetailExpanded = false
	m.activityFeed.Toggle()
	m.agentTreePanel.Toggle()
	m.SetCompactMode(false)
	m.planProgressPanel.Toggle()
	m.ShowCommandPalette()

	wants = map[string]string{
		paletteActionTodos:        "currently hidden — show",
		paletteActionLiveDetail:   "currently minimal — show",
		paletteActionActivityFeed: "currently hidden — show",
		paletteActionAgentTree:    "currently hidden — show",
		paletteActionCompactMode:  "currently normal — switch",
		paletteActionPlanPanel:    "currently expanded — compact",
	}
	for id, want := range wants {
		description := strings.ToLower(paletteActionForTest(t, m, id).Description)
		if !strings.Contains(description, want) {
			t.Fatalf("refreshed action %q description=%q, want %q", id, description, want)
		}
	}
}
