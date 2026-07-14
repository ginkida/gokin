package ui

import (
	"errors"
	"testing"
)

func TestClipboardCopyOutcomeDistinguishesConfirmedAndFallback(t *testing.T) {
	var systemText, oscText string
	confirmed := copyTextToClipboardUsing("payload",
		func(text string) error { systemText = text; return nil },
		func(text string) { oscText = text },
	)
	if confirmed.SystemError != nil || !confirmed.OSC52Sent || systemText != "payload" || oscText != "payload" {
		t.Fatalf("confirmed copy outcome=%+v system=%q osc=%q", confirmed, systemText, oscText)
	}

	fallback := copyTextToClipboardUsing("payload",
		func(string) error { return errors.New("clipboard command missing") },
		func(text string) { oscText = text },
	)
	if fallback.SystemError == nil || !fallback.OSC52Sent || oscText != "payload" {
		t.Fatalf("fallback copy outcome=%+v osc=%q", fallback, oscText)
	}

	failed := copyTextToClipboardUsing("payload", nil, nil)
	if failed.SystemError == nil || failed.OSC52Sent {
		t.Fatalf("unavailable copy outcome=%+v", failed)
	}
}

func TestClipboardFeedbackNeverClaimsFallbackWasCopied(t *testing.T) {
	manager := NewToastManager(DefaultStyles())
	showClipboardCopyFeedback(manager, "Copied result path", clipboardCopyOutcome{
		SystemError: errors.New("native unavailable"),
		OSC52Sent:   true,
	})
	active := manager.Active()
	if len(active) != 1 || active[0].Type != ToastWarning || active[0].Message != "Copy request sent via terminal · system clipboard unavailable" {
		t.Fatalf("fallback feedback=%+v", active)
	}

	manager = NewToastManager(DefaultStyles())
	showClipboardCopyFeedback(manager, "Copied result path", clipboardCopyOutcome{})
	active = manager.Active()
	if len(active) != 1 || active[0].Type != ToastSuccess || active[0].Message != "Copied result path" {
		t.Fatalf("confirmed feedback=%+v", active)
	}

	manager = NewToastManager(DefaultStyles())
	showClipboardCopyFeedback(manager, "Copied result path", clipboardCopyOutcome{SystemError: errors.New("native unavailable")})
	active = manager.Active()
	if len(active) != 1 || active[0].Type != ToastError || active[0].Message != "Could not copy · system clipboard unavailable" {
		t.Fatalf("failed feedback=%+v", active)
	}
}
