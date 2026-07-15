package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
)

func TestRequestErrorShowsNonDestructiveComposerRecovery(t *testing.T) {
	tests := []struct {
		name    string
		history string
		draft   string
		want    string
	}{
		{
			name:    "submitted message remains in history",
			history: "retry this request with secret-token",
			want:    "Press ↑ to restore your last message and retry",
		},
		{
			name:    "newer follow-up wins",
			history: "older submitted request",
			draft:   "new follow-up draft",
			want:    "Follow-up draft preserved in composer",
		},
		{
			name: "fresh composer",
			want: "Composer is ready — adjust the request and retry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel()
			m.width = 100
			m.state = StateProcessing
			if tt.history != "" {
				m.input.AddToHistory(tt.history)
			}
			m.input.textarea.SetValue(tt.draft)
			m.input.Blur()

			updated, _ := m.Update(ErrorMsg(errors.New("provider request failed")))
			got := updated.(Model)
			if got.state != StateInput || got.input.Value() != tt.draft || !got.input.Focused() {
				t.Fatalf("error recovery state=%v draft=%q focused=%v", got.state, got.input.Value(), got.input.Focused())
			}
			output := stripAnsi(got.output.Content())
			if !strings.Contains(output, tt.want) {
				t.Fatalf("error recovery missing %q:\n%s", tt.want, output)
			}
			if tt.history != "" && strings.Contains(output, tt.history) {
				t.Fatalf("recovery hint exposed submitted message contents:\n%s", output)
			}
		})
	}
}

func TestRequestTimeoutShowsHistoryRecoveryAndCancelsBackend(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.state = StateStreaming
	m.input.AddToHistory("last submitted request")
	m.input.Blur()
	m.streamStartTime = time.Now().Add(-m.streamTimeout - time.Minute)
	cancelled := false
	m.SetCancelCallback(func() { cancelled = true })

	updated, _ := m.Update(spinner.TickMsg{})
	got := updated.(Model)
	if got.state != StateInput || !got.input.Focused() || !cancelled {
		t.Fatalf("timeout recovery state=%v focused=%v cancelled=%v", got.state, got.input.Focused(), cancelled)
	}
	output := stripAnsi(got.output.Content())
	for _, want := range []string{"request timed out", "Press ↑ to restore your last message and retry"} {
		if !strings.Contains(output, want) {
			t.Fatalf("timeout recovery missing %q:\n%s", want, output)
		}
	}
	if got.input.Value() != "" {
		t.Fatalf("timeout recovery unexpectedly restored history into composer: %q", got.input.Value())
	}
}
