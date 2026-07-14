package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestToastLimitEvictsBySeverityThenAge(t *testing.T) {
	m := NewToastManager(DefaultStyles())
	m.maxToasts = 2
	m.ShowWarning("Important warning")
	m.ShowInfo("old info")
	m.ShowInfo("new info")

	if m.Count() != 2 {
		t.Fatalf("active count=%d", m.Count())
	}
	var warning, newest bool
	for _, toast := range m.toasts {
		warning = warning || toast.Message == "Important warning"
		newest = newest || toast.Message == "new info"
	}
	if !warning || !newest {
		t.Fatalf("severity-aware eviction kept wrong toasts: %+v", m.toasts)
	}
}

func TestToastHeightLimitPrioritizesCriticalAndReportsHiddenCount(t *testing.T) {
	m := NewToastManager(DefaultStyles())
	m.ShowError("Critical failure")
	m.ShowWarning("Warning")
	m.ShowInfo("Newest info")

	view := m.ViewLimit(40, 1)
	plain := stripAnsi(view)
	if strings.Contains(plain, "\n") || !strings.Contains(plain, "Critical failure") || !strings.Contains(plain, "+2 more") {
		t.Fatalf("limited toast view lost priority/count: %q", plain)
	}
	if got := lipgloss.Width(view); got > 40 {
		t.Fatalf("limited toast width=%d: %q", got, plain)
	}
}

func TestShortFrameKeepsCriticalToastAndBottomStatus(t *testing.T) {
	m := NewModel()
	m.currentModel = "glm-5.2"
	m.applyResize(&tea.WindowSizeMsg{Width: 60, Height: 6})
	m.toastManager.ShowError("Critical failure")
	for _, message := range []string{"info one", "info two", "info three", "info four"} {
		m.toastManager.Show(ToastInfo, "", message, time.Minute)
	}

	view := stripAnsi(m.View())
	if !strings.Contains(view, "Critical failure") || !strings.Contains(view, "+4 more") {
		t.Fatalf("short frame clipped critical toast:\n%s", view)
	}
	if strings.Contains(view, "Welcome") {
		t.Fatalf("welcome competed with urgent short-frame feedback:\n%s", view)
	}
	lines := strings.Split(view, "\n")
	if !strings.Contains(lines[len(lines)-1], "glm-5.2") {
		t.Fatalf("status bar lost bottom anchor: %q", lines[len(lines)-1])
	}
	if lipgloss.Height(m.View()) != 6 {
		t.Fatalf("short frame height=%d", lipgloss.Height(m.View()))
	}
}

func TestShortPromptKeepsDecisionFooterAlongsideCriticalToast(t *testing.T) {
	m := NewModel()
	m.applyResize(&tea.WindowSizeMsg{Width: 72, Height: 10})
	m.state = StatePermissionPrompt
	m.permRequest = &PermissionRequestMsg{ToolName: "bash", RiskLevel: "high", Args: map[string]any{"command": "go test ./..."}}
	m.toastManager.ShowError("Provider disconnected")

	view := stripAnsi(m.View())
	for _, want := range []string{"Provider disconnected", "Esc Deny"} {
		if !strings.Contains(view, want) {
			t.Fatalf("short prompt clipped %q:\n%s", want, view)
		}
	}
}
