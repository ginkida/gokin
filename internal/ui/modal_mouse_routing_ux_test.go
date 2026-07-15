package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func prepareScrollableBackground(m *Model) int {
	m.output.SetSize(80, 5)
	for i := range 30 {
		m.output.AppendLine(fmt.Sprintf("background %02d", i))
	}
	m.output.viewport.SetYOffset(7)
	return m.output.viewport.YOffset
}

func wheelDown() tea.MouseMsg {
	return tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}
}

func TestModalMouseWheelNavigatesVisibleListWithoutScrollingBackground(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*Model)
		index func(*Model) int
	}{
		{
			name: "settings",
			setup: func(m *Model) {
				m.openSettings(OpenSettingsMsg{Items: []SettingItem{{Key: "one"}, {Key: "two"}}})
			},
			index: func(m *Model) int { return m.settingsCursor },
		},
		{
			name: "model selector",
			setup: func(m *Model) {
				m.availableModels = []ModelInfo{{ID: "one"}, {ID: "two"}}
				m.state = StateModelSelector
			},
			index: func(m *Model) int { return m.modelSelectedIndex },
		},
		{
			name: "command palette",
			setup: func(m *Model) {
				m.commandPalette.commands = []EnhancedPaletteCommand{
					{Name: "one", Enabled: true},
					{Name: "two", Enabled: true},
				}
				m.commandPalette.filtered = append([]EnhancedPaletteCommand(nil), m.commandPalette.commands...)
				m.commandPalette.visible = true
				m.state = StateCommandPalette
			},
			index: func(m *Model) int { return m.commandPalette.selected },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel()
			backgroundOffset := prepareScrollableBackground(m)
			tt.setup(m)

			updated, _ := m.Update(wheelDown())
			got := updated.(Model)
			if index := tt.index(&got); index != 1 {
				t.Fatalf("visible selection=%d, want 1", index)
			}
			if got.output.viewport.YOffset != backgroundOffset {
				t.Fatalf("hidden output moved from %d to %d", backgroundOffset, got.output.viewport.YOffset)
			}
		})
	}
}

func TestModalMouseWheelReachesEmbeddedDiffViewport(t *testing.T) {
	m := NewModel()
	backgroundOffset := prepareScrollableBackground(m)
	m.diffPreview.SetSize(70, 8)
	m.diffPreview.viewport.SetContent(strings.Repeat("visible diff line\n", 40))
	m.diffPreview.viewport.GotoTop()
	m.state = StateDiffPreview

	updated, _ := m.Update(wheelDown())
	got := updated.(Model)
	if got.diffPreview.viewport.YOffset == 0 {
		t.Fatal("wheel event did not reach visible diff viewport")
	}
	if got.output.viewport.YOffset != backgroundOffset {
		t.Fatalf("hidden output moved from %d to %d", backgroundOffset, got.output.viewport.YOffset)
	}
}

func TestUnsupportedModalMouseClickDoesNotTouchBackground(t *testing.T) {
	m := NewModel()
	backgroundOffset := prepareScrollableBackground(m)
	m.openSettings(OpenSettingsMsg{Items: []SettingItem{{Key: "one"}, {Key: "two"}}})

	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	got := updated.(Model)
	if got.settingsCursor != 0 || got.output.viewport.YOffset != backgroundOffset {
		t.Fatalf("unsupported click changed state: cursor=%d output=%d", got.settingsCursor, got.output.viewport.YOffset)
	}
}

func TestLongPlanWheelScrollsStepsWithoutChangingDecision(t *testing.T) {
	m := NewModel()
	m.width, m.height = 72, 14
	backgroundOffset := prepareScrollableBackground(m)
	m.state = StatePlanApproval
	m.planSelectedOption = int(PlanRejected)
	m.planRequest = &PlanApprovalRequestMsg{Title: "Review carefully"}
	for i := range 12 {
		m.planRequest.Steps = append(m.planRequest.Steps, PlanStepInfo{ID: i + 1, Title: fmt.Sprintf("Step %02d", i+1)})
	}

	updated, _ := m.Update(wheelDown())
	got := updated.(Model)
	if got.planStepScroll != 3 || got.planSelectedOption != int(PlanRejected) {
		t.Fatalf("wheel changed decision or wrong step scroll: scroll=%d decision=%d", got.planStepScroll, got.planSelectedOption)
	}
	if got.output.viewport.YOffset != backgroundOffset {
		t.Fatalf("long-plan wheel moved hidden output from %d to %d", backgroundOffset, got.output.viewport.YOffset)
	}
	view := stripAnsi(got.renderPlanApproval())
	if !strings.Contains(view, "Step 4: Step 04") || !strings.Contains(view, "↑ 3 earlier step(s)") {
		t.Fatalf("wheel-scrolled plan lacks position context:\n%s", view)
	}

	updated, _ = got.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	got = updated.(Model)
	if got.planStepScroll != 0 || got.planSelectedOption != int(PlanRejected) {
		t.Fatalf("wheel-up did not clamp safely: scroll=%d decision=%d", got.planStepScroll, got.planSelectedOption)
	}
}

func TestShortPlanWheelNavigatesDecisionRows(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	m.state = StatePlanApproval
	m.planRequest = &PlanApprovalRequestMsg{
		Title: "Short plan",
		Steps: []PlanStepInfo{{ID: 1, Title: "Only step"}},
	}
	m.planSelectedOption = int(PlanApproved)

	updated, _ := m.Update(wheelDown())
	got := updated.(Model)
	if got.planSelectedOption != int(PlanRejected) || got.planStepScroll != 0 {
		t.Fatalf("short-plan wheel selection=%d scroll=%d, want Reject/0", got.planSelectedOption, got.planStepScroll)
	}
}
