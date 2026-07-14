package ui

import (
	"strings"
	"testing"
	"time"
)

func assertShortcutHints(t *testing.T, m *Model, want, notWant []string) {
	t.Helper()
	got := plainShortcutHints(m.contextualShortcutHintPairs())
	for _, hint := range want {
		if !strings.Contains(got, hint) {
			t.Errorf("missing shortcut hint %q in %q", hint, got)
		}
	}
	for _, hint := range notWant {
		if strings.Contains(got, hint) {
			t.Errorf("unavailable shortcut hint %q in %q", hint, got)
		}
	}
}

func TestStatusHintsDoNotOfferUnavailablePlanOrDiffActions(t *testing.T) {
	plan := NewModel()
	plan.state = StatePlanApproval
	plan.planRequest = &PlanApprovalRequestMsg{Title: "Empty plan"}
	assertShortcutHints(t, plan,
		[]string{"n Reject", "m Modify", "esc Cancel"},
		[]string{"y Approve"},
	)

	diff := NewModel()
	diff.state = StateMultiDiffPreview
	assertShortcutHints(t, diff,
		[]string{"esc Close"},
		[]string{"y/n Decide file", "A/R Decide rest", "Tab Switch pane", "↑↓ Browse"},
	)
}

func TestModelSelectorStatusHintsFollowAvailabilityAndReturnContext(t *testing.T) {
	model := ModelInfo{ID: "fast", Name: "Fast"}
	models := []ModelInfo{model, {ID: "deep", Name: "Deep"}}
	callback := func(string) {}

	for _, tc := range []struct {
		name        string
		models      []ModelInfo
		onSelect    func(string)
		pending     string
		returnState State
		want        []string
		notWant     []string
	}{
		{
			name:    "empty direct selector",
			want:    []string{"esc Close"},
			notWant: []string{"Enter Select", "↑↓ Navigate", "↑↓ Inspect"},
		},
		{
			name:        "empty settings selector",
			returnState: StateSettings,
			want:        []string{"esc Back"},
			notWant:     []string{"Enter Select", "esc Close"},
		},
		{
			name:    "read only selector",
			models:  models,
			want:    []string{"↑↓ Inspect", "esc Close"},
			notWant: []string{"Enter Select", "↑↓ Navigate"},
		},
		{
			name:     "switch pending",
			models:   models,
			onSelect: callback,
			pending:  model.ID,
			want:     []string{"↑↓ Inspect", "esc Close"},
			notWant:  []string{"Enter Select", "↑↓ Navigate"},
		},
		{
			name:     "selectable",
			models:   models,
			onSelect: callback,
			want:     []string{"↑↓ Navigate", "Enter Select", "esc Close"},
			notWant:  []string{"↑↓ Inspect"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel()
			m.state = StateModelSelector
			m.availableModels = tc.models
			m.onModelSelect = tc.onSelect
			m.modelSwitchPending = tc.pending
			m.modelSelectorReturnState = tc.returnState
			assertShortcutHints(t, m, tc.want, tc.notWant)
		})
	}
}

func TestNotificationStatusHintsFollowContentAndOverflow(t *testing.T) {
	empty := NewModel()
	empty.openNotificationCenter()
	assertShortcutHints(t, empty,
		[]string{"esc Back"},
		[]string{"↑↓ Select", "Enter Details", "c Clear earlier"},
	)

	active := NewModel()
	active.toastManager.ShowInfo("Connected")
	active.toastManager.ShowWarning("Needs attention")
	active.openNotificationCenter()
	assertShortcutHints(t, active,
		[]string{"↑↓ Select", "Enter Details", "esc Back"},
		[]string{"c Clear earlier"},
	)

	activeToast := active.toastManager.Active()[0]
	active.toastManager.Dismiss(activeToast.ID)
	assertShortcutHints(t, active,
		[]string{"↑↓ Select", "Enter Details", "c Clear earlier", "esc Back"},
		nil,
	)

	short := NewModel()
	short.width, short.height = 80, 40
	short.toastManager.ShowInfo("Short detail")
	short.openNotificationCenter()
	short.notificationDetail = true
	assertShortcutHints(t, short,
		[]string{"esc Back to list"},
		[]string{"↑↓ Scroll", "PgUp/PgDn Page"},
	)

	long := NewModel()
	long.width, long.height = 28, 12
	long.toastManager.Show(ToastError, "Failure", strings.Repeat("diagnostic recovery step ", 20), time.Minute)
	long.openNotificationCenter()
	long.notificationDetail = true
	assertShortcutHints(t, long,
		[]string{"↑↓ Scroll", "PgUp/PgDn Page", "esc Back to list"},
		nil,
	)
}

func TestSettingsAndKeyEntryStatusHintsFollowActionAvailability(t *testing.T) {
	toggle := func(string, bool) {}

	for _, tc := range []struct {
		name    string
		model   func() *Model
		want    []string
		notWant []string
	}{
		{
			name: "empty settings",
			model: func() *Model {
				m := NewModel()
				m.state = StateSettings
				return m
			},
			want:    []string{"m Model", "esc Close"},
			notWant: []string{"Space Toggle", "↑↓ Inspect"},
		},
		{
			name: "toggleable setting",
			model: func() *Model {
				m := NewModel()
				m.state = StateSettings
				m.settingsItems = []SettingItem{{Key: "tokens", Name: "Tokens"}, {Key: "compact", Name: "Compact"}}
				m.onSettingToggle = toggle
				return m
			},
			want:    []string{"Space Toggle", "↑↓ Navigate", "m Model", "esc Close"},
			notWant: []string{"↑↓ Inspect"},
		},
		{
			name: "invalid selected setting",
			model: func() *Model {
				m := NewModel()
				m.state = StateSettings
				m.settingsItems = []SettingItem{{Name: "Unavailable"}, {Key: "tokens", Name: "Tokens"}}
				m.onSettingToggle = toggle
				return m
			},
			want:    []string{"↑↓ Inspect", "m Model", "esc Close"},
			notWant: []string{"Space Toggle", "↑↓ Navigate"},
		},
		{
			name: "available key entry",
			model: func() *Model {
				m := NewModel()
				m.state = StateAPIKeyEntry
				m.keyEntryProvider = "openai"
				m.onKeyEntrySubmit = func(string, string) {}
				return m
			},
			want: []string{"Enter Save", "esc Cancel"},
		},
		{
			name: "unavailable key entry",
			model: func() *Model {
				m := NewModel()
				m.state = StateAPIKeyEntry
				return m
			},
			want:    []string{"esc Close"},
			notWant: []string{"Enter Save", "esc Cancel"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertShortcutHints(t, tc.model(), tc.want, tc.notWant)
		})
	}
}

func TestSettingsFooterDoesNotOfferInvalidSelectedToggle(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	m.state = StateSettings
	m.settingsItems = []SettingItem{{Name: "Unavailable"}}
	m.onSettingToggle = func(string, bool) {}

	got := stripAnsi(m.renderSettings())
	if !strings.Contains(got, "Selected unavailable") {
		t.Fatalf("settings footer should explain the disabled selected row:\n%s", got)
	}
	if strings.Contains(got, "Space Toggle") {
		t.Fatalf("settings footer offered an action the selected row cannot perform:\n%s", got)
	}
}

func TestShortcutAndBatchStatusHintsFollowSubmode(t *testing.T) {
	shortcuts := NewModel()
	shortcuts.state = StateShortcutsOverlay
	assertShortcutHints(t, shortcuts,
		[]string{"Type Filter", "↑↓ Browse", "esc Close"},
		[]string{"esc Clear filter"},
	)
	shortcuts.shortcutsOverlay.SetSearch("model")
	assertShortcutHints(t, shortcuts,
		[]string{"Type Filter", "Backspace Edit", "esc Clear filter"},
		[]string{"↑↓ Browse", "esc Close"},
	)

	running := NewModel()
	running.state = StateBatchProgress
	running.progressModel.onAction = func(ProgressAction) {}
	assertShortcutHints(t, running,
		[]string{"p Pause", "esc Cancel"},
		[]string{"Enter/Esc Close"},
	)
	running.progressModel.isPaused = true
	assertShortcutHints(t, running,
		[]string{"p Resume", "esc Cancel"},
		[]string{"p Pause"},
	)
	running.progressModel.isComplete = true
	assertShortcutHints(t, running,
		[]string{"Enter/Esc Close"},
		[]string{"p Resume", "esc Cancel"},
	)
}
