package client

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProcessStream_ModelRoundTimeoutReturnsAccumulatedResponse(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	chunks := make(chan ResponseChunk, 1)
	chunks <- ResponseChunk{
		Thinking:     "checked the repository",
		Text:         "partial answer",
		InputTokens:  120,
		OutputTokens: 9,
	}

	cause := NewModelRoundTimeoutError(14 * time.Minute)
	resp, err := ProcessStream(ctx, &StreamingResponse{Chunks: chunks}, &StreamHandler{
		// Token metadata is processed after text/thinking in the same chunk. Cancel
		// here so the next select deterministically takes the timeout path only
		// after all of this chunk has been accumulated.
		OnTokenUpdate: func(_, _ int) { cancel(cause) },
	})

	if !errors.Is(err, ErrModelRoundTimeout) {
		t.Fatalf("ProcessStream() error = %v, want ErrModelRoundTimeout", err)
	}
	if resp == nil {
		t.Fatal("ProcessStream() response = nil, want accumulated partial response")
	}
	if resp.Text != "partial answer" || resp.Thinking != "checked the repository" {
		t.Fatalf("partial response text/thinking = %q/%q", resp.Text, resp.Thinking)
	}
	if resp.InputTokens != 120 || resp.OutputTokens != 9 {
		t.Fatalf("partial response usage = (%d,%d), want (120,9)", resp.InputTokens, resp.OutputTokens)
	}
	telemetry := DetectFailureTelemetry(err)
	if telemetry.Reason != string(FailureReasonModelRoundTimeout) || !telemetry.Partial {
		t.Fatalf("timeout telemetry = %#v, want model_round_timeout with partial=true", telemetry)
	}
}

func TestCollectText_ReturnsPartialTextWithContextFailure(t *testing.T) {
	ctx, cancel := context.WithTimeoutCause(
		context.Background(), 20*time.Millisecond, NewModelRoundTimeoutError(20*time.Millisecond))
	defer cancel()

	chunks := make(chan ResponseChunk, 1)
	chunks <- ResponseChunk{Text: "keep this"}
	text, err := CollectText(ctx, &StreamingResponse{Chunks: chunks})
	if !errors.Is(err, ErrModelRoundTimeout) {
		t.Fatalf("CollectText() error = %v, want ErrModelRoundTimeout", err)
	}
	if text != "keep this" {
		t.Fatalf("CollectText() text = %q, want preserved partial text", text)
	}
}
