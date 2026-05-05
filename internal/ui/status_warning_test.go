package ui

import (
	"strings"
	"testing"
	"time"
)

func TestStatusWarning_UsesTaggedWarningToast(t *testing.T) {
	model := NewModel()

	updated, _ := model.Update(StatusUpdateMsg{
		Type:    StatusWarning,
		Message: "Kimi loop guard: repeated grep \"loop guard\" 5 times. Sent a recovery hint instead of rerunning it.",
		Details: map[string]any{"tag": "loop-guard"},
	})
	m := updated.(Model)

	if m.toastManager == nil {
		t.Fatal("toastManager is nil")
	}
	if got := m.toastManager.Count(); got != 1 {
		t.Fatalf("toast count = %d, want 1", got)
	}
	if got := m.toastManager.toasts[0].Message; got == "" {
		t.Fatal("warning toast message is empty")
	}
	if got := m.toastManager.toasts[0].Type; got != ToastWarning {
		t.Fatalf("toast type = %v, want warning", got)
	}

	updated, _ = m.Update(StatusUpdateMsg{
		Type:    StatusWarning,
		Message: "Kimi loop guard: repeated glob \"**/*.go\" in internal/tools 5 times. Sent a recovery hint instead of rerunning it.",
		Details: map[string]any{"tag": "loop-guard"},
	})
	m = updated.(Model)

	if got := m.toastManager.Count(); got != 1 {
		t.Fatalf("tagged warning should replace existing toast, count = %d, want 1", got)
	}
	if got := m.toastManager.toasts[0].Message; got != "Kimi loop guard: repeated glob \"**/*.go\" in internal/tools 5 times. Sent a recovery hint instead of rerunning it." {
		t.Fatalf("tagged warning toast not replaced, got %q", got)
	}
	if got := m.toastManager.toasts[0].Duration; got != 4*time.Second {
		t.Fatalf("warning duration = %v, want 4s", got)
	}
}

func TestStatusInfoPhaseLabelUpdatesLiveActivityWithoutToast(t *testing.T) {
	model := NewModel()
	model.width = 100

	updated, _ := model.Update(StatusUpdateMsg{
		Type:    StatusInfo,
		Message: "Quality gate 1/2: go_vet@.",
		Details: map[string]any{
			"phaseLabel": "Quality gate 1/2: go vet .",
			"silent":     true,
		},
	})
	m := updated.(Model)

	if m.state != StateProcessing {
		t.Fatalf("state = %v, want StateProcessing", m.state)
	}
	if got := m.processingLabel; got != "Quality gate 1/2: go vet ." {
		t.Fatalf("processingLabel = %q", got)
	}
	if got := m.toastManager.Count(); got != 0 {
		t.Fatalf("silent phase update should not create toast, count = %d", got)
	}

	view := stripAnsi(m.renderLiveActivityCard())
	if !strings.Contains(view, "Quality gate 1/2: go vet .") {
		t.Fatalf("live activity missing phase label:\n%s", view)
	}
}
