package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestOptionQuestionResizeDoesNotPanicBeforeCustomEditorExists(t *testing.T) {
	m := NewModel()
	m.applyResize(&tea.WindowSizeMsg{Width: 40, Height: 12})
	_ = m.handleMessageTypes(QuestionRequestMsg{
		Question: "Choose one",
		Options:  []string{"Selected answer", "Other answer"},
	})
	if m.state != StateQuestionPrompt || m.questionRequest == nil {
		t.Fatalf("setup did not open option question: state=%v request=%+v", m.state, m.questionRequest)
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Errorf("resizing an option-only question panicked before its lazy custom editor existed: %v", recovered)
		}
	}()
	m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 2})
}

func TestTinyHiddenTargetsCannotExecuteActions(t *testing.T) {
	t.Run("settings-toggle", func(t *testing.T) {
		calls := 0
		m := NewModel()
		m.SetSettingToggleCallback(func(string, bool) { calls++ })
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 2})
		m.openSettings(OpenSettingsMsg{Items: []SettingItem{{
			Key: "danger", Name: "Danger mode", Desc: "Changes the safety policy",
		}}})
		assertTinyAuditSetupHides(t, m.View(), "Danger mode", "Space Toggle")

		_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeySpace})
		if calls != 0 || m.settingsItems[0].On || m.settingsItems[0].Pending {
			t.Errorf("invisible setting was toggled: calls=%d on=%v pending=%v", calls, m.settingsItems[0].On, m.settingsItems[0].Pending)
		}
	})

	t.Run("model-switch", func(t *testing.T) {
		calls := 0
		m := NewModel()
		m.SetModelSelectCallback(func(string) { calls++ })
		m.availableModels = []ModelInfo{{ID: "target-model", Name: "Target Model"}}
		m.state = StateModelSelector
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 2})
		assertTinyAuditSetupHides(t, m.View(), "Target Model", "Enter Confirm")

		_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if calls != 0 || m.modelSwitchPending != "" {
			t.Errorf("invisible model selection switched: calls=%d pending=%q", calls, m.modelSwitchPending)
		}
	})

	t.Run("command-palette", func(t *testing.T) {
		runs := 0
		m := NewModel()
		m.commandPalette.SetActionCommands([]EnhancedPaletteCommand{{
			Name: "irreversible", Shortcut: "/irreversible", Type: CommandTypeAction, Enabled: true,
			Action: func() { runs++ },
		}})
		m.commandPalette.Show()
		m.state = StateCommandPalette
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 3})
		assertTinyAuditSetupHides(t, m.View(), "/irreversible")

		_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if runs != 0 {
			t.Errorf("Enter executed hidden palette target %d time(s)", runs)
		}
	})

	t.Run("search-open", func(t *testing.T) {
		calls := 0
		m := NewModel()
		m.searchResults.SetActionCallback(func(SearchAction, string, int) { calls++ })
		m.searchResults.SetActionsLinked(true)
		m.searchResults.SetResults("needle", "grep", []SearchResult{{
			FilePath: "selected-target.go", LineNumber: 7, Content: "match",
		}})
		m.state = StateSearchResults
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 2})
		assertTinyAuditSetupHides(t, m.View(), "selected-target.go", "Enter Open")

		_, cmd := m.searchResults.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if calls != 0 || cmd != nil {
			t.Errorf("Enter opened hidden search target: calls=%d command=%v", calls, cmd != nil)
		}
	})

	t.Run("git-stage", func(t *testing.T) {
		calls := 0
		m := NewModel()
		m.gitStatusModel.SetActionCallback(func(GitAction, []string, string) { calls++ })
		m.gitStatusModel.SetActionsLinked(true)
		m.gitStatusModel.SetStatus([]GitFileEntry{{FilePath: "selected-target.go"}}, "main", "", "")
		m.state = StateGitStatus
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 2})
		assertTinyAuditSetupHides(t, m.View(), "selected-target.go", "Stage/Unstage")

		_, cmd := m.gitStatusModel.Update(tea.KeyMsg{Type: tea.KeySpace})
		if calls != 0 || cmd != nil {
			t.Errorf("Space staged hidden git target: calls=%d command=%v", calls, cmd != nil)
		}
	})

	t.Run("api-key-submit", func(t *testing.T) {
		calls := 0
		m := NewModel()
		m.SetKeyEntrySubmitCallback(func(string, string) { calls++ })
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 2})
		m.openKeyEntry(OpenKeyEntryMsg{Provider: "provider", DisplayName: "Provider"})
		m.keyEntryInput.SetValue("secret-value")
		assertTinyAuditSetupHides(t, m.View(), "••", "Enter Save")

		_ = m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if calls != 0 || m.keyEntryPendingProvider != "" {
			t.Errorf("Enter submitted a completely hidden API-key field: calls=%d pending=%q", calls, m.keyEntryPendingProvider)
		}
	})

	t.Run("question-answer", func(t *testing.T) {
		var answers []string
		m := NewModel()
		m.SetQuestionCallback(func(answer string) { answers = append(answers, answer) })
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 2})
		_ = m.handleMessageTypes(QuestionRequestMsg{
			Question: "Choose target", Options: []string{"Selected Answer", "Other Answer"},
		})
		assertTinyAuditSetupHides(t, m.View(), "Selected Answer", "Enter Confirm")

		_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if len(answers) != 0 || m.questionRequest == nil {
			t.Errorf("Enter submitted hidden answer: answers=%v request-open=%v", answers, m.questionRequest != nil)
		}
	})

	t.Run("plan-approval", func(t *testing.T) {
		var decisions []PlanApprovalDecision
		m := NewModel()
		m.SetPlanApprovalCallback(func(decision PlanApprovalDecision) { decisions = append(decisions, decision) })
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 2})
		_ = m.handleMessageTypes(PlanApprovalRequestMsg{
			Title: "Dangerous Plan", Steps: []PlanStepInfo{{ID: 1, Title: "Delete target"}},
		})
		assertTinyAuditSetupHides(t, m.View(), "Approve", "Dangerous Plan")

		_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if len(decisions) != 0 || m.planRequest == nil {
			t.Errorf("Enter approved a completely hidden plan: decisions=%v request-open=%v", decisions, m.planRequest != nil)
		}
	})

	t.Run("notification-clear-history", func(t *testing.T) {
		m := NewModel()
		m.toastManager.history = []Toast{{
			ID: 1, Type: ToastWarning, Message: "Selected notification", CreatedAt: time.Now(),
		}}
		m.openNotificationCenter()
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 8})
		assertTinyAuditSetupHides(t, m.View(), "Clear")

		_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
		if got := len(m.toastManager.History()); got != 1 {
			t.Errorf("unadvertised c key destroyed notification history: remaining=%d", got)
		}
	})
}

func TestTinySearchResultsBlocksTargetActionsUntilReadable(t *testing.T) {
	keys := []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune{'e'}},
		{Type: tea.KeyRunes, Runes: []rune{'y'}},
	}
	for width := 1; width <= 20; width++ {
		for height := 1; height <= 4; height++ {
			calls := 0
			m := NewSearchResultsModel(DefaultStyles())
			m.SetActionCallback(func(SearchAction, string, int) { calls++ })
			m.SetActionsLinked(true)
			m.SetResults("needle", "grep", []SearchResult{{
				FilePath: "selected-target.go", LineNumber: 7, Content: "match",
			}})
			m.SetSize(width, height)

			for _, key := range keys {
				before := calls
				var cmd tea.Cmd
				m, cmd = m.Update(key)
				if cmd != nil || calls != before {
					t.Errorf("%dx%d hidden search key %q escaped gate: calls %d->%d cmd=%v", width, height, key.String(), before, calls, cmd != nil)
				}
			}
		}
	}
	for width := 1; width < minSearchTargetWidth; width++ {
		calls := 0
		m := NewSearchResultsModel(DefaultStyles())
		m.SetActionCallback(func(SearchAction, string, int) { calls++ })
		m.SetResults("needle", "grep", []SearchResult{{FilePath: "selected-target.go"}})
		m.SetSize(width, 12)
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd != nil || calls != 0 {
			t.Errorf("%dx12 search action escaped before any target cell fit: calls=%d cmd=%v", width, calls, cmd != nil)
		}
	}

	readableCalls := 0
	readable := NewSearchResultsModel(DefaultStyles())
	readable.SetActionCallback(func(SearchAction, string, int) { readableCalls++ })
	readable.SetResults("needle", "grep", []SearchResult{{FilePath: "selected-target.go"}})
	readable.SetSize(20, 12)
	if view := stripAnsi(readable.View()); !strings.Contains(view, "selected-t") {
		t.Fatalf("20x12 search setup did not render a distinguishable target:\n%s", view)
	}
	_, readableCmd := readable.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if readableCmd == nil || readableCalls != 1 {
		t.Fatalf("20x12 readable search target was over-blocked: calls=%d cmd=%v", readableCalls, readableCmd != nil)
	}

	wideShortCalls := 0
	wideShort := NewSearchResultsModel(DefaultStyles())
	wideShort.SetActionCallback(func(SearchAction, string, int) { wideShortCalls++ })
	wideShort.SetResults("needle", "grep", []SearchResult{{FilePath: "selected-target.go"}})
	wideShort.SetSize(40, 4)
	_, wideShortCmd := wideShort.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if wideShortCmd != nil || wideShortCalls != 0 {
		t.Fatalf("40x4 search action escaped while target row was vertically hidden: calls=%d cmd=%v", wideShortCalls, wideShortCmd != nil)
	}

	// 0x0 is the pre-WindowSize sentinel, not an actual one-cell terminal.
	calls := 0
	sentinel := NewSearchResultsModel(DefaultStyles())
	sentinel.SetActionCallback(func(SearchAction, string, int) { calls++ })
	sentinel.SetResults("needle", "grep", []SearchResult{{FilePath: "target.go"}})
	_, cmd := sentinel.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || calls != 1 {
		t.Fatalf("unspecified search geometry was incorrectly gated: calls=%d cmd=%v", calls, cmd != nil)
	}
}

func TestTinyGitStatusBlocksMutationsUntilReadable(t *testing.T) {
	for width := 1; width <= 20; width++ {
		for height := 1; height <= 4; height++ {
			calls := 0
			m := NewGitStatusModel(DefaultStyles())
			m.SetActionCallback(func(GitAction, []string, string) { calls++ })
			m.SetActionsLinked(true)
			m.SetStatus([]GitFileEntry{{FilePath: "selected-target.go"}}, "main", "", "")
			m.SetSize(width, height)

			for _, key := range []tea.KeyMsg{
				{Type: tea.KeySpace},
				{Type: tea.KeyRunes, Runes: []rune{'a'}},
				{Type: tea.KeyRunes, Runes: []rune{'d'}},
				{Type: tea.KeyRunes, Runes: []rune{'r'}},
			} {
				before := calls
				var cmd tea.Cmd
				m, cmd = m.Update(key)
				if cmd != nil || calls != before || m.confirmReset || m.showDiff {
					t.Errorf("%dx%d hidden git key %q escaped gate: calls %d->%d cmd=%v confirm=%v diff=%v", width, height, key.String(), before, calls, cmd != nil, m.confirmReset, m.showDiff)
				}
			}

			staged := NewGitStatusModel(DefaultStyles())
			stagedCalls := 0
			staged.SetActionCallback(func(GitAction, []string, string) { stagedCalls++ })
			staged.SetActionsLinked(true)
			staged.SetStatus([]GitFileEntry{{FilePath: "staged-target.go", IsStaged: true}}, "main", "", "")
			staged.SetSize(width, height)
			for _, key := range []tea.KeyMsg{
				{Type: tea.KeyRunes, Runes: []rune{'u'}},
				{Type: tea.KeyRunes, Runes: []rune{'c'}},
			} {
				before := stagedCalls
				var cmd tea.Cmd
				staged, cmd = staged.Update(key)
				if cmd != nil || stagedCalls != before {
					t.Errorf("%dx%d hidden staged git key %q escaped gate: calls %d->%d cmd=%v", width, height, key.String(), before, stagedCalls, cmd != nil)
				}
			}

			confirm := NewGitStatusModel(DefaultStyles())
			confirmCalls := 0
			confirm.SetActionCallback(func(GitAction, []string, string) { confirmCalls++ })
			confirm.SetActionsLinked(true)
			confirm.SetStatus([]GitFileEntry{{FilePath: "reset-target.go"}}, "main", "", "")
			confirm.SetSize(width, height)
			confirm.confirmReset = true
			confirm.pendingResetFiles = []string{"reset-target.go"}
			blocked, cmd := confirm.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if cmd != nil || confirmCalls != 0 || !blocked.confirmReset || len(blocked.pendingResetFiles) != 1 {
				t.Errorf("%dx%d hidden reset confirmation escaped gate: calls=%d cmd=%v confirm=%v files=%v", width, height, confirmCalls, cmd != nil, blocked.confirmReset, blocked.pendingResetFiles)
			}
		}
	}
	for width := 1; width < minGitTargetWidth; width++ {
		calls := 0
		m := NewGitStatusModel(DefaultStyles())
		m.SetActionCallback(func(GitAction, []string, string) { calls++ })
		m.SetStatus([]GitFileEntry{{FilePath: "selected-target.go"}}, "main", "", "")
		m.SetSize(width, 12)
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeySpace})
		if cmd != nil || calls != 0 {
			t.Errorf("%dx12 git mutation escaped before any target cell fit: calls=%d cmd=%v", width, calls, cmd != nil)
		}
	}

	readableCalls := 0
	readable := NewGitStatusModel(DefaultStyles())
	readable.SetActionCallback(func(GitAction, []string, string) { readableCalls++ })
	readable.SetStatus([]GitFileEntry{{FilePath: "selected-target.go"}}, "main", "", "")
	readable.SetSize(20, 12)
	if view := stripAnsi(readable.View()); !strings.Contains(view, "selected-t") {
		t.Fatalf("20x12 git setup did not render a distinguishable target:\n%s", view)
	}
	_, readableCmd := readable.Update(tea.KeyMsg{Type: tea.KeySpace})
	if readableCmd == nil || readableCalls != 1 {
		t.Fatalf("20x12 readable git target was over-blocked: calls=%d cmd=%v", readableCalls, readableCmd != nil)
	}

	wideShortCalls := 0
	wideShort := NewGitStatusModel(DefaultStyles())
	wideShort.SetActionCallback(func(GitAction, []string, string) { wideShortCalls++ })
	wideShort.SetStatus([]GitFileEntry{{FilePath: "selected-target.go"}}, "main", "", "")
	wideShort.SetSize(40, 4)
	_, wideShortCmd := wideShort.Update(tea.KeyMsg{Type: tea.KeySpace})
	if wideShortCmd != nil || wideShortCalls != 0 {
		t.Fatalf("40x4 git mutation escaped while target row was vertically hidden: calls=%d cmd=%v", wideShortCalls, wideShortCmd != nil)
	}

	calls := 0
	sentinel := NewGitStatusModel(DefaultStyles())
	sentinel.SetActionCallback(func(GitAction, []string, string) { calls++ })
	sentinel.SetStatus([]GitFileEntry{{FilePath: "target.go"}}, "main", "", "")
	_, cmd := sentinel.Update(tea.KeyMsg{Type: tea.KeySpace})
	if cmd == nil || calls != 1 {
		t.Fatalf("unspecified git geometry was incorrectly gated: calls=%d cmd=%v", calls, cmd != nil)
	}
}

func TestTinyNotificationClearRequiresVisibleAffordance(t *testing.T) {
	for width := 1; width <= 20; width++ {
		for height := 1; height <= 8; height++ {
			m := NewModel()
			m.toastManager.history = []Toast{{
				ID: 1, Type: ToastWarning, Message: "Selected notification", CreatedAt: time.Now(),
			}}
			m.openNotificationCenter()
			m.applyResize(&tea.WindowSizeMsg{Width: width, Height: height})
			_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
			if got := len(m.toastManager.History()); got != 1 {
				t.Errorf("%dx%d invisible c cleared notification history: remaining=%d", width, height, got)
			}
		}
	}

	visible := NewModel()
	visible.toastManager.history = []Toast{{ID: 1, Message: "Earlier", CreatedAt: time.Now()}}
	visible.openNotificationCenter()
	visible.applyResize(&tea.WindowSizeMsg{Width: 40, Height: 8})
	if view := stripAnsi(visible.View()); !strings.Contains(view, "c Clear") {
		t.Fatalf("wide notification surface did not advertise clear:\n%s", view)
	}
	_ = visible.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if got := len(visible.toastManager.History()); got != 0 {
		t.Fatalf("visible clear action was blocked: remaining=%d", got)
	}

	sentinel := NewModel()
	sentinel.toastManager.history = []Toast{{ID: 1, Message: "Earlier", CreatedAt: time.Now()}}
	sentinel.openNotificationCenter()
	_ = sentinel.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if got := len(sentinel.toastManager.History()); got != 0 {
		t.Fatalf("unspecified notification geometry was incorrectly gated: remaining=%d", got)
	}
}

func assertTinyAuditSetupHides(t *testing.T, rendered string, hidden ...string) {
	t.Helper()
	plain := stripAnsi(rendered)
	if !strings.Contains(plain, "Esc") {
		t.Fatalf("audit setup lost safe recovery together with the hidden target:\n%s", plain)
	}
	for _, text := range hidden {
		if strings.Contains(plain, text) {
			t.Fatalf("audit setup unexpectedly rendered %q:\n%s", text, plain)
		}
	}
}
