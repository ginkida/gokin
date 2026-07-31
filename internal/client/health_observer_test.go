package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/genai"
)

type selfObservingFallbackStub struct {
	fakeFallbackClientStub
	provider       string
	directTracking bool
}

func (s *selfObservingFallbackStub) setDirectHealthTracking(enabled bool) {
	s.directTracking = enabled
}

func (s *selfObservingFallbackStub) observedSend(ctx context.Context) (*StreamingResponse, error) {
	stream, err := s.fakeFallbackClientStub.send(ctx)
	if err == nil && s.directTracking {
		stream = observeProviderStream(ctx, s.provider, stream)
	}
	return stream, err
}

func (s *selfObservingFallbackStub) SendMessage(ctx context.Context, _ string) (*StreamingResponse, error) {
	return s.observedSend(ctx)
}

func (s *selfObservingFallbackStub) SendMessageWithHistory(
	ctx context.Context,
	_ []*genai.Content,
	_ string,
) (*StreamingResponse, error) {
	return s.observedSend(ctx)
}

func (s *selfObservingFallbackStub) SendFunctionResponse(
	ctx context.Context,
	_ []*genai.Content,
	_ []*genai.FunctionResponse,
) (*StreamingResponse, error) {
	return s.observedSend(ctx)
}

func TestObserveProviderStreamRecordsSuccessfulTerminalOutcome(t *testing.T) {
	provider := "test-direct-health-success"
	before := getProviderHealth(provider)
	observed := observeProviderStream(context.Background(), provider, fallbackTestStream(
		ResponseChunk{Text: "ok", Done: true},
	))

	response, err := ProcessStream(context.Background(), observed, &StreamHandler{})
	if err != nil || response.Text != "ok" {
		t.Fatalf("response/error = %#v/%v", response, err)
	}
	<-observed.Done
	after := getProviderHealth(provider)
	if after.Score != before.Score+1 || after.FailureStreak != 0 {
		t.Fatalf("health before/after = %+v/%+v, want one success", before, after)
	}
}

func TestObserveProviderStreamRecordsSSEFailure(t *testing.T) {
	provider := "test-direct-health-stream-failure"
	streamErr := errors.New("provider stream overloaded")
	before := getProviderHealth(provider)
	observed := observeProviderStream(context.Background(), provider, fallbackTestStream(
		ResponseChunk{Thinking: "partial reasoning"},
		ResponseChunk{Error: streamErr, Done: true},
	))

	response, err := ProcessStream(context.Background(), observed, &StreamHandler{})
	if !errors.Is(err, streamErr) || response == nil || response.Thinking == "" {
		t.Fatalf("response/error = %#v/%v, want partial response and stream error", response, err)
	}
	<-observed.Done
	after := getProviderHealth(provider)
	if after.Score >= before.Score || after.FailureStreak != before.FailureStreak+1 {
		t.Fatalf("health before/after = %+v/%+v, want one failure", before, after)
	}
}

func TestObserveProviderStreamCancellationDoesNotChangeHealth(t *testing.T) {
	provider := "test-direct-health-cancel"
	before := getProviderHealth(provider)
	source := make(chan ResponseChunk)
	ctx, cancel := context.WithCancel(context.Background())
	observed := observeProviderStream(ctx, provider, &StreamingResponse{Chunks: source})
	cancel()
	<-observed.Done

	after := getProviderHealth(provider)
	if after.Score != before.Score || after.FailureStreak != before.FailureStreak {
		t.Fatalf("cancellation changed health: before=%+v after=%+v", before, after)
	}
}

func TestObserveProviderStreamEmptyCompletionCountsAsFailure(t *testing.T) {
	provider := "test-direct-health-empty"
	before := getProviderHealth(provider)
	observed := observeProviderStream(context.Background(), provider, fallbackTestStream(
		ResponseChunk{Done: true},
	))
	if _, err := ProcessStream(context.Background(), observed, &StreamHandler{}); err != nil {
		t.Fatalf("observer must preserve the underlying empty-stream behavior: %v", err)
	}
	<-observed.Done

	after := getProviderHealth(provider)
	if after.Score >= before.Score || after.FailureStreak != before.FailureStreak+1 {
		t.Fatalf("health before/after = %+v/%+v, want empty-response failure", before, after)
	}
}

func TestObserveProviderStreamSafetyRejectionDoesNotPenalizeHealth(t *testing.T) {
	provider := "test-direct-health-safety"
	before := getProviderHealth(provider)
	safetyErr := &TerminalProviderError{Code: "1301", Message: "sensitive content rejected"}
	observed := observeProviderStream(context.Background(), provider, fallbackTestStream(
		ResponseChunk{Error: safetyErr, Done: true},
	))
	if _, err := ProcessStream(context.Background(), observed, &StreamHandler{}); !errors.Is(err, safetyErr) {
		t.Fatalf("ProcessStream error=%v, want safety rejection", err)
	}
	<-observed.Done

	after := getProviderHealth(provider)
	if after.Score != before.Score || after.FailureStreak != before.FailureStreak {
		t.Fatalf("request-scoped safety rejection changed health: before=%+v after=%+v", before, after)
	}
}

func TestAnthropicDirectClientRecordsRequestFailure(t *testing.T) {
	provider := "test-direct-health-http-failure"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary outage", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	direct, err := NewAnthropicClient(AnthropicConfig{
		APIKey:      "test-key",
		BaseURL:     server.URL,
		Provider:    provider,
		Model:       "test-model",
		MaxTokens:   1024,
		MaxRetries:  0,
		HTTPTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := getProviderHealth(provider)
	if _, err := direct.SendMessage(context.Background(), "hello"); err == nil {
		t.Fatal("SendMessage should surface the provider failure")
	}
	after := getProviderHealth(provider)
	if after.Score >= before.Score || after.FailureStreak != before.FailureStreak+1 {
		t.Fatalf("health before/after = %+v/%+v, want one request failure", before, after)
	}
}

func TestFallbackDisablesDirectChildHealthTracking(t *testing.T) {
	child, err := NewAnthropicClient(AnthropicConfig{
		APIKey:   "test-key",
		Provider: "test-fallback-owned-health",
		Model:    "test-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, enabled := child.directHealthState(); !enabled {
		t.Fatal("standalone client unexpectedly started with health tracking disabled")
	}
	fallback, err := NewFallbackClient(
		[]Client{child},
		[]string{"test-fallback-owned-health"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer fallback.Close()
	if _, enabled := child.directHealthState(); enabled {
		t.Fatal("fallback-owned child retained direct tracking and would double-count outcomes")
	}
}

func TestFallbackOutcomeIsNotDoubleCountedByBuiltInChildObserver(t *testing.T) {
	provider := "test-fallback-health-count-once"
	child := &selfObservingFallbackStub{
		fakeFallbackClientStub: fakeFallbackClientStub{
			model:    "test-model",
			sendResp: fallbackTestStream(ResponseChunk{Text: "ok", Done: true}),
		},
		provider:       provider,
		directTracking: true,
	}
	fallback, err := NewFallbackClient([]Client{child}, []string{provider})
	if err != nil {
		t.Fatal(err)
	}
	defer fallback.Close()

	before := getProviderHealth(provider)
	stream, err := fallback.SendMessage(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProcessStream(context.Background(), stream, &StreamHandler{}); err != nil {
		t.Fatal(err)
	}
	<-stream.Done
	after := getProviderHealth(provider)
	if after.Score != before.Score+1 {
		t.Fatalf("health score before/after=%d/%d, want exactly one success", before.Score, after.Score)
	}
}

func TestAnthropicTrackingModeSurvivesCloneAndWithModel(t *testing.T) {
	client, err := NewAnthropicClient(AnthropicConfig{
		APIKey:   "test-key",
		Provider: "test-health-clone",
		Model:    "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	client.setDirectHealthTracking(false)

	clone := client.cloneForSession().(*AnthropicClient)
	if _, enabled := clone.directHealthState(); enabled {
		t.Fatal("session clone re-enabled direct tracking")
	}
	switched := client.WithModel("second").(*AnthropicClient)
	if _, enabled := switched.directHealthState(); enabled {
		t.Fatal("WithModel re-enabled direct tracking")
	}
}
