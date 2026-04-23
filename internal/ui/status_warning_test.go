package ui

import (
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
