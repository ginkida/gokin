package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestBusyStatusMakesFollowUpDiscoverableAndContextual(t *testing.T) {
	m := NewModel()
	m.SetCallbacks(func(string) {}, nil)
	m.width = 100
	m.state = StateStreaming

	if got := stripAnsi(m.renderStatusBar()); !strings.Contains(got, "Type follow-up · Esc interrupt") {
		t.Fatalf("empty busy composer does not advertise follow-up entry: %q", got)
	}

	m.input.textarea.SetValue("change direction")
	if got := stripAnsi(m.renderStatusBar()); !strings.Contains(got, "Enter send · Esc interrupt") {
		t.Fatalf("composed follow-up does not advertise submit action: %q", got)
	}

	m.input.historySearchMode = true
	if got := stripAnsi(m.renderStatusBar()); !strings.Contains(got, "Enter use · Esc interrupt") {
		t.Fatalf("history search advertises the wrong Enter action: %q", got)
	}
}

func TestBusyStatusDescribesSuggestionEnterHonestly(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.state = StateProcessing
	m.input.textarea.SetValue("/he")
	m.input.showSuggestions = true
	m.input.suggestions = []CommandInfo{{Name: "help"}}

	if got := stripAnsi(m.renderStatusBar()); !strings.Contains(got, "Enter complete · Esc interrupt") {
		t.Fatalf("open suggestions advertise submit instead of completion: %q", got)
	}
}

func TestMinimalBusyStatusPreservesQueueAndCancelAtEveryWidth(t *testing.T) {
	for _, width := range []int{10, 12, 20, 40, 59} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			m := NewModel()
			m.width = width
			m.state = StateStreaming
			m.currentModel = "model-with-a-very-long-identity"
			m.permissionsEnabled = false
			m.sandboxEnabled = false
			m.queuedPending = 7

			got := stripAnsi(m.renderStatusBar())
			if !strings.Contains(got, "📥7") || !strings.Contains(got, "esc") {
				t.Fatalf("critical busy signals were truncated at width %d: %q", width, got)
			}
			if gotWidth := lipgloss.Width(m.renderStatusBar()); gotWidth > width {
				t.Fatalf("status width=%d exceeds terminal width=%d: %q", gotWidth, width, got)
			}
		})
	}
}
