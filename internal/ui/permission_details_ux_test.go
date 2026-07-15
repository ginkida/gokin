package ui

import (
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func permissionDetailsTestModel() *Model {
	m := NewModel()
	m.width = 72
	m.height = 18
	m.state = StatePermissionPrompt
	m.permRequest = &PermissionRequestMsg{
		ID:        "request-1",
		ToolName:  "bash",
		RiskLevel: "high",
		Reason:    "The command changes generated files and needs explicit review",
		Args: map[string]any{
			"command":     "go generate ./internal/very/long/package && gofmt -w generated_output.go",
			"description": "Regenerate checked-in sources",
		},
	}
	return m
}

func TestPermissionDetailsToggleRendersInline(t *testing.T) {
	m := permissionDetailsTestModel()
	beforeOutput := m.output.state.content.String()
	initial := stripAnsi(m.renderPermissionPrompt())
	for _, want := range []string{"? Details", "Esc Deny"} {
		if !strings.Contains(initial, want) {
			t.Fatalf("decision footer missing %q:\n%s", want, initial)
		}
	}

	_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !m.permShowDetails {
		t.Fatal("? should open inline permission details")
	}
	if got := m.output.state.content.String(); got != beforeOutput {
		t.Fatalf("details leaked into hidden scrollback: before=%q after=%q", beforeOutput, got)
	}

	view := stripAnsi(m.renderPermissionPrompt())
	for _, want := range []string{"Request details", "Reason:", "command:", "go generate", "? Back", "Esc Deny"} {
		if !strings.Contains(view, want) {
			t.Fatalf("inline details missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "1. Allow") {
		t.Fatalf("hidden decision list remained visible in detail mode:\n%s", view)
	}

	_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if m.permShowDetails {
		t.Fatal("second ? should return to decision list")
	}
}

func TestPermissionCompactSummaryFitsNarrowWidth(t *testing.T) {
	for _, terminalWidth := range []int{1, 2, 3, 8, 12, 20} {
		t.Run(strconv.Itoa(terminalWidth), func(t *testing.T) {
			m := permissionDetailsTestModel()
			m.width = terminalWidth
			m.permRequest.Args["command"] = strings.Repeat("x", 100)
			m.permRequest.Reason = strings.Repeat("reason", 20)

			view := m.renderPermissionPrompt() // must not panic at sub-ellipsis widths
			for i, line := range strings.Split(view, "\n") {
				if width := lipgloss.Width(line); terminalWidth >= 20 && width > m.width {
					t.Fatalf("row %d width=%d exceeds terminal width=%d:\n%s", i, width, m.width, stripAnsi(view))
				}
			}
		})
	}
}

func TestPermissionDetailNavigationCannotConfirmHiddenSelection(t *testing.T) {
	m := permissionDetailsTestModel()
	m.height = 12
	m.permShowDetails = true
	called := false
	m.onPermission = func(string, PermissionDecision) { called = true }

	_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyDown})
	if m.permDetailScroll == 0 {
		t.Fatal("Down should scroll overflowing detail content")
	}
	if m.permSelectedOption != 0 {
		t.Fatalf("detail scrolling moved hidden decision cursor to %d", m.permSelectedOption)
	}
	_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if called || m.permRequest == nil || m.state != StatePermissionPrompt {
		t.Fatalf("Enter confirmed an invisible decision: called=%v request=%v state=%v", called, m.permRequest, m.state)
	}
}

func TestPermissionShortDetailsDoNotAdvertiseDeadScrolling(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.height = 30
	m.state = StatePermissionPrompt
	m.permShowDetails = true
	m.permRequest = &PermissionRequestMsg{
		ToolName: "read",
		Reason:   "Review the requested file",
		Args:     map[string]any{"path": "main.go"},
	}

	view := stripAnsi(m.renderPermissionPrompt())
	hints := plainShortcutHints(m.contextualShortcutHintPairs())
	for _, got := range []string{view, hints} {
		if strings.Contains(got, "Scroll") || strings.Contains(got, "PgUp") {
			t.Fatalf("short details advertised unavailable scrolling:\n%s", got)
		}
	}
	for _, want := range []string{"? Back", "Decide", "Esc Deny"} {
		if !strings.Contains(view+"\n"+hints, want) {
			t.Fatalf("short details lost recovery action %q:\n%s\n%s", want, view, hints)
		}
	}

	_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyDown})
	_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.permDetailScroll != 0 {
		t.Fatalf("short details moved to impossible scroll offset %d", m.permDetailScroll)
	}
}

func TestPermissionOverflowDetailsAdvertiseLineAndPageScrolling(t *testing.T) {
	m := permissionDetailsTestModel()
	m.height = 12
	m.permShowDetails = true

	view := stripAnsi(m.renderPermissionPrompt())
	hints := plainShortcutHints(m.contextualShortcutHintPairs())
	for _, want := range []string{"↑/↓ Scroll", "↑↓ Scroll", "PgUp/PgDn Page", "? Back", "y/a/n Decide"} {
		if !strings.Contains(view+"\n"+hints, want) {
			t.Fatalf("overflow details missing %q:\n%s\n%s", want, view, hints)
		}
	}
}

func TestPermissionExplicitNumberStillDecidesFromDetails(t *testing.T) {
	m := permissionDetailsTestModel()
	m.permShowDetails = true
	var got PermissionDecision
	m.onPermission = func(_ string, decision PermissionDecision) { got = decision }

	_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	if got != PermissionDeny || m.permRequest != nil || m.permShowDetails || m.permDetailScroll != 0 {
		t.Fatalf("explicit detail decision did not cleanly deny: decision=%v request=%v details=%v scroll=%d", got, m.permRequest, m.permShowDetails, m.permDetailScroll)
	}
}

func TestPermissionNewRequestResetsDetailState(t *testing.T) {
	m := NewModel()
	m.permShowDetails = true
	m.permDetailScroll = 7

	m.handleMessageTypes(PermissionRequestMsg{ID: "new", ToolName: "write", Args: map[string]any{"file_path": "main.go"}})
	if m.state != StatePermissionPrompt || m.permRequest == nil {
		t.Fatalf("new permission request did not open: state=%v request=%v", m.state, m.permRequest)
	}
	if m.permShowDetails || m.permDetailScroll != 0 {
		t.Fatalf("new request inherited detail state: details=%v scroll=%d", m.permShowDetails, m.permDetailScroll)
	}
}
