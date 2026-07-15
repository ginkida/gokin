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

func TestMinimalBusyStatusKeepsCancelAheadOfModelSwitch(t *testing.T) {
	for _, width := range []int{10, 12, 20, 40, 59} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			m := NewModel()
			m.width = width
			m.state = StateStreaming
			m.queuedPending = 7
			m.modelSwitchPending = "model-with-a-long-pending-name"

			got := stripAnsi(m.renderStatusBar())
			if !strings.Contains(got, "esc") || !strings.Contains(got, "📥7") {
				t.Fatalf("model-switch status displaced recovery at width %d: %q", width, got)
			}
			if gotWidth := lipgloss.Width(m.renderStatusBar()); gotWidth > width {
				t.Fatalf("status width=%d exceeds terminal width=%d: %q", gotWidth, width, got)
			}
		})
	}
}

func TestMediumBusyStatusKeepsSafetyAndLiveStateBeforeProject(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.state = StateStreaming
	m.permissionsEnabled = false
	m.sandboxEnabled = false
	m.workDir = "/Users/example/projects/a/very/long/repository/path/that/would-fill-the-status-bar"
	m.currentModel = "model-with-a-very-long-identity"

	got := stripAnsi(m.renderStatusBar())
	for _, want := range []string{"YOLO", "!SANDBOX", "WRITING", "Esc interrupt"} {
		if !strings.Contains(got, want) {
			t.Fatalf("medium busy status lost priority signal %q: %q", want, got)
		}
	}
	if gotWidth := lipgloss.Width(m.renderStatusBar()); gotWidth > m.width {
		t.Fatalf("status width=%d exceeds terminal width=%d: %q", gotWidth, m.width, got)
	}
}
