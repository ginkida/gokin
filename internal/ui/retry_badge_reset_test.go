package ui

import "testing"

// TestMarkResponseStarted_ClearsStaleRetryBadge (round 7) pins the fix: a
// retried request whose new stream starts and completes cleanly, with no
// idle gap, never triggered StatusStreamResume (the previous — and only —
// place retryAttempt/retryMax were cleared), leaving a stale "retry N/M"
// badge shown for every subsequent turn until an unrelated event (e.g. an
// MCP server reconnecting, which reuses the same StatusStreamResume message
// type for an unrelated purpose) happened to clear it. The first real
// content of ANY new response is the correct "no longer retrying" signal.
func TestMarkResponseStarted_ClearsStaleRetryBadge(t *testing.T) {
	m := NewModel()

	// Simulate a retry-in-progress toast, as message_processor.go's retry
	// loop emits before the backoff wait.
	m.handleMessageTypes(StatusUpdateMsg{
		Type:    StatusRetry,
		Message: "Retry 1/3 in 3s (server error)",
		Details: map[string]any{"attempt": 1, "maxAttempts": 3},
	})
	if m.retryAttempt != 1 || m.retryMax != 3 {
		t.Fatalf("test setup invalid: retryAttempt=%d retryMax=%d, want 1/3", m.retryAttempt, m.retryMax)
	}

	// The retried request's NEW stream starts producing text directly, with
	// no idle gap — StatusStreamResume never fires for this stream.
	m.handleMessageTypes(StreamTextMsg("Here's the answer"))

	if m.retryAttempt != 0 || m.retryMax != 0 {
		t.Fatalf("stale retry badge not cleared by fresh response content: retryAttempt=%d retryMax=%d", m.retryAttempt, m.retryMax)
	}
}

// TestMarkResponseStarted_OnlyClearsOnFirstChunk confirms the fix doesn't
// clear a NEWLY-set retry badge mid-response (markResponseStarted only acts
// on the FIRST chunk of a response, per responseHeaderShown).
func TestMarkResponseStarted_OnlyClearsOnFirstChunk(t *testing.T) {
	m := NewModel()

	m.handleMessageTypes(StreamTextMsg("first chunk"))
	if !m.responseHeaderShown {
		t.Fatal("test setup invalid: responseHeaderShown should be true after the first chunk")
	}

	// A retry badge appearing mid-response (e.g. a partial-stream-idle retry
	// starting a NEW round within the same turn) must not be silently wiped
	// by a later markResponseStarted call, since responseHeaderShown is
	// already true and the guard skips the body entirely.
	m.handleMessageTypes(StatusUpdateMsg{
		Type:    StatusRetry,
		Message: "Retry 1/2 in 3s (stream stalled)",
		Details: map[string]any{"attempt": 1, "maxAttempts": 2},
	})
	m.handleMessageTypes(StreamTextMsg("more text"))

	if m.retryAttempt != 1 || m.retryMax != 2 {
		t.Fatalf("mid-response retry badge should survive a second markResponseStarted call: retryAttempt=%d retryMax=%d", m.retryAttempt, m.retryMax)
	}
}
