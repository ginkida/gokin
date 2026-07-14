package ui

import (
	"strings"
	"testing"
)

func TestQueueRejectionRestoresMessageToEmptyComposer(t *testing.T) {
	m := NewModel()
	m.state = StateStreaming
	m.queuedPending = 4
	m.input.AddToHistory("rejected follow-up")

	_ = m.handleMessageTypes(QueuedMessageRejectedMsg{
		Message: "rejected follow-up",
		Reason:  "Queue full\nFORGED",
		Waiting: 5,
	})
	if got := m.input.Value(); got != "rejected follow-up" {
		t.Fatalf("rejected message restored as %q", got)
	}
	if m.queuedPending != 5 {
		t.Fatalf("queue badge=%d, want authoritative 5", m.queuedPending)
	}
	toast := stripAnsi(m.toastManager.View(100))
	if !strings.Contains(toast, "restored to composer") || strings.Contains(toast, "\n") {
		t.Fatalf("rejection feedback is missing or injectable: %q", toast)
	}
	if output := stripAnsi(m.output.state.content.String()); !strings.Contains(output, "not queued") {
		t.Fatalf("scrollback lacks durable rejection marker: %q", output)
	}
}

func TestQueueRejectionRecollapsesLargePaste(t *testing.T) {
	m := NewModel()
	m.state = StateProcessing
	large := strings.Repeat("large pasted line\n", 20)
	_ = m.handleMessageTypes(QueuedMessageRejectedMsg{Message: large, Reason: "Queue full", Waiting: 5})

	if visible := m.input.Value(); !strings.Contains(visible, "[Pasted text #") {
		t.Fatalf("large rejected paste expanded into composer: %q", visible)
	}
	if expanded := m.input.ExpandedValue(); expanded != strings.TrimSpace(large) {
		t.Fatalf("collapsed rejected paste did not round-trip: got %d bytes, want %d", len(expanded), len(strings.TrimSpace(large)))
	}
}

func TestQueueRejectionNeverClobbersNewerDraft(t *testing.T) {
	m := NewModel()
	m.state = StateStreaming
	m.input.textarea.SetValue("newer draft")
	m.input.AddToHistory("rejected older message")

	_ = m.handleMessageTypes(QueuedMessageRejectedMsg{
		Message: "rejected older message", Reason: "Queue full", Waiting: 5,
	})
	if got := m.input.Value(); got != "newer draft" {
		t.Fatalf("queue rejection clobbered newer draft with %q", got)
	}
	toast := stripAnsi(m.toastManager.View(120))
	for _, want := range []string{"current draft kept", "history"} {
		if !strings.Contains(toast, want) {
			t.Fatalf("non-destructive recovery missing %q: %q", want, toast)
		}
	}
	if history := m.input.GetHistory(); len(history) == 0 || history[len(history)-1] != "rejected older message" {
		t.Fatalf("rejected message unavailable in history: %v", history)
	}
}

func TestQueuedCountClampsInvalidNegativeValue(t *testing.T) {
	m := NewModel()
	_ = m.handleMessageTypes(QueuedCountMsg(-7))
	if m.queuedPending != 0 {
		t.Fatalf("negative queue count leaked into UI state: %d", m.queuedPending)
	}
}
