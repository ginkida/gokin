package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestCompactModeSurvivesResizeAndLimitsVisibleTranscript(t *testing.T) {
	m := NewModel()
	m.state = StateInput
	m.applyResize(&tea.WindowSizeMsg{Width: 90, Height: 30})
	if got := m.output.viewport.Height; got != 25 {
		t.Fatalf("normal viewport height=%d, want 25", got)
	}
	for i := range 60 {
		m.output.AppendLine(fmt.Sprintf("transcript line %02d", i))
	}

	m.toggleCompactMode()
	if !m.CompactMode || m.output.viewport.Height != 10 {
		t.Fatalf("compact toggle state=%v height=%d, want true/10", m.CompactMode, m.output.viewport.Height)
	}
	// The debounced resize path used to overwrite the compact height here.
	m.applyResize(&tea.WindowSizeMsg{Width: 96, Height: 36})
	if got := m.output.viewport.Height; got != 12 {
		t.Fatalf("compact viewport after resize=%d, want 12", got)
	}

	compact := stripAnsi(m.View())
	if got := lipgloss.Height(compact); got != 36 {
		t.Fatalf("compact frame height=%d, want 36", got)
	}
	if !strings.Contains(compact, "transcript line 59") {
		t.Fatalf("compact frame lost newest transcript line:\n%s", compact)
	}
	if strings.Contains(compact, "transcript line 30") {
		t.Fatalf("compact frame rendered substantially more than its 12-row transcript budget:\n%s", compact)
	}
	assertInputAndStatusStayBottomAnchored(t, m, compact)
}

func TestCompactModeToggleBackRestoresNormalViewport(t *testing.T) {
	m := NewModel()
	m.state = StateInput
	m.applyResize(&tea.WindowSizeMsg{Width: 90, Height: 30})
	m.toggleCompactMode()
	m.toggleCompactMode()

	if m.CompactMode || m.output.viewport.Height != 25 {
		t.Fatalf("normal restore state=%v height=%d, want false/25", m.CompactMode, m.output.viewport.Height)
	}
}

func TestCompactModeHalfPageUsesVisibleViewportHeight(t *testing.T) {
	m := NewModel()
	m.state = StateStreaming
	m.applyResize(&tea.WindowSizeMsg{Width: 90, Height: 30})
	m.toggleCompactMode()
	for i := range 60 {
		m.output.AppendLine(fmt.Sprintf("line %02d", i))
	}
	m.output.viewport.SetYOffset(0)
	m.output.SetFrozen(true)

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlD})
	if got := m.output.viewport.YOffset; got != 5 {
		t.Fatalf("compact Ctrl+D moved %d rows, want half of visible height (5)", got)
	}
}

func TestCompactModeConfigUpdateAppliesAndRestoresGeometry(t *testing.T) {
	m := NewModel()
	m.applyResize(&tea.WindowSizeMsg{Width: 90, Height: 30})

	_ = m.handleMessageTypes(ConfigUpdateMsg{CompactMode: true})
	if !m.CompactMode || m.output.viewport.Height != 10 {
		t.Fatalf("live config compact state=%v height=%d, want true/10", m.CompactMode, m.output.viewport.Height)
	}
	_ = m.handleMessageTypes(ConfigUpdateMsg{CompactMode: false})
	if m.CompactMode || m.output.viewport.Height != 25 {
		t.Fatalf("live config normal state=%v height=%d, want false/25", m.CompactMode, m.output.viewport.Height)
	}
}

func TestCompactModePaletteTogglePersistsAndRollsBackAuthoritatively(t *testing.T) {
	m := NewModel()
	m.applyResize(&tea.WindowSizeMsg{Width: 90, Height: 30})
	var calls int
	var key string
	var enabled bool
	m.SetSettingToggleCallback(func(gotKey string, gotEnabled bool) {
		calls++
		key, enabled = gotKey, gotEnabled
	})

	m.toggleCompactMode()
	if calls != 1 || key != "compactui" || !enabled || !m.CompactMode {
		t.Fatalf("persist request calls=%d key=%q enabled=%v compact=%v", calls, key, enabled, m.CompactMode)
	}
	// A second activation while ApplyConfig is pending must not enqueue an
	// out-of-order inverse write.
	m.toggleCompactMode()
	if calls != 1 || !m.CompactMode {
		t.Fatalf("pending toggle was not guarded: calls=%d compact=%v", calls, m.CompactMode)
	}

	_ = m.handleMessageTypes(SettingToggleResultMsg{
		Key:     "compactui",
		On:      false,
		Success: false,
		Message: "save failed",
	})
	if m.CompactMode || m.output.viewport.Height != 25 {
		t.Fatalf("failed persistence did not restore authoritative layout: compact=%v height=%d", m.CompactMode, m.output.viewport.Height)
	}
}

func TestCompactModePaletteDescriptionTracksCurrentState(t *testing.T) {
	m := NewModel()
	m.RegisterPaletteActions()
	compactDescription := func() string {
		for _, command := range m.commandPalette.actionCommands {
			if command.ActionID == paletteActionCompactMode {
				return command.Description
			}
		}
		return ""
	}

	m.SetCompactMode(false)
	if got := strings.ToLower(compactDescription()); !strings.Contains(got, "currently normal") {
		t.Fatalf("normal palette description=%q", got)
	}
	m.SetCompactMode(true)
	if got := strings.ToLower(compactDescription()); !strings.Contains(got, "currently compact") {
		t.Fatalf("compact palette description=%q", got)
	}
}

func assertInputAndStatusStayBottomAnchored(t *testing.T, m *Model, view string) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if got, want := strings.TrimRight(lines[len(lines)-1], " "), stripAnsi(m.renderStatusBar()); got != want {
		t.Fatalf("last row is not status bar: got %q want %q", got, want)
	}
	inputRow := -1
	for i, line := range lines {
		if strings.Contains(line, "›") {
			inputRow = i
		}
	}
	if inputRow < len(lines)*3/4 {
		t.Fatalf("compact input floated away from bottom: row=%d of %d\n%s", inputRow, len(lines), view)
	}
}
