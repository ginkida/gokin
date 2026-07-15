package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPaletteSubmissionPathsShareLifecycleAndPreserveDraft(t *testing.T) {
	tests := []struct {
		name  string
		want  string
		setup func(*Model)
	}{
		{
			name: "direct command line",
			want: "/open main.go",
			setup: func(m *Model) {
				m.commandPalette.commands = []EnhancedPaletteCommand{{
					Name: "open", Shortcut: "/open", ArgHint: "<file>", Type: CommandTypeSlash, Enabled: true,
				}}
				m.commandPalette.visible = true
				m.commandPalette.SetQuery("/open main.go")
			},
		},
		{
			name: "inline argument",
			want: "/grep needle",
			setup: func(m *Model) {
				m.commandPalette.visible = true
				m.commandPalette.BeginArgEntry(EnhancedPaletteCommand{
					Name: "grep", Shortcut: "/grep", ArgHint: "<pattern>", Type: CommandTypeSlash, Enabled: true,
				})
				m.commandPalette.AppendArg("needle")
			},
		},
		{
			name: "selected slash command",
			want: "/status",
			setup: func(m *Model) {
				cmd := EnhancedPaletteCommand{Name: "status", Shortcut: "/status", Type: CommandTypeSlash, Enabled: true}
				m.commandPalette.commands = []EnhancedPaletteCommand{cmd}
				m.commandPalette.filtered = []EnhancedPaletteCommand{cmd}
				m.commandPalette.visible = true
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel()
			m.state = StateCommandPalette
			m.input.textarea.SetValue("draft stays exact")
			m.input.textarea.CursorEnd()
			m.input.AddToHistory("older request")
			m.output.SetSize(80, 5)
			for range 20 {
				m.output.AppendLine("old output")
			}
			m.output.SetFrozen(true)
			m.slowWarningShown = true
			m.responseHeaderShown = true
			m.responseToolDuration = 3 * time.Second
			m.responseToolCount = 4
			m.responseToolFailures = 2
			var submitted string
			m.SetCallbacks(func(line string) { submitted = line }, nil)
			tt.setup(m)

			// Drive the real parent Update path: this catches an Enter forwarded a
			// second time into the now-active type-ahead composer.
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			got := updated.(Model)

			if submitted != tt.want || got.state != StateProcessing {
				t.Fatalf("submitted=%q state=%v, want %q / Processing", submitted, got.state, tt.want)
			}
			if draft := got.input.Value(); draft != "draft stays exact" {
				t.Fatalf("palette Enter corrupted composer draft: %q", draft)
			}
			history := got.input.GetHistory()
			if len(history) == 0 || history[len(history)-1] != tt.want {
				t.Fatalf("palette command missing from input history: %q", history)
			}
			if got.streamStartTime.IsZero() || got.lastActivityTime.IsZero() || got.lastSubmitTime.IsZero() {
				t.Fatal("palette submission did not initialize request timing")
			}
			if got.slowWarningShown || got.responseHeaderShown || got.responseToolDuration != 0 ||
				got.responseToolCount != 0 || got.responseToolFailures != 0 {
				t.Fatalf("stale response lifecycle survived: slow=%v header=%v duration=%v count=%d failures=%d",
					got.slowWarningShown, got.responseHeaderShown, got.responseToolDuration, got.responseToolCount, got.responseToolFailures)
			}
			if got.output.IsFrozen() {
				t.Fatal("palette submission left output scroll frozen")
			}
			if got.commandPalette.IsVisible() || got.commandPalette.InArgEntry() {
				t.Fatal("palette remained visible after submission")
			}
			if output := stripAnsi(got.output.Content()); !strings.Contains(output, tt.want) {
				t.Fatalf("submitted command missing from scrollback: %q", output)
			}
		})
	}
}

func TestUnavailablePaletteSubmissionDoesNotMutateLifecycle(t *testing.T) {
	m := NewModel()
	m.state = StateCommandPalette
	m.input.textarea.SetValue("draft")
	m.commandPalette.commands = []EnhancedPaletteCommand{{
		Name: "open", Shortcut: "/open", ArgHint: "<file>", Type: CommandTypeSlash, Enabled: true,
	}}
	m.commandPalette.visible = true
	m.commandPalette.SetQuery("/open main.go")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	if got.state != StateCommandPalette || got.input.Value() != "draft" || len(got.input.GetHistory()) != 0 {
		t.Fatalf("unavailable submission mutated state: state=%v draft=%q history=%q", got.state, got.input.Value(), got.input.GetHistory())
	}
	if got.commandPalette.submitError == "" || !got.streamStartTime.IsZero() {
		t.Fatalf("unavailable feedback/timing mismatch: error=%q start=%v", got.commandPalette.submitError, got.streamStartTime)
	}
}
