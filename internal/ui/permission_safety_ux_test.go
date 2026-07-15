package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestPermissionUnknownRiskNeverLooksLowRisk(t *testing.T) {
	for _, risk := range []string{"", "unexpected", "  CRITICAL  "} {
		m := NewModel()
		m.width = 80
		m.permRequest = &PermissionRequestMsg{ToolName: "write", RiskLevel: risk}
		got := stripAnsi(m.renderPermissionPrompt())
		if !strings.Contains(got, "RISK UNKNOWN") || strings.Contains(got, "LOW RISK") {
			t.Fatalf("risk=%q rendered unsafe classification:\n%s", risk, got)
		}
	}

	m := NewModel()
	m.width = 80
	m.permRequest = &PermissionRequestMsg{ToolName: "read", RiskLevel: "  LOW  "}
	if got := stripAnsi(m.renderPermissionPrompt()); !strings.Contains(got, "LOW RISK") {
		t.Fatalf("known risk should be normalized case-insensitively:\n%s", got)
	}
}

func TestPermissionInvalidSelectionFailsClosed(t *testing.T) {
	for _, invalid := range []int{-9, 99} {
		t.Run(string(rune('a'+invalid+9)), func(t *testing.T) {
			m := NewModel()
			m.state = StatePermissionPrompt
			m.permSelectedOption = invalid
			m.permRequest = &PermissionRequestMsg{ID: "unsafe", ToolName: "bash", RiskLevel: "high"}
			var decision PermissionDecision
			m.onPermission = func(_ string, got PermissionDecision) { decision = got }

			view := stripAnsi(m.renderPermissionPrompt())
			if !strings.Contains(view, "> 3. Deny") {
				t.Fatalf("invalid selection did not visibly fail closed:\n%s", view)
			}
			_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
			if decision != PermissionDeny {
				t.Fatalf("invalid index %d confirmed decision %v, want deny", invalid, decision)
			}
		})
	}
}

func TestPermissionCompactFrameKeepsEveryDecisionVisible(t *testing.T) {
	m := NewModel()
	m.applyResize(&tea.WindowSizeMsg{Width: 72, Height: 12})
	m.state = StatePermissionPrompt
	m.permRequest = &PermissionRequestMsg{
		ToolName:  "bash",
		RiskLevel: "high",
		Reason:    strings.Repeat("This operation changes generated files. ", 8),
		Args:      map[string]any{"command": strings.Repeat("go generate ./... ", 10)},
	}

	view := stripAnsi(m.View())
	for _, want := range []string{"HIGH RISK", "1. Allow once", "2. Allow for session", "3. Deny", "? Details", "Esc Deny"} {
		if !strings.Contains(view, want) {
			t.Fatalf("compact permission frame clipped %q:\n%s", want, view)
		}
	}
	if got := lipgloss.Height(view); got != 12 {
		t.Fatalf("frame height=%d want 12", got)
	}
}

func TestPermissionDetailFrameKeepsNavigationVisible(t *testing.T) {
	m := NewModel()
	m.applyResize(&tea.WindowSizeMsg{Width: 72, Height: 12})
	m.state = StatePermissionPrompt
	m.permShowDetails = true
	m.permRequest = &PermissionRequestMsg{
		ToolName:  "bash",
		RiskLevel: "high",
		Reason:    strings.Repeat("review this reason ", 10),
		Args: map[string]any{
			"command": strings.Repeat("界 command ", 20),
			"path":    strings.Repeat("nested/path/", 15),
		},
	}

	view := stripAnsi(m.View())
	for _, want := range []string{"Request details", "↓", "? Back", "1-3 Decide", "Esc Deny"} {
		if !strings.Contains(view, want) {
			t.Fatalf("permission details clipped %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "1. Allow once") {
		t.Fatalf("hidden decisions leaked into detail mode:\n%s", view)
	}
}

func TestPermissionPromptFitsHeightWithoutFrameCropping(t *testing.T) {
	for _, height := range []int{12, 18, 24} {
		for _, details := range []bool{false, true} {
			m := NewModel()
			m.width = 72
			m.height = height
			m.permShowDetails = details
			m.permDetailScroll = 2
			m.permNotice = "Review the exact operation before deciding"
			m.permRequest = &PermissionRequestMsg{
				ToolName:  "bash",
				RiskLevel: "high",
				Reason:    strings.Repeat("This command changes generated files. ", 10),
				Args: map[string]any{
					"command": strings.Repeat("go generate ./internal/package && ", 10),
					"path":    strings.Repeat("nested/path/", 16),
				},
			}

			view := m.renderPermissionPrompt()
			if got := lipgloss.Height(view); got > height {
				t.Fatalf("height=%d details=%v permission modal rendered %d rows:\n%s", height, details, got, stripAnsi(view))
			}
			plain := stripAnsi(view)
			for _, want := range []string{"HIGH RISK", "Esc"} {
				if !strings.Contains(plain, want) {
					t.Fatalf("height=%d details=%v missing %q:\n%s", height, details, want, plain)
				}
			}
		}
	}
}

func TestPermissionPromptFitsNarrowWidthsWithUnicode(t *testing.T) {
	for width := 10; width <= 48; width++ {
		for _, details := range []bool{false, true} {
			m := NewModel()
			m.width = width
			m.height = 12
			m.permShowDetails = details
			m.permRequest = &PermissionRequestMsg{
				ToolName:  "  write\n file  ",
				RiskLevel: "unknown",
				Reason:    strings.Repeat("причина 界 ", 20),
				Args: map[string]any{
					strings.Repeat("very_long_key", 4): strings.Repeat("значение 界 ", 20),
				},
			}

			got := m.renderPermissionPrompt()
			for row, line := range strings.Split(got, "\n") {
				if lineWidth := lipgloss.Width(line); lineWidth > width {
					t.Fatalf("width=%d details=%v row=%d overflow=%d: %q", width, details, row, lineWidth, stripAnsi(line))
				}
			}
		}
	}
}

func TestPermissionDetailCellWrappingHonorsDisplayWidth(t *testing.T) {
	lines := wrapPermissionField("ключ", strings.Repeat("界", 12), 8)
	if len(lines) < 3 {
		t.Fatalf("wide glyphs were counted as single-cell runes: %v", lines)
	}
	for i, line := range lines {
		if width := lipgloss.Width(line); width > 8 {
			t.Fatalf("line %d width=%d exceeds 8: %q", i, width, line)
		}
	}
}

func TestPermissionNilRequestRecoversWithoutDecision(t *testing.T) {
	m := NewModel()
	m.state = StatePermissionPrompt
	called := false
	m.onPermission = func(string, PermissionDecision) { called = true }
	_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StateInput || called {
		t.Fatalf("nil request state=%v callback=%v", m.state, called)
	}
}
