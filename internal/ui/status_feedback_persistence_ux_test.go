package ui

import (
	"strings"
	"testing"
)

func TestStatusPayloadIsSanitizedBeforePersistentLiveState(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.state = StateProcessing

	_ = m.handleMessageTypes(StatusUpdateMsg{
		Type:    StatusStreamIdle,
		Message: "\x1b[2J Waiting for provider\nFORGED ROW ",
	})
	if m.streamIdleMsg != "Waiting for provider FORGED ROW" {
		t.Fatalf("idle state retained unsafe payload: %q", m.streamIdleMsg)
	}
	if strings.Contains(m.renderLiveActivityCard(false), "\x1b[2J") {
		t.Fatal("terminal control sequence reached live activity card")
	}

	_ = m.handleMessageTypes(StatusUpdateMsg{
		Type:    StatusInfo,
		Message: "phase",
		Details: map[string]any{"phaseLabel": "\x1b[31m Verify\nFORGED LABEL ", "silent": true},
	})
	if m.processingLabel != "Verify FORGED LABEL" {
		t.Fatalf("phase label retained unsafe payload: %q", m.processingLabel)
	}

	_ = m.handleMessageTypes(ActivityLabelMsg(" Implementing\nFORGED ACTIVITY "))
	if m.currentActivity != "Implementing FORGED ACTIVITY" {
		t.Fatalf("activity label retained multiline payload: %q", m.currentActivity)
	}
}

func TestRecoverableStatusRemainsInScrollbackAndDeduplicates(t *testing.T) {
	m := NewModel()
	unsafe := "\x1b[2J Login failed:\ninvalid key"

	_ = m.handleMessageTypes(StatusUpdateMsg{Type: StatusRecoverableError, Message: unsafe})
	_ = m.handleMessageTypes(StatusUpdateMsg{Type: StatusRecoverableError, Message: "Login failed: invalid   key"})

	output := stripAnsi(m.output.state.content.String())
	if strings.Count(output, "Login failed: invalid key") != 1 {
		t.Fatalf("recoverable status was lost or duplicated:\n%s", output)
	}
	if strings.Contains(m.output.state.content.String(), "\x1b[2J") {
		t.Fatalf("recoverable status retained terminal control sequence: %q", m.output.state.content.String())
	}
	if m.toastManager.Count() != 1 || m.toastManager.toasts[0].Message == "" {
		t.Fatalf("recoverable toast missing or duplicated: %+v", m.toastManager.toasts)
	}
}

func TestEmptyCriticalStatusesHaveMeaningfulFallbacks(t *testing.T) {
	for _, tc := range []struct {
		status StatusType
		want   string
	}{
		{StatusRateLimit, "Rate limit reached"},
		{StatusRecoverableError, "Recoverable error"},
		{StatusCancelled, "Operation cancelled"},
	} {
		m := NewModel()
		_ = m.handleMessageTypes(StatusUpdateMsg{Type: tc.status})
		if m.toastManager.Count() != 1 || m.toastManager.toasts[0].Message != tc.want {
			t.Fatalf("status=%v fallback toast=%+v, want %q", tc.status, m.toastManager.toasts, tc.want)
		}
	}
}
