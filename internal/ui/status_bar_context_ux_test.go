package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func plainShortcutHints(hints []shortcutHint) string {
	var parts []string
	for _, hint := range hints {
		parts = append(parts, hint.key+" "+hint.desc)
	}
	return strings.Join(parts, " | ")
}

func TestStatusBarHintsFollowPromptSubmode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		model   Model
		want    []string
		notWant []string
	}{
		{
			name:    "plan feedback",
			model:   Model{state: StatePlanApproval, planFeedbackMode: true},
			want:    []string{"Enter Submit", "esc Back"},
			notWant: []string{"y Approve", "m Modify"},
		},
		{
			name:    "custom answer",
			model:   Model{state: StateQuestionPrompt, questionCustomInput: true},
			want:    []string{"Enter Submit", "esc Back"},
			notWant: []string{"↑↓ Navigate"},
		},
		{
			name:    "permission details",
			model:   Model{state: StatePermissionPrompt, permShowDetails: true},
			want:    []string{"↑↓ Scroll", "? Back", "y/a/n Decide"},
			notWant: []string{"Enter"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := plainShortcutHints(tc.model.contextualShortcutHintPairs())
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in %q", want, got)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(got, notWant) {
					t.Errorf("stale submode hint %q in %q", notWant, got)
				}
			}
		})
	}
}

func TestNarrowStatusBarKeepsModelIdentity(t *testing.T) {
	for _, tc := range []struct {
		width int
		model string
		want  string
	}{
		{width: 79, model: "gpt-5.4-preview", want: "gpt-5.4"},
		{width: 40, model: "claude-sonnet-4", want: "claude"},
	} {
		m := NewModel()
		m.width = tc.width
		m.currentModel = tc.model
		m.runtimeStatus.Provider = ""
		got := ansi.Strip(m.renderStatusBar())
		if !strings.Contains(got, tc.want) {
			t.Fatalf("width=%d lost model identity %q: %q", tc.width, tc.want, got)
		}
	}
}

func TestStatusBarSanitizesRuntimeIdentity(t *testing.T) {
	m := NewModel()
	m.width = 120
	m.workDir = "/tmp/repo\nFORGED PATH"
	m.currentModel = "gpt-5.4\nFORGED MODEL"
	m.runtimeStatus.Provider = "\x1b[2Jopenai\nFORGED PROVIDER"

	got := m.renderStatusBar()
	if strings.Contains(got, "\x1b[2J") {
		t.Fatalf("status bar preserved runtime terminal control sequence: %q", got)
	}
	if plain := ansi.Strip(got); strings.Contains(plain, "\n") {
		t.Fatalf("runtime metadata injected an extra status row: %q", plain)
	}
}
