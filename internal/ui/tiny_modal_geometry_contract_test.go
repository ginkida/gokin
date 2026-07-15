package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func tinyModalGeometryName(width, height int, key tea.KeyMsg) string {
	return fmt.Sprintf("%dx%d/%s", width, height, key.String())
}

func assertTinyModalResizeContract(t *testing.T, m *Model, unsafeHints ...string) {
	t.Helper()
	view := stripAnsi(m.View())
	if !strings.Contains(view, "Resize") && !strings.Contains(view, "↔") {
		t.Fatalf("unreadable modal lacks resize/recovery signal: %q", view)
	}
	hints := plainShortcutHints(m.contextualShortcutHintPairs())
	if !strings.Contains(hints, "Resize") {
		t.Fatalf("unreadable contextual hints lack Resize: %q", hints)
	}
	for _, unsafe := range unsafeHints {
		if strings.Contains(hints, unsafe) {
			t.Fatalf("unreadable contextual hints advertise %q: %q", unsafe, hints)
		}
	}
}

func assertReadableModalContract(t *testing.T, m *Model, target, primary string) {
	t.Helper()
	view := stripAnsi(m.View())
	for _, want := range []string{target, primary} {
		if !strings.Contains(view, want) {
			t.Fatalf("readable geometry hides %q:\n%s", want, view)
		}
	}
}

func TestTinyModalPrimaryActionsFollowReadableGeometryMatrix(t *testing.T) {
	for width := 1; width <= 20; width++ {
		for height := 1; height <= 4; height++ {
			for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeySpace}} {
				t.Run("settings/"+tinyModalGeometryName(width, height, key), func(t *testing.T) {
					calls := 0
					m := NewModel()
					m.SetSettingToggleCallback(func(string, bool) { calls++ })
					m.applyResize(&tea.WindowSizeMsg{Width: width, Height: height})
					m.openSettings(OpenSettingsMsg{Items: []SettingItem{{Key: "danger", Name: "Danger mode"}}})
					if m.settingsPrimaryActionReadable() {
						assertReadableModalContract(t, m, "Dange", "Space")
						return
					}
					assertTinyModalResizeContract(t, m, "Space Toggle")
					_ = m.handleSettingsKeys(key)
					if calls != 0 || m.settingsItems[0].On || m.settingsItems[0].Pending {
						t.Fatalf("hidden setting toggled: calls=%d on=%v pending=%v", calls, m.settingsItems[0].On, m.settingsItems[0].Pending)
					}
				})
			}

			for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeySpace}, {Type: tea.KeyRunes, Runes: []rune{'1'}}} {
				t.Run("model/"+tinyModalGeometryName(width, height, key), func(t *testing.T) {
					calls := 0
					m := NewModel()
					m.SetModelSelectCallback(func(string) { calls++ })
					m.SetAvailableModels([]ModelInfo{{ID: "target", Name: "Target Model"}})
					m.applyResize(&tea.WindowSizeMsg{Width: width, Height: height})
					m.openModelSelector()
					if m.modelSelectorPrimaryActionReadable() {
						assertReadableModalContract(t, m, "Target", "Enter")
						return
					}
					assertTinyModalResizeContract(t, m, "Enter Select", "Enter Keep current")
					_ = m.handleModelSelectorKeys(key)
					if calls != 0 || m.modelSwitchPending != "" || m.state != StateModelSelector {
						t.Fatalf("hidden model selected: calls=%d pending=%q state=%v", calls, m.modelSwitchPending, m.state)
					}
				})
			}

			t.Run(fmt.Sprintf("api-key/%dx%d", width, height), func(t *testing.T) {
				calls := 0
				m := NewModel()
				m.SetKeyEntrySubmitCallback(func(string, string) { calls++ })
				m.applyResize(&tea.WindowSizeMsg{Width: width, Height: height})
				m.openKeyEntry(OpenKeyEntryMsg{Provider: "provider", DisplayName: "Provider"})
				m.keyEntryInput.SetValue("secret-value")
				if m.keyEntryPrimaryActionReadable() {
					assertReadableModalContract(t, m, "•", "Enter")
					return
				}
				assertTinyModalResizeContract(t, m, "Enter Save")
				_ = m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEnter})
				if calls != 0 || m.keyEntryPendingProvider != "" || m.state != StateAPIKeyEntry {
					t.Fatalf("hidden key submitted: calls=%d pending=%q state=%v", calls, m.keyEntryPendingProvider, m.state)
				}
			})

			for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeySpace}, {Type: tea.KeyRunes, Runes: []rune{'1'}}} {
				t.Run("question/"+tinyModalGeometryName(width, height, key), func(t *testing.T) {
					calls := 0
					m := NewModel()
					m.SetQuestionCallback(func(string) { calls++ })
					m.applyResize(&tea.WindowSizeMsg{Width: width, Height: height})
					_ = m.handleMessageTypes(QuestionRequestMsg{Question: "Choose", Options: []string{"Selected answer", "Other answer"}})
					if m.questionPrimaryActionReadable() {
						assertReadableModalContract(t, m, "Selec", "Enter")
						return
					}
					assertTinyModalResizeContract(t, m, "Enter Confirm")
					_ = m.handleQuestionPromptKeys(key)
					if calls != 0 || m.questionRequest == nil || m.state != StateQuestionPrompt {
						t.Fatalf("hidden answer submitted: calls=%d request=%v state=%v", calls, m.questionRequest != nil, m.state)
					}
				})
			}

			for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeySpace}, {Type: tea.KeyRunes, Runes: []rune{'y'}}, {Type: tea.KeyRunes, Runes: []rune{'1'}}} {
				t.Run("plan/"+tinyModalGeometryName(width, height, key), func(t *testing.T) {
					calls := 0
					m := NewModel()
					m.SetPlanApprovalCallback(func(PlanApprovalDecision) { calls++ })
					m.applyResize(&tea.WindowSizeMsg{Width: width, Height: height})
					_ = m.handleMessageTypes(PlanApprovalRequestMsg{Title: "Plan", Steps: []PlanStepInfo{{ID: 1, Title: "Step"}}})
					if m.planPrimaryActionReadable() {
						assertReadableModalContract(t, m, "Approve", "Enter")
						return
					}
					assertTinyModalResizeContract(t, m, "y Approve", "Enter Confirm")
					_ = m.handlePlanApprovalKeys(key)
					if calls != 0 || m.planRequest == nil || m.state != StatePlanApproval {
						t.Fatalf("hidden plan approved: calls=%d request=%v state=%v", calls, m.planRequest != nil, m.state)
					}
				})
			}
		}
	}
}

func TestModalPrimaryActionReadableBoundariesRemainUsable(t *testing.T) {
	t.Run("settings", func(t *testing.T) {
		calls := 0
		m := NewModel()
		m.SetSettingToggleCallback(func(string, bool) { calls++ })
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 4})
		m.openSettings(OpenSettingsMsg{Items: []SettingItem{{Key: "feature", Name: "Feature"}}})
		readable := m.settingsPrimaryActionReadable()
		assertReadableModalContract(t, m, "Featu", "Space")
		_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeySpace})
		if !readable || calls != 1 {
			t.Fatalf("readable settings action blocked: readable=%v calls=%d", readable, calls)
		}
	})

	t.Run("model", func(t *testing.T) {
		calls := 0
		m := NewModel()
		m.SetModelSelectCallback(func(string) { calls++ })
		m.SetAvailableModels([]ModelInfo{{ID: "target", Name: "Target"}})
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 3})
		m.openModelSelector()
		assertReadableModalContract(t, m, "Target", "Enter")
		_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if calls != 1 {
			t.Fatalf("readable model action blocked: calls=%d", calls)
		}
	})

	t.Run("api-key", func(t *testing.T) {
		calls := 0
		m := NewModel()
		m.SetKeyEntrySubmitCallback(func(string, string) { calls++ })
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 10})
		m.openKeyEntry(OpenKeyEntryMsg{Provider: "provider"})
		m.keyEntryInput.SetValue("secret")
		assertReadableModalContract(t, m, "•", "Enter")
		_ = m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if calls != 1 {
			t.Fatalf("readable key action blocked: calls=%d", calls)
		}
	})

	t.Run("question", func(t *testing.T) {
		calls := 0
		m := NewModel()
		m.SetQuestionCallback(func(string) { calls++ })
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 4})
		_ = m.handleMessageTypes(QuestionRequestMsg{Question: "Choose", Options: []string{"Answer"}})
		assertReadableModalContract(t, m, "Answer", "Enter")
		_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if calls != 1 {
			t.Fatalf("readable question action blocked: calls=%d", calls)
		}
	})

	t.Run("plan", func(t *testing.T) {
		calls := 0
		m := NewModel()
		m.SetPlanApprovalCallback(func(PlanApprovalDecision) { calls++ })
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 8})
		_ = m.handleMessageTypes(PlanApprovalRequestMsg{Title: "Plan", Steps: []PlanStepInfo{{ID: 1, Title: "Step"}}})
		assertReadableModalContract(t, m, "Approve", "Enter")
		_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if calls != 1 {
			t.Fatalf("readable plan action blocked: calls=%d", calls)
		}
	})
}

func TestModalPrimaryActionGeometryPreservesUnspecifiedSizeSentinel(t *testing.T) {
	m := NewModel()
	m.width, m.height = 0, 0
	m.settingsItems = []SettingItem{{Key: "feature"}}
	m.availableModels = []ModelInfo{{ID: "model"}}
	m.questionRequest = &QuestionRequestMsg{Options: []string{"answer"}}
	m.planRequest = &PlanApprovalRequestMsg{Steps: []PlanStepInfo{{ID: 1}}}
	m.keyEntryProvider = "provider"
	m.SetKeyEntrySubmitCallback(func(string, string) {})

	if !m.settingsPrimaryActionReadable() || !m.modelSelectorPrimaryActionReadable() ||
		!m.questionPrimaryActionReadable() || !m.planPrimaryActionReadable() || !m.keyEntryPrimaryActionReadable() {
		t.Fatal("0x0 pre-size sentinel was treated as explicit unreadable geometry")
	}
}
