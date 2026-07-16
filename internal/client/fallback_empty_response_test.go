package client

import (
	"context"
	"errors"
	"testing"
)

// The audit finding this fixes (v0.100.90): when EVERY fallback provider
// streams a "successful" but content-less completion (no text/thinking/tool
// calls), the exhausted error surfaced to the caller must still satisfy
// errors.Is(err, ErrEmptyModelResponse) so the outer retry/backoff layers
// (IsRetryableError, and the executor/agent's dedicated empty-response
// recovery) keep working once a fallback chain is configured. Before the
// fix, forwardFallbackCandidate returned a PLAIN fmt.Errorf with no %w —
// the sentinel never reached the caller, silently defeating retry.
func TestFallbackClient_AllProvidersEmpty_ErrorIsRetryableEmptyResponse(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", sendResp: fallbackTestStream(ResponseChunk{Done: true})}
	c2 := &fakeFallbackClientStub{id: "b", sendResp: fallbackTestStream(ResponseChunk{Done: true})}
	fc, err := NewFallbackClient([]Client{c1, c2}, []string{"test-fb-empty-0", "test-fb-empty-1"})
	if err != nil {
		t.Fatalf("NewFallbackClient: %v", err)
	}

	resp, err := fc.SendMessage(context.Background(), "hello")
	if err != nil {
		t.Fatalf("SendMessage() returned a synchronous error = %v, want the error to surface via the stream", err)
	}

	var streamErr error
	for chunk := range resp.Chunks {
		if chunk.Error != nil {
			streamErr = chunk.Error
		}
	}
	if streamErr == nil {
		t.Fatal("stream closed with no error, want the exhausted-fallback empty-response error")
	}
	if !errors.Is(streamErr, ErrEmptyModelResponse) {
		t.Fatalf("err = %v, want it to wrap ErrEmptyModelResponse so callers can detect the class", streamErr)
	}
	if !IsRetryableError(streamErr) {
		t.Fatalf("IsRetryableError(%v) = false, want true — an all-providers-empty exhaustion must stay retryable", streamErr)
	}
}

// A mid-chain provider streaming empty content must still fail over to the
// NEXT provider (unchanged behavior — forwardFallbackCandidate's error isn't
// a TerminalProviderError/HTTPError/APIError, so shouldFallbackToNextProvider
// already defaults to true); only the FINAL exhausted case needed the fix.
func TestFallbackClient_FirstProviderEmpty_FallsOverAndSucceeds(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", sendResp: fallbackTestStream(ResponseChunk{Done: true})}
	c2 := &fakeFallbackClientStub{id: "b", sendResp: fallbackTestStream(ResponseChunk{Text: "ok", Done: true})}
	fc, err := NewFallbackClient([]Client{c1, c2}, []string{"test-fb-empty-2", "test-fb-empty-3"})
	if err != nil {
		t.Fatalf("NewFallbackClient: %v", err)
	}

	resp, err := fc.SendMessage(context.Background(), "hello")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	var text string
	var streamErr error
	for chunk := range resp.Chunks {
		text += chunk.Text
		if chunk.Error != nil {
			streamErr = chunk.Error
		}
	}
	if streamErr != nil {
		t.Fatalf("unexpected stream error after fallover to a healthy provider: %v", streamErr)
	}
	if text != "ok" {
		t.Fatalf("text = %q, want the second provider's content", text)
	}
	if fc.getCurrent() != 1 {
		t.Errorf("current = %d, want 1 (failed over to second)", fc.getCurrent())
	}
}
