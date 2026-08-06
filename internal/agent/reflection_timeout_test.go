package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/testkit"
)

func TestSemanticReflectionUsesLiveModelRoundTimeout(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText(`{
		"category":"logic_error",
		"suggestion":"retry with corrected state",
		"should_retry":true,
		"alternative":"",
		"root_cause":"opaque state mismatch"
	}`)
	deadlineRemaining := make(chan time.Duration, 1)
	mock.OnSend = func(ctx context.Context) {
		deadline, ok := ctx.Deadline()
		if !ok {
			deadlineRemaining <- 0
			return
		}
		deadlineRemaining <- time.Until(deadline)
	}

	r := NewReflector()
	r.SetClient(mock)
	r.SetSemanticTimeout(42 * time.Minute)
	reflection := r.Reflect(context.Background(), "custom_tool", nil,
		"frobnicator entered an opaque state")
	if reflection == nil || reflection.Category != "logic_error" || !reflection.ShouldRetry {
		t.Fatalf("semantic reflection = %#v, want retryable logic_error", reflection)
	}
	remaining := <-deadlineRemaining
	if remaining < 41*time.Minute || remaining > 43*time.Minute {
		t.Fatalf("semantic reflection deadline = %v, want approximately 42m", remaining)
	}
	if got := r.SemanticTimeout(); got != 42*time.Minute {
		t.Fatalf("SemanticTimeout() = %v, want 42m", got)
	}
	r.SetSemanticTimeout(0)
	if got := r.SemanticTimeout(); got != client.DefaultModelRoundTimeout {
		t.Fatalf("zero semantic timeout = %v, want default %v", got, client.DefaultModelRoundTimeout)
	}
}

func TestAgentModelRoundTimeoutPropagatesToSemanticReflection(t *testing.T) {
	a := &Agent{reflector: NewReflector()}
	a.SetModelRoundTimeout(39 * time.Minute)
	if got := a.reflector.SemanticTimeout(); got != 39*time.Minute {
		t.Fatalf("reflector timeout = %v, want 39m", got)
	}
}

type unterminatedReflectionClient struct {
	*testkit.MockClient
	cause chan error
}

func (c *unterminatedReflectionClient) SendMessage(ctx context.Context, _ string) (*client.StreamingResponse, error) {
	chunks := make(chan client.ResponseChunk)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(chunks)
		payload := `{"category":"logic_error","suggestion":"retry this prefix","should_retry":true,"alternative":"","root_cause":"unterminated stream"}`
		select {
		case chunks <- client.ResponseChunk{Text: payload}:
		case <-ctx.Done():
			return
		}
		<-ctx.Done()
		c.cause <- client.ContextErr(ctx)
	}()
	return &client.StreamingResponse{Chunks: chunks, Done: done}, nil
}

func TestSemanticReflectionRejectsCompleteJSONFromTimedOutStream(t *testing.T) {
	c := &unterminatedReflectionClient{
		MockClient: testkit.NewMockClient(),
		cause:      make(chan error, 1),
	}
	r := NewReflector()
	r.SetClient(c)
	r.SetSemanticTimeout(25 * time.Millisecond)

	if got := r.semanticAnalyze(context.Background(), "custom_tool", nil, "opaque unterminated response"); got != nil {
		t.Fatalf("timed-out unterminated response was accepted: %#v", got)
	}
	select {
	case cause := <-c.cause:
		if !errors.Is(cause, client.ErrModelRoundTimeout) {
			t.Fatalf("reflection stream cause = %v, want model-round timeout", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("reflection producer did not observe timeout")
	}
	r.semanticCacheMu.Lock()
	cacheEntries := len(r.semanticCache)
	r.semanticCacheMu.Unlock()
	if cacheEntries != 0 {
		t.Fatalf("timed-out reflection populated %d cache entries", cacheEntries)
	}
}

func TestReflectorRuntimeSemanticUpdatesAreRaceSafe(t *testing.T) {
	r := NewReflector()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; i <= 500; i++ {
			r.SetSemanticTimeout(time.Duration(i) * time.Millisecond)
			r.SetSemanticAnalysis(i%2 == 0)
			r.SetClient(testkit.NewMockClient())
		}
	}()
	for i := 0; i < 500; i++ {
		_ = r.SemanticTimeout()
		_ = r.semanticAnalyze(context.Background(), "custom_tool", nil, "opaque state")
	}
	<-done
}
