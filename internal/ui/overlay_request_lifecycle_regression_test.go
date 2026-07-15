package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

const requestLifecycleDraft = "follow-up draft"

type requestLifecycleSurface struct {
	name            string
	state           State
	returnState     func(*Model) State
	close           func(*Model)
	assertPreserved func(*testing.T, *Model)
}

func requestLifecycleUpdate(m *Model, msg tea.Msg) {
	updated, _ := m.Update(msg)
	*m = updated.(Model)
}

func openRequestLifecycleSurface(t *testing.T, m *Model, name string) requestLifecycleSurface {
	t.Helper()
	surface := requestLifecycleSurface{name: name}

	switch name {
	case "settings":
		requestLifecycleUpdate(m, OpenSettingsMsg{Items: []SettingItem{
			{Key: "sandbox", Name: "Sandbox"},
			{Key: "permissions", Name: "Permissions"},
		}})
		m.settingsCursor = 1
		surface.state = StateSettings
		surface.returnState = func(m *Model) State { return m.settingsReturnState }
		surface.close = func(m *Model) {
			requestLifecycleUpdate(m, tea.KeyMsg{Type: tea.KeyEscape})
		}
		surface.assertPreserved = func(t *testing.T, m *Model) {
			t.Helper()
			if m.settingsCursor != 1 || len(m.settingsItems) != 2 {
				t.Errorf("settings selection was lost: cursor=%d items=%d", m.settingsCursor, len(m.settingsItems))
			}
		}

	case "api key":
		m.SetKeyEntrySubmitCallback(func(string, string) {})
		requestLifecycleUpdate(m, OpenKeyEntryMsg{Provider: "glm", DisplayName: "GLM"})
		m.keyEntryInput.SetValue("sk-preserve-until-close")
		surface.state = StateAPIKeyEntry
		surface.returnState = func(m *Model) State { return m.keyEntryReturnState }
		surface.close = func(m *Model) {
			requestLifecycleUpdate(m, tea.KeyMsg{Type: tea.KeyEscape})
		}
		surface.assertPreserved = func(t *testing.T, m *Model) {
			t.Helper()
			if got := m.keyEntryInput.Value(); got != "sk-preserve-until-close" {
				t.Errorf("API-key draft was changed while modal stayed open: %q", got)
			}
		}

	case "transient palette":
		m.ShowCommandPalette()
		m.commandPalette.SetQuery("Toggle")
		selected := 0
		for i, command := range m.commandPalette.filtered {
			if command.ActionID == paletteActionCompactMode {
				selected = i
				break
			}
		}
		m.commandPalette.selected = selected
		selectedCommand := m.commandPalette.GetSelected()
		if selectedCommand == nil {
			t.Fatal("setup transient palette: no selected command")
		}
		selectedID := selectedCommand.ActionID
		surface.state = StateCommandPalette
		surface.returnState = func(m *Model) State { return m.transientOverlayReturnState }
		surface.close = func(m *Model) {
			requestLifecycleUpdate(m, tea.KeyMsg{Type: tea.KeyEscape})
		}
		surface.assertPreserved = func(t *testing.T, m *Model) {
			t.Helper()
			selected := m.commandPalette.GetSelected()
			if m.commandPalette.GetQuery() != "Toggle" || selected == nil || selected.ActionID != selectedID {
				t.Errorf("palette context was lost: query=%q selected=%+v", m.commandPalette.GetQuery(), selected)
			}
		}

	case "workspace browser":
		dir := t.TempDir()
		target := filepath.Join(dir, "selected.go")
		if err := os.WriteFile(target, []byte("package selected\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		requestLifecycleUpdate(m, FileBrowserRequestMsg{StartPath: dir})
		for i, entry := range m.fileBrowser.entries {
			if entry.Path == target {
				m.fileBrowser.selectedIndex = i
				break
			}
		}
		surface.state = StateFileBrowser
		surface.returnState = func(m *Model) State { return m.workspaceOverlayReturnState }
		surface.close = func(m *Model) {
			requestLifecycleUpdate(m, FileBrowserActionMsg{Action: FileBrowserActionClose})
		}
		surface.assertPreserved = func(t *testing.T, m *Model) {
			t.Helper()
			if len(m.fileBrowser.entries) == 0 || m.fileBrowser.entries[m.fileBrowser.selectedIndex].Path != target {
				t.Errorf("workspace selection was lost: selected=%d entries=%d", m.fileBrowser.selectedIndex, len(m.fileBrowser.entries))
			}
		}

	case "batch summary":
		requestLifecycleUpdate(m, ProgressCompleteMsg{TotalItems: 2, SuccessCount: 1, FailureCount: 1})
		surface.state = StateBatchProgress
		surface.returnState = func(m *Model) State { return m.progressReturnState }
		surface.close = func(m *Model) {
			requestLifecycleUpdate(m, CloseOverlayMsg{})
		}
		surface.assertPreserved = func(t *testing.T, m *Model) {
			t.Helper()
			if !m.progressActive || !m.progressModel.isComplete || m.progressModel.total != 2 {
				t.Errorf("batch summary was lost: active=%v complete=%v total=%d", m.progressActive, m.progressModel.isComplete, m.progressModel.total)
			}
		}

	default:
		t.Fatalf("unknown lifecycle surface %q", name)
	}

	if m.state != surface.state {
		t.Fatalf("setup %s: state=%v want=%v", name, m.state, surface.state)
	}
	if surface.returnState(m) != StateStreaming {
		t.Fatalf("setup %s: return=%v want streaming", name, surface.returnState(m))
	}
	return surface
}

func requestLifecycleSurfaceNames() []string {
	return []string{"settings", "api key", "transient palette", "workspace browser", "batch summary"}
}

func assertRequestLifecycleContentPreserved(t *testing.T, m *Model, surface requestLifecycleSurface) {
	t.Helper()
	if got := m.input.Value(); got != requestLifecycleDraft {
		t.Errorf("%s lost the composer draft: %q", surface.name, got)
	}
	surface.assertPreserved(t, m)
}

func TestWatchdogTimeoutSettlesUnderlyingTurnWithoutClosingOverlay(t *testing.T) {
	for _, name := range requestLifecycleSurfaceNames() {
		t.Run(name, func(t *testing.T) {
			m := NewModel()
			m.width, m.height = 100, 30
			m.state = StateStreaming
			m.input.textarea.SetValue(requestLifecycleDraft)
			cancelled := 0
			m.SetCancelCallback(func() { cancelled++ })
			surface := openRequestLifecycleSurface(t, m, name)
			m.streamTimeout = time.Second
			m.streamStartTime = time.Now().Add(-2 * m.streamTimeout)
			m.lastActivityTime = m.streamStartTime

			requestLifecycleUpdate(m, spinner.TickMsg{})

			if m.state != surface.state {
				t.Errorf("watchdog closed %s: state=%v want=%v", name, m.state, surface.state)
			}
			if got := surface.returnState(m); got != StateInput {
				t.Errorf("watchdog left stale underlying state for %s: %v", name, got)
			}
			if cancelled != 1 {
				t.Errorf("watchdog cancel calls for %s=%d, want 1", name, cancelled)
			}
			if !m.streamStartTime.IsZero() {
				t.Errorf("watchdog kept timeout clock for %s: %v", name, m.streamStartTime)
			}
			assertRequestLifecycleContentPreserved(t, m, surface)

			surface.close(m)
			if m.state != StateInput {
				t.Errorf("closing timed-out %s restored stale activity: %v", name, m.state)
			}
			if got := m.input.Value(); got != requestLifecycleDraft {
				t.Errorf("closing timed-out %s lost composer draft: %q", name, got)
			}
			if name == "api key" && (m.keyEntryInput.Value() != "" || m.keyEntryProvider != "") {
				t.Errorf("closing timed-out API-key modal retained secret/provider: value=%q provider=%q", m.keyEntryInput.Value(), m.keyEntryProvider)
			}
		})
	}
}

func TestSlowWarningUsesUnderlyingActiveTurnWithoutClosingOverlay(t *testing.T) {
	for _, name := range requestLifecycleSurfaceNames() {
		t.Run(name, func(t *testing.T) {
			m := NewModel()
			m.width, m.height = 100, 30
			m.state = StateStreaming
			m.input.textarea.SetValue(requestLifecycleDraft)
			surface := openRequestLifecycleSurface(t, m, name)
			m.streamStartTime = time.Now()
			m.lastActivityTime = time.Now().Add(-slowOperationThreshold - time.Second)
			m.slowWarningShown = false

			requestLifecycleUpdate(m, spinner.TickMsg{})

			if m.state != surface.state || surface.returnState(m) != StateStreaming {
				t.Errorf("slow warning changed %s lifecycle: state=%v return=%v", name, m.state, surface.returnState(m))
			}
			if !m.slowWarningShown {
				t.Errorf("slow warning ignored active turn under %s", name)
			}
			assertRequestLifecycleContentPreserved(t, m, surface)
		})
	}
}

func TestPhaseLabelUpdatesUnderlyingTurnWithoutClobberingOverlay(t *testing.T) {
	for _, name := range requestLifecycleSurfaceNames() {
		t.Run(name, func(t *testing.T) {
			m := NewModel()
			m.width, m.height = 100, 30
			m.state = StateStreaming
			m.input.textarea.SetValue(requestLifecycleDraft)
			surface := openRequestLifecycleSurface(t, m, name)

			requestLifecycleUpdate(m, StatusUpdateMsg{
				Type:    StatusInfo,
				Message: "Quality gate",
				Details: map[string]any{"phaseLabel": "Quality gate 1/2", "silent": true},
			})

			if m.state != surface.state {
				t.Errorf("phase label clobbered %s: state=%v want=%v", name, m.state, surface.state)
			}
			if got := surface.returnState(m); got != StateProcessing {
				t.Errorf("phase label did not update underlying %s state: %v", name, got)
			}
			if m.processingLabel != "Quality gate 1/2" || m.streamStartTime.IsZero() || m.lastActivityTime.IsZero() {
				t.Errorf("phase label did not refresh live activity for %s: label=%q stream=%v activity=%v", name, m.processingLabel, m.streamStartTime, m.lastActivityTime)
			}
			assertRequestLifecycleContentPreserved(t, m, surface)

			surface.close(m)
			if m.state != StateProcessing {
				t.Errorf("closing %s lost phase-label processing state: %v", name, m.state)
			}
		})
	}
}

func TestPhaseLabelNeverClobbersBlockingPrompt(t *testing.T) {
	m := NewModel()
	m.width, m.height = 100, 30
	m.state = StateStreaming
	requestLifecycleUpdate(m, PermissionRequestMsg{ID: "req-1", ToolName: "bash"})
	request := m.permRequest

	requestLifecycleUpdate(m, StatusUpdateMsg{
		Type:    StatusInfo,
		Message: "Quality gate",
		Details: map[string]any{"phaseLabel": "Quality gate 1/2", "silent": true},
	})

	if m.state != StatePermissionPrompt || m.permRequest != request {
		t.Fatalf("phase label clobbered blocking permission prompt: state=%v request=%p want=%p", m.state, m.permRequest, request)
	}
}

func TestBatchProgressUpdateRefreshesUnderlyingWatchdogActivity(t *testing.T) {
	m := NewModel()
	m.width, m.height = 100, 30
	m.state = StateProcessing
	m.streamTimeout = time.Second
	m.streamStartTime = time.Now().Add(-2 * m.streamTimeout)
	m.lastActivityTime = m.streamStartTime
	m.slowWarningShown = true
	cancelled := 0
	m.SetCancelCallback(func() { cancelled++ })

	before := time.Now()
	requestLifecycleUpdate(m, ProgressUpdateMsg{
		Current:     1,
		Total:       3,
		CurrentItem: "still-working.go",
		Message:     "Batch is making progress",
	})
	if m.state != StateBatchProgress || m.progressReturnState != StateProcessing || !m.progressActive {
		t.Fatalf("setup: progress did not open over processing: state=%v return=%v active=%v", m.state, m.progressReturnState, m.progressActive)
	}
	if m.streamStartTime.Before(before) || m.lastActivityTime.Before(before) || m.slowWarningShown {
		t.Errorf("batch heartbeat did not refresh request activity: stream=%v activity=%v slow=%v before=%v", m.streamStartTime, m.lastActivityTime, m.slowWarningShown, before)
	}

	requestLifecycleUpdate(m, spinner.TickMsg{})
	if cancelled != 0 || m.state != StateBatchProgress || m.progressReturnState != StateProcessing {
		t.Errorf("watchdog cancelled a batch that just reported progress: cancelled=%d state=%v return=%v", cancelled, m.state, m.progressReturnState)
	}
	if m.progressModel.current != 1 || m.progressModel.currentItem != "still-working.go" {
		t.Errorf("watchdog lost live batch progress: current=%d item=%q", m.progressModel.current, m.progressModel.currentItem)
	}
}

func TestPlanProgressUpdateRefreshesUnderlyingWatchdogActivity(t *testing.T) {
	m := NewModel()
	m.width, m.height = 100, 30
	m.state = StateProcessing
	m.StartPlanExecution("plan-1", "Lifecycle audit", "", []PlanStepInfo{{ID: 1, Title: "Verify heartbeat"}})
	m.streamTimeout = time.Second
	m.streamStartTime = time.Now().Add(-2 * m.streamTimeout)
	m.lastActivityTime = m.streamStartTime
	m.slowWarningShown = true
	cancelled := 0
	m.SetCancelCallback(func() { cancelled++ })

	before := time.Now()
	requestLifecycleUpdate(m, PlanProgressMsg{
		PlanID:        "plan-1",
		CurrentStepID: 1,
		CurrentTitle:  "Verify heartbeat",
		TotalSteps:    1,
		Status:        "in_progress",
	})
	if m.state != StateProcessing || !m.planProgressMode || m.planProgress == nil {
		t.Fatalf("setup: plan progress changed active processing: state=%v mode=%v progress=%v", m.state, m.planProgressMode, m.planProgress)
	}
	if m.streamStartTime.Before(before) || m.lastActivityTime.Before(before) || m.slowWarningShown {
		t.Errorf("plan heartbeat did not refresh request activity: stream=%v activity=%v slow=%v before=%v", m.streamStartTime, m.lastActivityTime, m.slowWarningShown, before)
	}

	requestLifecycleUpdate(m, spinner.TickMsg{})
	if cancelled != 0 || m.state != StateProcessing {
		t.Errorf("watchdog cancelled a plan that just reported progress: cancelled=%d state=%v", cancelled, m.state)
	}
	if m.planProgressPanel.currentStepID != 1 {
		t.Errorf("watchdog lost live plan step: current=%d", m.planProgressPanel.currentStepID)
	}
}
