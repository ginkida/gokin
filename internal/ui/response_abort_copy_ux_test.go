package ui

import (
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

func TestInterruptedResponseDoesNotContaminateNextCopyPayload(t *testing.T) {
	m := *NewModel()
	m.output.SetSize(80, 10)

	updated, _ := m.Update(StreamTextMsg("cancelled partial"))
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(Model)

	if got := m.currentResponseBuf.String(); got != "" {
		t.Fatalf("interrupt retained partial response buffer: %q", got)
	}

	updated, _ = m.Update(StreamTextMsg("fresh"))
	m = updated.(Model)
	updated, _ = m.Update(ResponseDoneMsg{})
	m = updated.(Model)

	if got := m.lastResponseText; got != "fresh" {
		t.Fatalf("next copy payload = %q, want %q", got, "fresh")
	}
	if got := m.currentResponseBuf.String(); got != "" {
		t.Fatalf("completed response buffer was not cleared: %q", got)
	}
}

func TestTimedOutResponseDoesNotContaminateNextCopyPayload(t *testing.T) {
	m := *NewModel()
	m.output.SetSize(80, 10)
	m.streamTimeout = time.Second

	updated, _ := m.Update(StreamTextMsg("cancelled partial"))
	m = updated.(Model)
	m.streamStartTime = time.Now().Add(-2 * m.streamTimeout)
	updated, _ = m.Update(spinner.TickMsg{})
	m = updated.(Model)

	if got := m.currentResponseBuf.String(); got != "" {
		t.Fatalf("watchdog timeout retained partial response buffer: %q", got)
	}

	updated, _ = m.Update(StreamTextMsg("fresh"))
	m = updated.(Model)
	updated, _ = m.Update(ResponseDoneMsg{})
	m = updated.(Model)

	if got := m.lastResponseText; got != "fresh" {
		t.Fatalf("next copy payload = %q, want %q", got, "fresh")
	}
	if got := m.currentResponseBuf.String(); got != "" {
		t.Fatalf("completed response buffer was not cleared: %q", got)
	}
}
