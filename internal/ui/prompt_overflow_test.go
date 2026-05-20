package ui

import (
	"strings"
	"testing"
)

// TestPromptPaletteWidth_NarrowTerminalFallsBackToFlat pins the overflow
// guard: when the terminal is narrower than minBorderedPromptWidth, the
// helper must return bordered=false so callers skip the rounded-border
// container instead of rendering a 45-wide box in (say) a 40-cell terminal.
//
// Without this guard the lipgloss container overflows the terminal edge
// horizontally and breaks every line of the prompt — worse UX than a
// borderless flat prompt would deliver.
func TestPromptPaletteWidth_NarrowTerminalFallsBackToFlat(t *testing.T) {
	cases := []struct {
		name        string
		termWidth   int
		wantBordered bool
		wantWidthMin int // lower bound on returned content width
	}{
		{"very narrow", 30, false, 26}, // termWidth-4
		{"just below threshold", 49, false, 30},
		{"at threshold", 50, true, 44}, // 50-6 = 44
		{"comfortable", 80, true, 74},  // 80-6 = 74
		{"wide", 120, true, 78},        // capped at 78
		{"very wide", 200, true, 78},   // still capped
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotWidth, gotBordered := promptPaletteWidth(tc.termWidth)
			if gotBordered != tc.wantBordered {
				t.Errorf("bordered = %v, want %v (termWidth=%d)", gotBordered, tc.wantBordered, tc.termWidth)
			}
			if gotWidth < tc.wantWidthMin {
				t.Errorf("width = %d, want at least %d (termWidth=%d)", gotWidth, tc.wantWidthMin, tc.termWidth)
			}
		})
	}
}

// TestPermissionPrompt_NarrowTerminalSkipsBorder verifies the user-visible
// effect: rendering the permission prompt in a 40-cell terminal must not
// emit border-line characters. lipgloss's RoundedBorder uses ╭╮╰╯─│; any
// of those in the output means we tried to draw a box.
func TestPermissionPrompt_NarrowTerminalSkipsBorder(t *testing.T) {
	m := Model{
		width: 40, // narrower than minBorderedPromptWidth=50
		permRequest: &PermissionRequestMsg{
			ToolName:  "bash",
			RiskLevel: "high",
			Args:      map[string]any{"command": "ls"},
			Reason:    "Run a shell command",
		},
		// Default permSelectedOption (0) is fine
	}
	got := m.renderPermissionPrompt()
	borderRunes := []string{"╭", "╮", "╰", "╯", "│"}
	for _, r := range borderRunes {
		if strings.Contains(got, r) {
			t.Errorf("narrow-terminal prompt should NOT contain border rune %q, got:\n%s", r, got)
		}
	}
	// Content must still appear, just without the box.
	if !strings.Contains(got, "Permission Required") {
		t.Errorf("prompt title missing in narrow render:\n%s", got)
	}
}

// TestPermissionPrompt_ComfortableTerminalKeepsBorder is the converse —
// at the threshold width the bordered rendering must actually fire so
// users on wide terminals don't regress to flat-line prompts.
func TestPermissionPrompt_ComfortableTerminalKeepsBorder(t *testing.T) {
	m := Model{
		width: 100,
		permRequest: &PermissionRequestMsg{
			ToolName:  "bash",
			RiskLevel: "low",
			Args:      map[string]any{"command": "ls"},
		},
	}
	got := m.renderPermissionPrompt()
	if !strings.Contains(got, "╭") || !strings.Contains(got, "╰") {
		t.Errorf("comfortable-terminal prompt should render a rounded border, got:\n%s", got)
	}
}
