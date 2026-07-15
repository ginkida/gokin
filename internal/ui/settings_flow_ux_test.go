package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestSettingsWithoutApplyCallbackIsHonestlyReadOnly(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.openSettings(OpenSettingsMsg{Items: []SettingItem{{Key: "sandbox", Name: "Sandbox", On: false, Live: true}}})

	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if m.settingsItems[0].On {
		t.Fatal("unwired setting was optimistically flipped")
	}
	got := stripAnsi(m.renderSettings())
	for _, want := range []string{"Changes are unavailable", "Read-only", "Sandbox"} {
		if !strings.Contains(got, want) {
			t.Fatalf("read-only settings missing %q:\n%s", want, got)
		}
	}
}

func TestSettingsEmptyKeyCannotInvokeCallback(t *testing.T) {
	m := NewModel()
	m.width = 80
	called := false
	m.SetSettingToggleCallback(func(string, bool) { called = true })
	m.openSettings(OpenSettingsMsg{Items: []SettingItem{{Name: "Broken setting", On: true}}})

	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if called || !m.settingsItems[0].On {
		t.Fatalf("empty key callback=%v state=%v", called, m.settingsItems[0].On)
	}
	got := stripAnsi(m.renderSettings())
	if !strings.Contains(got, "unavailable") || !strings.Contains(got, "no configurable key") {
		t.Fatalf("invalid setting lacks explanation:\n%s", got)
	}
}

func TestSettingsToggleShowsAppliedAndRestartFeedback(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.SetSettingToggleCallback(func(string, bool) {})
	m.openSettings(OpenSettingsMsg{Items: []SettingItem{{Key: "sessions", Name: "Session storage", On: false, Live: false}}})

	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEnter})
	pending := stripAnsi(m.renderSettings())
	for _, want := range []string{"Session storage: applying…", "[·] Session storage · applying…"} {
		if !strings.Contains(pending, want) {
			t.Fatalf("pending feedback missing %q:\n%s", want, pending)
		}
	}
	_ = m.handleMessageTypes(SettingToggleResultMsg{Key: "sessions", On: true, Success: true})
	got := stripAnsi(m.renderSettings())
	for _, want := range []string{"Session storage: on · restart required", "[✓] Session storage · on · restart required"} {
		if !strings.Contains(got, want) {
			t.Fatalf("restart feedback missing %q:\n%s", want, got)
		}
	}
}

func TestSettingsExplainsCombinedSafetyStateAndTransitions(t *testing.T) {
	tests := []struct {
		name      string
		perms     bool
		sandbox   bool
		want      string
		notWanted string
	}{
		{name: "guarded", perms: true, sandbox: true, want: "Safety: prompts on · bash sandboxed", notWanted: "YOLO"},
		{name: "prompts off only", perms: false, sandbox: true, want: "Safety: no prompts · bash sandboxed", notWanted: "bash unrestricted"},
		{name: "sandbox off only", perms: true, sandbox: false, want: "Safety: prompts on · approved bash unrestricted", notWanted: "no prompts"},
		{name: "full yolo", perms: false, sandbox: false, want: "Safety: full YOLO · no prompts · bash unrestricted", notWanted: "bash sandboxed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel()
			m.width, m.height = 90, 24
			m.openSettings(OpenSettingsMsg{Items: []SettingItem{
				{Key: "permissions", Name: "Ask before risky actions", On: tt.perms, Live: true},
				{Key: "sandbox", Name: "Sandbox bash commands", On: tt.sandbox, Live: true},
			}})
			view := stripAnsi(m.renderSettings())
			if !strings.Contains(view, tt.want) {
				t.Fatalf("combined safety state missing %q:\n%s", tt.want, view)
			}
			if strings.Contains(view, tt.notWanted) {
				t.Fatalf("combined safety state falsely claims %q:\n%s", tt.notWanted, view)
			}
		})
	}

	m := NewModel()
	m.width, m.height = 90, 24
	m.SetSettingToggleCallback(func(string, bool) {})
	m.openSettings(OpenSettingsMsg{Items: []SettingItem{
		{Key: "permissions", Name: "Ask before risky actions", On: true, Live: true},
		{Key: "sandbox", Name: "Sandbox bash commands", On: true, Live: true},
	}})

	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyDown})
	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if view := stripAnsi(m.renderSettings()); !strings.Contains(view, "Safety: prompts on · approved bash unrestricted") {
		t.Fatalf("optimistic sandbox change did not update safety state:\n%s", view)
	}
	_ = m.handleMessageTypes(SettingToggleResultMsg{Key: "sandbox", On: false, Success: true})
	if !strings.Contains(m.settingsNotice, "prompts on · approved bash unrestricted") {
		t.Fatalf("confirmed sandbox change lacks impact feedback: %q", m.settingsNotice)
	}

	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyUp})
	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEnter})
	_ = m.handleMessageTypes(SettingToggleResultMsg{Key: "permissions", On: false, Success: true})
	view := stripAnsi(m.renderSettings())
	for _, want := range []string{"Safety: full YOLO · no prompts · bash unrestricted", "full YOLO · no prompts · bash unrestricted"} {
		if !strings.Contains(view, want) {
			t.Fatalf("full YOLO transition missing %q:\n%s", want, view)
		}
	}
}

func TestSettingsPendingToggleBlocksConcurrentFlip(t *testing.T) {
	m := NewModel()
	calls := 0
	m.SetSettingToggleCallback(func(string, bool) { calls++ })
	m.openSettings(OpenSettingsMsg{Items: []SettingItem{
		{Key: "first", Name: "First", Live: true},
		{Key: "second", Name: "Second", Live: true},
	}})

	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEnter})
	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyDown})
	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if calls != 1 || m.settingsItems[1].On {
		t.Fatalf("concurrent setting flip escaped gate: calls=%d second=%v", calls, m.settingsItems[1].On)
	}
	if !strings.Contains(m.settingsNotice, "Wait for") {
		t.Fatalf("pending gate lacks feedback: %q", m.settingsNotice)
	}
}

func TestSettingsPendingToggleSurvivesCloseAndFreshSnapshot(t *testing.T) {
	m := NewModel()
	m.width = 80
	calls := 0
	m.SetSettingToggleCallback(func(key string, on bool) {
		calls++
		if key != "sandbox" || !on {
			t.Fatalf("toggle callback key=%q on=%v", key, on)
		}
	})
	m.openSettings(OpenSettingsMsg{Items: []SettingItem{{Key: "sandbox", Name: "Sandbox", On: false, Live: true}}})
	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEnter})
	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEscape})

	// The app can reopen Settings with a pre-apply snapshot while its worker is
	// still running. The optimistic value and lock must outlive the old modal.
	m.openSettings(OpenSettingsMsg{Items: []SettingItem{{Key: "SANDBOX", Name: "Sandbox", On: false, Live: true}}})
	if !m.settingsItems[0].On || !m.settingsItems[0].Pending {
		t.Fatalf("reopened row lost in-flight state: %+v", m.settingsItems[0])
	}
	if got := stripAnsi(m.renderSettings()); !strings.Contains(got, "[·] Sandbox · applying…") ||
		!strings.Contains(got, "Applying…") || strings.Contains(got, "Space Toggle") {
		t.Fatalf("reopened modal has dishonest in-flight feedback:\n%s", got)
	}
	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if calls != 1 || !strings.Contains(m.settingsNotice, "Wait for") {
		t.Fatalf("reopened modal allowed duplicate toggle: calls=%d notice=%q", calls, m.settingsNotice)
	}

	_ = m.handleMessageTypes(SettingToggleResultMsg{Key: "sandbox", On: true, Success: true})
	if m.settingsItems[0].Pending || !m.settingsItems[0].On || len(m.settingsPending) != 0 {
		t.Fatalf("successful result did not resolve in-flight state: item=%+v pending=%v", m.settingsItems[0], m.settingsPending)
	}
	if got := stripAnsi(m.renderSettings()); !strings.Contains(got, "Sandbox: on") {
		t.Fatalf("reopened modal lacks applied feedback:\n%s", got)
	}
}

func TestSettingsFailureRestoresAuthoritativeValueAndPersistsFeedback(t *testing.T) {
	m := NewModel()
	m.SetSettingToggleCallback(func(string, bool) {})
	m.openSettings(OpenSettingsMsg{Items: []SettingItem{{Key: "sandbox", Name: "Sandbox", On: false, Live: true}}})
	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEnter})

	_ = m.handleMessageTypes(SettingToggleResultMsg{
		Key: "sandbox", On: false, Success: false, Message: "Couldn't apply sandbox: provider unavailable",
	})
	if m.settingsItems[0].On || m.settingsItems[0].Pending {
		t.Fatalf("failed toggle was not restored: %+v", m.settingsItems[0])
	}
	view := stripAnsi(m.renderSettings())
	if !strings.Contains(view, "Sandbox: couldn't apply — restored off") {
		t.Fatalf("modal lacks rollback feedback:\n%s", view)
	}
	if m.toastManager.Count() != 1 || m.toastManager.toasts[0].Type != ToastError {
		t.Fatalf("rollback error toast missing: %+v", m.toastManager.toasts)
	}
	if output := stripAnsi(m.output.Content()); !strings.Contains(output, "provider unavailable") {
		t.Fatalf("rollback reason is not durable: %q", output)
	}
}

func TestSettingsOpenCopiesAndSanitizesSnapshot(t *testing.T) {
	items := []SettingItem{{Key: " feature ", Name: "\x1b[31mFeature\x1b[0m\nName", Desc: "Line one\nLine two", Category: "UI\nGroup"}}
	m := NewModel()
	m.width = 80
	m.openSettings(OpenSettingsMsg{Items: items, Model: "model\nname", Provider: "provider\tname"})
	items[0].Name = "mutated later"

	if m.settingsItems[0].Key != "feature" || m.settingsItems[0].Name != "Feature Name" || m.settingsItems[0].Desc != "Line one Line two" {
		t.Fatalf("snapshot not normalized: %+v", m.settingsItems[0])
	}
	got := stripAnsi(m.renderSettings())
	if strings.Contains(got, "mutated later") || !strings.Contains(got, "Model: model name · Provider: provider name") {
		t.Fatalf("snapshot alias or metadata injection remained:\n%s", got)
	}
}

func TestSettingsNavigationPagesAndClampsCursor(t *testing.T) {
	m := NewModel()
	m.height = 12
	m.settingsCursor = 999
	for i := 0; i < 20; i++ {
		m.settingsItems = append(m.settingsItems, SettingItem{Key: fmt.Sprintf("k%d", i), Name: fmt.Sprintf("Setting %02d", i)})
	}

	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyHome})
	if m.settingsCursor != 0 {
		t.Fatalf("Home cursor=%d", m.settingsCursor)
	}
	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.settingsCursor != 6 {
		t.Fatalf("PgDown cursor=%d", m.settingsCursor)
	}
	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEnd})
	if m.settingsCursor != len(m.settingsItems)-1 {
		t.Fatalf("End cursor=%d", m.settingsCursor)
	}
}

func TestSettingsPageNavigationCountsVisibleItemsNotCategoryRows(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.height = 12
	m.openSettings(OpenSettingsMsg{Items: sampleSettingItems(), Model: "glm-5.2", Provider: "glm"})
	m.settingsCursor = 5 // Plan mode; its compact page also contains Done gate.

	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.settingsCursor != 7 {
		t.Fatalf("PgDown cursor=%d, want 7 after the two visible settings", m.settingsCursor)
	}
}

func TestSettingsCompactFrameKeepsSelectionAndActionsVisible(t *testing.T) {
	m := NewModel()
	m.SetSettingToggleCallback(func(string, bool) {})
	m.applyResize(&tea.WindowSizeMsg{Width: 72, Height: 12})
	m.openSettings(OpenSettingsMsg{Items: sampleSettingItems(), Model: "glm-5.2", Provider: "glm"})
	m.settingsCursor = len(m.settingsItems) - 1

	view := stripAnsi(m.View())
	for _, want := range []string{"Show token usage", "↑", "Space Toggle", "m Model", "Esc Close"} {
		if !strings.Contains(view, want) {
			t.Fatalf("compact settings clipped %q:\n%s", want, view)
		}
	}
	if got := lipgloss.Height(view); got != 12 {
		t.Fatalf("frame height=%d want 12", got)
	}
}

func TestEmptySettingsExplainsMissingModelsInline(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.openSettings(OpenSettingsMsg{})
	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if m.state != StateModelSelector {
		t.Fatalf("empty model list should still open its recoverable selector, state=%v", m.state)
	}
	got := stripAnsi(m.renderModelSelector())
	for _, want := range []string{"No model choices are registered", "/provider", "/doctor", "Esc Back to settings"} {
		if !strings.Contains(got, want) {
			t.Fatalf("empty nested selector missing %q:\n%s", want, got)
		}
	}
	_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if m.state != StateSettings {
		t.Fatalf("empty selector should return to settings, state=%v", m.state)
	}
}

func TestModelSelectorWithoutCallbackDoesNotFakeSwitch(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.SetAvailableModels([]ModelInfo{{ID: "fast", Name: "Fast"}, {ID: "reasoning", Name: "Reasoning"}})
	m.SetCurrentModel("fast")
	m.state = StateModelSelector
	m.modelSelectedIndex = 1

	_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.currentModel != "fast" || m.state != StateModelSelector {
		t.Fatalf("unwired selector faked switch: model=%q state=%v", m.currentModel, m.state)
	}
	got := stripAnsi(m.renderModelSelector())
	if !strings.Contains(got, "switching is unavailable") || !strings.Contains(got, "Read-only") {
		t.Fatalf("unwired selector lacks durable feedback:\n%s", got)
	}
}

func TestModelSelectorInvalidIDStaysOpen(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.SetModelSelectCallback(func(string) { t.Fatal("invalid model submitted") })
	m.SetAvailableModels([]ModelInfo{{Name: "Broken model"}})
	m.state = StateModelSelector

	_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StateModelSelector || m.currentModel != "" {
		t.Fatalf("invalid model closed or changed selector: state=%v model=%q", m.state, m.currentModel)
	}
	if got := stripAnsi(m.renderModelSelector()); !strings.Contains(got, "no selectable ID") || !strings.Contains(got, "unavailable") {
		t.Fatalf("invalid model lacks explanation:\n%s", got)
	}
}

func TestModelSelectorFailureKeepsAuthoritativeModelAndVisibleReason(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.SetAvailableModels([]ModelInfo{{ID: "fast", Name: "Fast"}, {ID: "reasoning", Name: "Reasoning"}})
	m.SetCurrentModel("fast")
	m.SetModelSelectCallback(func(string) {})
	m.state = StateModelSelector
	m.modelSelectedIndex = 1

	_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.currentModel != "fast" || m.modelSwitchPending != "reasoning" {
		t.Fatalf("request faked success: model=%q pending=%q", m.currentModel, m.modelSwitchPending)
	}
	m.openModelSelector()
	if got := stripAnsi(m.renderModelSelector()); !strings.Contains(got, "Applying…") || strings.Contains(got, "Enter Confirm") {
		t.Fatalf("pending selector advertises another switch:\n%s", got)
	}
	_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyDown})
	_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.modelSwitchPending != "reasoning" || !strings.Contains(m.modelSelectorNotice, "Wait for") {
		t.Fatalf("pending selector accepted another request: pending=%q notice=%q", m.modelSwitchPending, m.modelSelectorNotice)
	}
	_ = m.handleMessageTypes(ModelSelectResultMsg{RequestedID: "obsolete", ModelID: "obsolete", Success: true})
	if m.currentModel != "fast" || m.modelSwitchPending != "reasoning" {
		t.Fatalf("stale result replaced active switch: model=%q pending=%q", m.currentModel, m.modelSwitchPending)
	}

	reason := "Couldn't switch to reasoning: credentials rejected · previous model restored"
	_ = m.handleMessageTypes(ModelSelectResultMsg{RequestedID: "reasoning", ModelID: "fast", Message: reason})
	if m.currentModel != "fast" || m.modelSwitchPending != "" {
		t.Fatalf("failure lost authoritative model: model=%q pending=%q", m.currentModel, m.modelSwitchPending)
	}
	if got := stripAnsi(m.renderModelSelector()); !strings.Contains(got, "credentials rejected") || !strings.Contains(got, "Enter Confirm") {
		t.Fatalf("failure lacks recovery feedback or restored action:\n%s", got)
	}
	if m.toastManager.Count() != 1 || m.toastManager.toasts[0].Type != ToastError {
		t.Fatalf("failure toast=%+v", m.toastManager.toasts)
	}
	if output := stripAnsi(m.output.Content()); !strings.Contains(output, "credentials rejected") {
		t.Fatalf("failure reason is not durable: %q", output)
	}
}

func TestModelSelectorCompactFrameKeepsSelectedModelVisible(t *testing.T) {
	m := NewModel()
	m.SetModelSelectCallback(func(string) {})
	m.applyResize(&tea.WindowSizeMsg{Width: 72, Height: 12})
	for i := 0; i < 12; i++ {
		m.availableModels = append(m.availableModels, ModelInfo{ID: fmt.Sprintf("m%d", i), Name: fmt.Sprintf("Model %02d", i), Description: strings.Repeat("description ", 20)})
	}
	m.currentModel = "m0"
	m.state = StateModelSelector
	m.modelSelectedIndex = 10

	view := stripAnsi(m.View())
	for _, want := range []string{"> 11. Model 10", "↑", "↓", "Enter Confirm", "Esc Close"} {
		if !strings.Contains(view, want) {
			t.Fatalf("compact model selector clipped %q:\n%s", want, view)
		}
	}
}

func TestSettingsAndModelSelectorFitNarrowWidths(t *testing.T) {
	for width := 10; width <= 48; width++ {
		settings := NewModel()
		settings.width = width
		settings.height = 12
		settings.openSettings(OpenSettingsMsg{Items: []SettingItem{{Key: "feature", Name: strings.Repeat("Настройка 界 ", 12), Desc: strings.Repeat("Описание ", 12), Category: strings.Repeat("Категория ", 8)}}})

		selector := NewModel()
		selector.width = width
		selector.height = 12
		selector.availableModels = []ModelInfo{{ID: "model", Name: strings.Repeat("Модель 界 ", 12), Description: strings.Repeat("Описание ", 12)}}

		for name, rendered := range map[string]string{"settings": settings.renderSettings(), "selector": selector.renderModelSelector()} {
			for row, line := range strings.Split(rendered, "\n") {
				if got := lipgloss.Width(line); got > width {
					t.Fatalf("%s width=%d row=%d overflow=%d: %q", name, width, row, got, stripAnsi(line))
				}
			}
		}
	}
}
