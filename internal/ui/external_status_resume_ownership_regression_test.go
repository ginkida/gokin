package ui

import (
	"strings"
	"testing"
	"time"
)

// StatusStreamResume is also used by asynchronous MCP health/tool-list
// notifications. A non-empty message identifies that external notification;
// it does not own the foreground request's retry/idle bookkeeping. Treating it
// as a foreground stream-resume event both hides the MCP feedback and clears a
// still-retrying turn's status.
func TestExternalStatusResumeDoesNotSettleForegroundRecoveryState(t *testing.T) {
	m := NewModel()
	m.state = StateStreaming
	m.responseHeaderShown = true
	m.retryAttempt = 2
	m.retryMax = 4
	m.streamIdleMsg = "Foreground stream is still waiting"
	m.rateLimitWaitUntil = time.Now().Add(time.Minute)
	m.lastRecoverableStatus = "Foreground provider is recovering"

	beforeWait := m.rateLimitWaitUntil
	_ = m.handleMessageTypes(StatusUpdateMsg{
		Type:    StatusStreamResume,
		Message: `MCP server "database" reconnected`,
	})

	if m.retryAttempt != 2 || m.retryMax != 4 {
		t.Errorf("external MCP event cleared foreground retry owner: retry=%d/%d", m.retryAttempt, m.retryMax)
	}
	if m.streamIdleMsg != "Foreground stream is still waiting" {
		t.Errorf("external MCP event cleared foreground idle feedback: %q", m.streamIdleMsg)
	}
	if !m.rateLimitWaitUntil.Equal(beforeWait) {
		t.Errorf("external MCP event cleared foreground rate-limit deadline: got=%v want=%v", m.rateLimitWaitUntil, beforeWait)
	}
	if m.lastRecoverableStatus != "Foreground provider is recovering" {
		t.Errorf("external MCP event cleared foreground recovery episode: %q", m.lastRecoverableStatus)
	}

	active := m.toastManager.Active()
	if len(active) != 1 || active[0].Type != ToastInfo || !strings.Contains(active[0].Message, "MCP server") {
		t.Errorf("external MCP recovery feedback was not shown: %+v", active)
	}
}

func TestEmptyStatusResumeSettlesForegroundRecoveryState(t *testing.T) {
	m := NewModel()
	m.retryAttempt = 2
	m.retryMax = 4
	m.streamIdleMsg = "Foreground stream is still waiting"
	m.rateLimitWaitUntil = time.Now().Add(time.Minute)
	m.lastRecoverableStatus = "Foreground provider is recovering"

	_ = m.handleMessageTypes(StatusUpdateMsg{Type: StatusStreamResume})

	if m.retryAttempt != 0 || m.retryMax != 0 || m.streamIdleMsg != "" ||
		!m.rateLimitWaitUntil.IsZero() || m.lastRecoverableStatus != "" {
		t.Fatalf("empty foreground resume did not settle recovery state: retry=%d/%d idle=%q wait=%v recovery=%q",
			m.retryAttempt, m.retryMax, m.streamIdleMsg, m.rateLimitWaitUntil, m.lastRecoverableStatus)
	}
	if active := m.toastManager.Active(); len(active) != 0 {
		t.Fatalf("empty foreground resume created a notification: %+v", active)
	}
}
