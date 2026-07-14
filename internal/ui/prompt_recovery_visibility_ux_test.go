package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestConstrainedSafetyPromptsKeepEscapeRecoveryVisible(t *testing.T) {
	for _, width := range []int{24, 32, 40} {
		t.Run(fmt.Sprintf("permission-%d", width), func(t *testing.T) {
			m := NewModel()
			m.applyResize(&tea.WindowSizeMsg{Width: width, Height: 12})
			m.state = StatePermissionPrompt
			m.permRequest = &PermissionRequestMsg{ToolName: "bash", RiskLevel: "high", Reason: "Modify files"}
			assertPromptRecoveryVisible(t, m.View(), "Esc Deny")
		})

		t.Run(fmt.Sprintf("question-%d", width), func(t *testing.T) {
			m := NewModel()
			m.applyResize(&tea.WindowSizeMsg{Width: width, Height: 12})
			m.state = StateQuestionPrompt
			m.questionRequest = &QuestionRequestMsg{Question: "Choose", Options: []string{"Safe", "Fast"}}
			assertPromptRecoveryVisible(t, m.View(), "Esc Cancel")
		})

		t.Run(fmt.Sprintf("question-input-%d", width), func(t *testing.T) {
			m := NewModel()
			m.applyResize(&tea.WindowSizeMsg{Width: width, Height: 12})
			m.state = StateQuestionPrompt
			m.questionRequest = &QuestionRequestMsg{Question: "Explain"}
			m.questionInputModel = NewInputModel(m.styles, m.workDir)
			assertPromptRecoveryVisible(t, m.View(), "Esc Cancel")
		})

		t.Run(fmt.Sprintf("plan-%d", width), func(t *testing.T) {
			m := NewModel()
			m.applyResize(&tea.WindowSizeMsg{Width: width, Height: 12})
			m.state = StatePlanApproval
			m.planRequest = &PlanApprovalRequestMsg{Title: "Review plan"}
			assertPromptRecoveryVisible(t, m.View(), "Esc Cancel")
		})

		t.Run(fmt.Sprintf("plan-feedback-%d", width), func(t *testing.T) {
			m := NewModel()
			m.applyResize(&tea.WindowSizeMsg{Width: width, Height: 12})
			m.state = StatePlanApproval
			m.planRequest = &PlanApprovalRequestMsg{Title: "Review plan"}
			m.planFeedbackMode = true
			m.planFeedbackInput = NewInputModel(m.styles, m.workDir)
			assertPromptRecoveryVisible(t, m.View(), "Esc Back")
		})

		t.Run(fmt.Sprintf("api-key-%d", width), func(t *testing.T) {
			m := NewModel()
			m.SetKeyEntrySubmitCallback(func(string, string) {})
			m.applyResize(&tea.WindowSizeMsg{Width: width, Height: 12})
			m.openKeyEntry(OpenKeyEntryMsg{Provider: "glm", DisplayName: "GLM"})
			assertPromptRecoveryVisible(t, m.View(), "Esc Cancel")
		})

		t.Run(fmt.Sprintf("settings-%d", width), func(t *testing.T) {
			m := NewModel()
			m.SetSettingToggleCallback(func(string, bool) {})
			m.applyResize(&tea.WindowSizeMsg{Width: width, Height: 12})
			m.openSettings(OpenSettingsMsg{Items: []SettingItem{{Key: "safe", Name: "Safe mode", Desc: "Require confirmations"}}})
			assertPromptRecoveryVisible(t, m.View(), "Esc Close")
		})

		t.Run(fmt.Sprintf("model-selector-%d", width), func(t *testing.T) {
			m := NewModel()
			m.SetModelSelectCallback(func(string) {})
			m.applyResize(&tea.WindowSizeMsg{Width: width, Height: 12})
			m.availableModels = []ModelInfo{{ID: "fast", Name: "Fast", Description: "Fast model"}}
			m.state = StateModelSelector
			assertPromptRecoveryVisible(t, m.View(), "Esc Close")
		})
	}
}

func TestConstrainedCommandPaletteKeepsCloseAndBackVisible(t *testing.T) {
	for _, width := range []int{34, 40, 50} {
		t.Run(fmt.Sprintf("list-%d", width), func(t *testing.T) {
			m := NewModel()
			m.applyResize(&tea.WindowSizeMsg{Width: width, Height: 14})
			m.commandPalette.commands = []EnhancedPaletteCommand{{
				Name: "help", Shortcut: "/help", Description: "Show help", Type: CommandTypeSlash, Enabled: true,
			}}
			m.commandPalette.Show()
			m.state = StateCommandPalette
			assertPromptRecoveryVisible(t, m.View(), "Esc Close")
		})

		t.Run(fmt.Sprintf("argument-%d", width), func(t *testing.T) {
			m := NewModel()
			m.applyResize(&tea.WindowSizeMsg{Width: width, Height: 14})
			m.commandPalette.BeginArgEntry(EnhancedPaletteCommand{
				Name: "open", Shortcut: "/open", Description: "Open file", ArgHint: "<file>", Type: CommandTypeSlash, Enabled: true,
			})
			m.commandPalette.visible = true
			m.state = StateCommandPalette
			assertPromptRecoveryVisible(t, m.View(), "Esc Back")
		})
	}
}

func assertPromptRecoveryVisible(t *testing.T, rendered, recovery string) {
	t.Helper()
	plain := stripAnsi(rendered)
	if !strings.Contains(plain, recovery) {
		t.Fatalf("constrained prompt clipped recovery %q:\n%s", recovery, plain)
	}
}
