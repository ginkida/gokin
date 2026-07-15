package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestThreeRowPermissionKeepsDecisionAndRecoveryVisible(t *testing.T) {
	for _, tc := range []struct {
		width    int
		selected PermissionDecision
		want     string
	}{
		{width: 20, selected: PermissionAllow, want: "> 1. Allow once"},
		{width: 8, selected: PermissionAllow, want: "1 ONCE"},
		{width: 8, selected: PermissionAllowSession, want: "2 SESS"},
		{width: 8, selected: PermissionDeny, want: "3 DENY"},
	} {
		t.Run(fmt.Sprintf("width_%d/decision_%d", tc.width, tc.selected), func(t *testing.T) {
			m := NewModel()
			m.applyResize(&tea.WindowSizeMsg{Width: tc.width, Height: 3})
			m.state = StatePermissionPrompt
			m.permSelectedOption = int(tc.selected)
			m.permRequest = &PermissionRequestMsg{ID: "req", ToolName: "bash", RiskLevel: "high"}

			plain := stripAnsi(m.View())
			for _, want := range []string{tc.want, "Esc Deny"} {
				if !strings.Contains(plain, want) {
					t.Fatalf("%dx3 permission frame hid %q:\n%s", tc.width, want, plain)
				}
			}
		})
	}
}

func TestUnreadablePermissionGeometryBlocksEveryAllowKey(t *testing.T) {
	type geometry struct{ width, height int }
	var geometries []geometry
	for width := 1; width <= 10; width++ {
		for height := 1; height <= 4; height++ {
			if width < permissionReadableDecisionWidth || height < permissionReadableDecisionHeight {
				geometries = append(geometries, geometry{width: width, height: height})
			}
		}
	}
	geometries = append(geometries,
		geometry{width: 20, height: 1},
		geometry{width: 20, height: 2},
		geometry{width: 1, height: 6},
		geometry{width: 7, height: 6},
	)
	allowKeys := []struct {
		name     string
		key      tea.KeyMsg
		selected PermissionDecision
	}{
		{name: "enter", key: tea.KeyMsg{Type: tea.KeyEnter}, selected: PermissionAllow},
		{name: "space", key: tea.KeyMsg{Type: tea.KeySpace}, selected: PermissionAllowSession},
		{name: "one", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}}, selected: PermissionDeny},
		{name: "two", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}}, selected: PermissionDeny},
		{name: "y", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}, selected: PermissionDeny},
		{name: "a", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}, selected: PermissionDeny},
	}

	for _, geometry := range geometries {
		for _, allow := range allowKeys {
			name := fmt.Sprintf("%dx%d/%s", geometry.width, geometry.height, allow.name)
			t.Run(name, func(t *testing.T) {
				m := NewModel()
				m.width, m.height = geometry.width, geometry.height
				m.state = StatePermissionPrompt
				m.permSelectedOption = int(allow.selected)
				request := &PermissionRequestMsg{ID: "req", ToolName: "bash", RiskLevel: "high"}
				m.permRequest = request
				called := false
				m.onPermission = func(string, PermissionDecision) { called = true }

				_ = m.handlePermissionPromptKeys(allow.key)
				if called || m.state != StatePermissionPrompt || m.permRequest != request {
					t.Fatalf("unreadable geometry accepted allow: called=%v state=%v request=%p want=%p", called, m.state, m.permRequest, request)
				}
			})
		}
	}
}

func TestUnreadablePermissionGeometryKeepsFailClosedDenial(t *testing.T) {
	denyKeys := []struct {
		name     string
		key      tea.KeyMsg
		selected PermissionDecision
	}{
		{name: "escape", key: tea.KeyMsg{Type: tea.KeyEscape}, selected: PermissionAllow},
		{name: "n", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}, selected: PermissionAllow},
		{name: "three", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}}, selected: PermissionAllow},
		{name: "enter_selected_deny", key: tea.KeyMsg{Type: tea.KeyEnter}, selected: PermissionDeny},
		{name: "space_selected_deny", key: tea.KeyMsg{Type: tea.KeySpace}, selected: PermissionDeny},
	}

	for _, deny := range denyKeys {
		t.Run(deny.name, func(t *testing.T) {
			m := NewModel()
			m.width, m.height = 1, 1
			m.state = StatePermissionPrompt
			m.permSelectedOption = int(deny.selected)
			m.permRequest = &PermissionRequestMsg{ID: "req", ToolName: "bash", RiskLevel: "high"}
			called := 0
			var decision PermissionDecision
			m.onPermission = func(_ string, got PermissionDecision) {
				called++
				decision = got
			}

			_ = m.handlePermissionPromptKeys(deny.key)
			if called != 1 || decision != PermissionDeny || m.state != StateProcessing || m.permRequest != nil {
				t.Fatalf("fail-closed denial failed: calls=%d decision=%v state=%v request=%v", called, decision, m.state, m.permRequest)
			}
		})
	}
}

func TestSubThreeRowPermissionExplainsFailClosedResize(t *testing.T) {
	for _, height := range []int{1, 2} {
		m := NewModel()
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: height})
		m.state = StatePermissionPrompt
		m.permRequest = &PermissionRequestMsg{ID: "req", ToolName: "bash", RiskLevel: "high"}

		plain := stripAnsi(m.View())
		if !strings.Contains(plain, "Resize") || !strings.Contains(plain, "Esc/n Deny") {
			t.Fatalf("20x%d permission frame lacks fail-closed guidance: %q", height, plain)
		}
	}

	m := NewModel()
	m.applyResize(&tea.WindowSizeMsg{Width: 7, Height: 6})
	m.state = StatePermissionPrompt
	m.permRequest = &PermissionRequestMsg{ID: "req", ToolName: "bash", RiskLevel: "high"}
	if plain := stripAnsi(m.View()); !strings.Contains(plain, "Resize") {
		t.Fatalf("7x6 permission frame lacks narrow-width resize guidance: %q", plain)
	}
}

func TestPermissionStatusHintDistinguishesDenyFromUnavailableCancel(t *testing.T) {
	for _, details := range []bool{false, true} {
		normal := Model{state: StatePermissionPrompt, permShowDetails: details}
		normalHints := plainShortcutHints(normal.contextualShortcutHintPairs())
		if !strings.Contains(normalHints, "esc Deny") || strings.Contains(normalHints, "esc Cancel") {
			t.Fatalf("details=%v normal permission hints misstate Esc semantics: %q", details, normalHints)
		}

		unavailable := normal
		unavailable.permNotice = "Unavailable: cannot send permission response"
		unavailableHints := plainShortcutHints(unavailable.contextualShortcutHintPairs())
		if !strings.Contains(unavailableHints, "esc Cancel") || strings.Contains(unavailableHints, "esc Deny") {
			t.Fatalf("details=%v unavailable permission hints lost hard-cancel semantics: %q", details, unavailableHints)
		}
	}

	unavailableTiny := Model{
		state:      StatePermissionPrompt,
		width:      20,
		height:     3,
		permNotice: "Unavailable: cannot send permission response",
	}
	if status := stripAnsi(unavailableTiny.renderStatusBar()); !strings.Contains(status, "Esc Cancel") || strings.Contains(status, "Esc Deny") {
		t.Fatalf("tiny unavailable permission status lost hard-cancel semantics: %q", status)
	}
}
