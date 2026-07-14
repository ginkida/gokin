package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestSettingsModal_NavigateToggleClose exercises the modal: open populates and
// enters StateSettings, ↓ moves the cursor, Space toggles the selected item
// (optimistic flip + callback with the new value), Esc returns to input.
func TestSettingsModal_NavigateToggleClose(t *testing.T) {
	m := NewModel()

	var gotKey string
	var gotOn bool
	var calls int
	m.SetSettingToggleCallback(func(key string, on bool) { gotKey = key; gotOn = on; calls++ })

	m.openSettings(OpenSettingsMsg{
		Items: []SettingItem{
			{Key: "permissions", Desc: "a", On: true},
			{Key: "sandbox", Desc: "b", On: false},
		},
		Model:    "glm-5.2",
		Provider: "glm",
	})
	if m.state != StateSettings {
		t.Fatal("openSettings should enter StateSettings")
	}

	// Down to the second item.
	m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyDown})
	if m.settingsCursor != 1 {
		t.Fatalf("cursor=%d, want 1", m.settingsCursor)
	}

	// Space toggles the selected ("sandbox") on.
	m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if calls != 1 || gotKey != "sandbox" || !gotOn {
		t.Errorf("toggle callback: calls=%d key=%q on=%v, want 1/sandbox/true", calls, gotKey, gotOn)
	}
	if !m.settingsItems[1].On {
		t.Error("selected item should be optimistically flipped on")
	}

	// Esc closes back to the input state.
	m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if m.state != StateInput {
		t.Errorf("esc should return to StateInput, got %v", m.state)
	}
}

// TestSettingsModal_MOpensModelSelector: 'm' jumps to the model selector (model
// is a list, not a checkbox, so it reuses the existing selector).
func TestSettingsModal_MOpensModelSelector(t *testing.T) {
	m := NewModel()
	m.SetAvailableModels([]ModelInfo{{ID: "glm-5.2", Name: "GLM 5.2"}})
	m.SetCurrentModel("glm-5.2")
	m.openSettings(OpenSettingsMsg{
		Items:    []SettingItem{{Key: "permissions", On: true}},
		Model:    "glm-5.2",
		Provider: "glm",
	})
	m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if m.state != StateModelSelector {
		t.Errorf("'m' in settings should open the model selector, got %v", m.state)
	}

	m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if m.state != StateSettings {
		t.Errorf("Esc in nested model selector should return to settings, got %v", m.state)
	}
}

func TestSettingsModelSelectionReturnsAndRefreshesHeader(t *testing.T) {
	m := NewModel()
	m.SetAvailableModels([]ModelInfo{
		{ID: "fast", Name: "Fast"},
		{ID: "reasoning", Name: "Reasoning"},
	})
	m.SetCurrentModel("fast")
	m.SetModelSelectCallback(func(string) {})
	m.openSettings(OpenSettingsMsg{Items: []SettingItem{{Key: "permissions", On: true}}, Model: "fast", Provider: "glm"})

	m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyDown})
	m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEnter})

	if m.state != StateSettings {
		t.Fatalf("selection should return to settings, got %v", m.state)
	}
	if m.currentModel != "fast" || m.settingsModel != "fast" || m.modelSwitchPending != "reasoning" {
		t.Fatalf("unconfirmed selection changed model: current=%q settings=%q pending=%q", m.currentModel, m.settingsModel, m.modelSwitchPending)
	}
	if got := renderToPlain(m.renderSettings()); !strings.Contains(got, "Model: fast") || !strings.Contains(got, "Switching model to Reasoning") {
		t.Fatalf("settings lacks honest pending state:\n%s", got)
	}

	_ = m.handleMessageTypes(ModelSelectResultMsg{RequestedID: "reasoning", ModelID: "reasoning", Success: true})
	if m.currentModel != "reasoning" || m.settingsModel != "reasoning" || m.modelSwitchPending != "" {
		t.Fatalf("confirmed model snapshots diverged: current=%q settings=%q pending=%q", m.currentModel, m.settingsModel, m.modelSwitchPending)
	}
	if got := renderToPlain(m.renderSettings()); !strings.Contains(got, "Model: reasoning") || !strings.Contains(got, "Switched to reasoning") {
		t.Fatalf("settings header remained stale after confirmation:\n%s", got)
	}
}

// sampleSettingItems builds a multi-category list for render tests.
func sampleSettingItems() []SettingItem {
	return []SettingItem{
		{Key: "permissions", Name: "Ask before risky actions", Desc: "Ask before risky tool actions", Category: "Safety", On: true, Live: true},
		{Key: "sandbox", Name: "Sandbox bash commands", Desc: "Run bash commands in a sandbox", Category: "Safety", On: false, Live: true},
		{Key: "diff", Name: "Confirm edits with a diff", Desc: "Show a diff approval card before edits", Category: "Safety", On: true, Live: true},
		{Key: "autocompact", Name: "Auto-compact long history", Desc: "Auto-summarize history near the limit", Category: "Context & Memory", On: true, Live: true},
		{Key: "memory", Name: "Memory tool & recall", Desc: "Enable the memory tool and recall", Category: "Context & Memory", On: true, Live: true},
		{Key: "plan", Name: "Plan mode", Desc: "Enable plan-mode tools", Category: "Workflow", On: true, Live: true},
		{Key: "donegate", Name: "Verify before finishing", Desc: "Verify build/test before finishing", Category: "Workflow", On: true, Live: true},
		{Key: "session", Name: "Save & resume conversations", Desc: "Save & resume conversations across restarts", Category: "Files & Search", On: true, Live: false},
		{Key: "watcher", Name: "Watch for file changes", Desc: "Detect external file changes", Category: "Files & Search", On: false, Live: false},
		{Key: "tokens", Name: "Show token usage", Desc: "Show token usage in the status bar", Category: "Interface & Web", On: true, Live: true},
	}
}

func TestSettingsModal_RendersGroupedFriendly(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.height = 44
	m.openSettings(OpenSettingsMsg{Items: sampleSettingItems(), Model: "glm-5.2", Provider: "glm"})
	out := renderToPlain(m.renderSettings())

	// Category headers (uppercased).
	for _, cat := range []string{"SAFETY", "CONTEXT & MEMORY", "WORKFLOW", "FILES & SEARCH", "INTERFACE & WEB"} {
		if !strings.Contains(out, cat) {
			t.Errorf("missing category header %q\n%s", cat, out)
		}
	}
	// Friendly name shown (not the raw key as the label).
	if !strings.Contains(out, "Ask before risky actions") {
		t.Errorf("friendly name not shown:\n%s", out)
	}
	// The first item is selected by default -> its description shows below.
	if !strings.Contains(out, "Ask before risky tool actions") {
		t.Errorf("selected item description not shown:\n%s", out)
	}
	// Boot-wired item shows the restart label.
	if !strings.Contains(out, "restart") {
		t.Errorf("boot-wired restart label missing:\n%s", out)
	}
}

func TestSettingsModal_HeightAwareScrollNoClip(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.height = 16 // short: cannot fit all 10 items + 5 headers + desc + footer
	m.openSettings(OpenSettingsMsg{Items: sampleSettingItems(), Model: "glm-5.2", Provider: "glm"})
	// Move the cursor to the last item — its row must be brought into view.
	m.settingsCursor = len(m.settingsItems) - 1
	out := renderToPlain(m.renderSettings())

	if !strings.Contains(out, "Show token usage") {
		t.Errorf("selected (last) item not scrolled into view:\n%s", out)
	}
	if !strings.Contains(out, "more") {
		t.Errorf("expected an '↑/↓ more' marker on a clipped short terminal:\n%s", out)
	}
	// The rendered modal must not blow past the terminal height by a lot
	// (height-aware window). Allow container chrome slack.
	lines := strings.Count(out, "\n")
	if lines > m.height+8 {
		t.Errorf("modal rendered %d lines for height %d — not height-aware:\n%s", lines, m.height, out)
	}
}

// TestSettingsModal_EdgeDimensionsNoPanic pins the width-math rule: the
// height-aware modal must render without panicking at degenerate sizes
// (width/height 0 before the first WindowSizeMsg, very narrow terminals).
func TestSettingsModal_EdgeDimensionsNoPanic(t *testing.T) {
	for _, dim := range []struct{ w, h int }{{0, 0}, {1, 1}, {10, 4}, {40, 2}, {200, 200}} {
		m := NewModel()
		m.width = dim.w
		m.height = dim.h
		m.openSettings(OpenSettingsMsg{Items: sampleSettingItems(), Model: "glm-5.2", Provider: "glm"})
		m.settingsCursor = len(m.settingsItems) - 1 // exercise the scroll path
		_ = m.renderSettings()                      // must not panic
		// empty items too
		m.settingsItems = nil
		_ = m.renderSettings()
	}
}
