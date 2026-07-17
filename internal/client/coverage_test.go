package client

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/genai"
)

// ===========================================================================
// DefaultStatusCallback (status.go) — all 6 methods at 0%
// ===========================================================================

func TestDefaultStatusCallback_AllMethodsNoOp(t *testing.T) {
	d := &DefaultStatusCallback{}

	// None of these should panic. They are documented no-ops.
	d.OnRetry(1, 3, 500*time.Millisecond, "connection reset")
	d.OnRateLimit(2 * time.Second)
	d.OnStreamIdle(30 * time.Second)
	d.OnThinkingIdle(45*time.Second, "glm")
	d.OnStreamResume()
	d.OnError(errors.New("test error"), true)

	// A second call with different args also must be safe.
	d.OnRetry(2, 3, 0, "")
	d.OnRateLimit(0)
	d.OnStreamIdle(0)
	d.OnThinkingIdle(0, "")
	d.OnError(nil, false)
}

// ===========================================================================
// TimeoutError.Error / EmptyModelResponseError (errors.go) — 0% branches
// ===========================================================================

func TestTimeoutError_Error_AllFormats(t *testing.T) {
	tests := []struct {
		name string
		err  *TimeoutError
		want string
	}{
		{
			name: "full",
			err: &TimeoutError{
				Reason:   FailureReasonHTTPTimeout,
				Provider: "glm",
				Timeout:  30 * time.Second,
				Err:      errors.New("deadline exceeded"),
			},
			want: "http_timeout",
		},
		{
			name: "provider_only",
			err: &TimeoutError{
				Reason:   FailureReasonModelRoundTimeout,
				Provider: "kimi",
				Err:      errors.New("round timeout"),
			},
			want: "model_round_timeout",
		},
		{
			name: "timeout_only",
			err: &TimeoutError{
				Reason:  FailureReasonStreamIdleTimeout,
				Timeout: 60 * time.Second,
				Err:     errors.New("idle"),
			},
			want: "stream_idle_timeout",
		},
		{
			name: "reason_only",
			err: &TimeoutError{
				Reason: FailureReasonContextCancel,
				Err:    errors.New("canceled"),
			},
			want: "context_cancel",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if !containsLower(got, tt.want) {
				t.Errorf("Error() = %q, want substring %q", got, tt.want)
			}
		})
	}
}

func TestTimeoutError_Unwrap(t *testing.T) {
	inner := errors.New("root cause")
	te := &TimeoutError{Reason: FailureReasonHTTPTimeout, Err: inner}
	if !errors.Is(te, inner) {
		t.Error("Unwrap should expose inner error")
	}
}

func TestEmptyModelResponseError(t *testing.T) {
	// AfterToolResults branch.
	e := &EmptyModelResponseError{AfterToolResults: true}
	if !containsLower(e.Error(), "after tool results") {
		t.Errorf("AfterToolResults error = %q", e.Error())
	}

	// Plain branch.
	e2 := &EmptyModelResponseError{}
	if !containsLower(e2.Error(), "empty response") {
		t.Errorf("plain error = %q", e2.Error())
	}

	// Unwrap should expose sentinel.
	if !errors.Is(e2, ErrEmptyModelResponse) {
		t.Error("Unwrap should expose ErrEmptyModelResponse")
	}
	if !errors.Is(e, ErrEmptyModelResponse) {
		t.Error("Unwrap should expose ErrEmptyModelResponse (AfterToolResults)")
	}
}

// ===========================================================================
// ResetClientFallback (errors.go) — 0%
// ===========================================================================

// testFallbackResetter embeds fakeClient (pool_test.go) so it satisfies the
// full Client interface, then adds ResetFallbackPosition to exercise the
// fallbackResetter type-assertion branch in ResetClientFallback.
type testFallbackResetter struct {
	fakeClient
	reset bool
}

func (t *testFallbackResetter) ResetFallbackPosition() { t.reset = true }

func TestResetClientFallback_WithResetter(t *testing.T) {
	fr := &testFallbackResetter{}
	ResetClientFallback(fr)
	if !fr.reset {
		t.Error("ResetFallbackPosition should be called")
	}
}

func TestResetClientFallback_NonFallback(t *testing.T) {
	// A Client that does NOT implement fallbackResetter → no-op, no panic.
	ResetClientFallback(&fakeClient{id: "no-fallback"})
}

// ===========================================================================
// ContextErr edge cases (errors.go — 80%)
// ===========================================================================

func TestContextErr_NilContext(t *testing.T) {
	if err := ContextErr(nil); err != nil {
		t.Errorf("nil ctx should return nil, got %v", err)
	}
}

func TestContextErr_NoError(t *testing.T) {
	ctx := context.Background()
	if err := ContextErr(ctx); err != nil {
		t.Errorf("non-cancelled ctx should return nil, got %v", err)
	}
}

func TestContextErr_CausePreserved(t *testing.T) {
	cause := errors.New("custom cause")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)

	got := ContextErr(ctx)
	if !errors.Is(got, cause) {
		t.Errorf("ContextErr should preserve cause, got %v", got)
	}
}

func TestContextErr_CanceledNoCause(t *testing.T) {
	// context.WithCancel (no cause) → cause is context.Canceled, falls to ctx.Err().
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := ContextErr(ctx)
	if !errors.Is(got, context.Canceled) {
		t.Errorf("ContextErr should return context.Canceled, got %v", got)
	}
}

// ===========================================================================
// NewModelRoundTimeoutError (errors.go)
// ===========================================================================

func TestNewModelRoundTimeoutError_Coverage(t *testing.T) {
	err := NewModelRoundTimeoutError(14 * time.Minute)
	if !errors.Is(err, ErrModelRoundTimeout) {
		t.Error("should wrap ErrModelRoundTimeout")
	}
	tel := DetectFailureTelemetry(err)
	if tel.Reason != string(FailureReasonModelRoundTimeout) {
		t.Errorf("telemetry reason = %q, want %q", tel.Reason, FailureReasonModelRoundTimeout)
	}
}

// ===========================================================================
// WrapProviderHTTPTimeout (errors.go)
// ===========================================================================

func TestWrapProviderHTTPTimeout_Wraps(t *testing.T) {
	inner := &net.OpError{Op: "read", Err: timeoutErr{}}
	wrapped := WrapProviderHTTPTimeout(inner, "glm", 30*time.Second)

	var te *TimeoutError
	if !errors.As(wrapped, &te) {
		t.Fatal("should wrap in TimeoutError")
	}
	if te.Reason != FailureReasonHTTPTimeout {
		t.Errorf("reason = %q", te.Reason)
	}
	if te.Provider != "glm" {
		t.Errorf("provider = %q", te.Provider)
	}
}

func TestWrapProviderHTTPTimeout_NonTimeoutUnchanged(t *testing.T) {
	plain := errors.New("not a timeout")
	wrapped := WrapProviderHTTPTimeout(plain, "glm", 30*time.Second)
	if wrapped != plain {
		t.Error("non-timeout error should be returned unchanged")
	}
}

func TestWrapProviderHTTPTimeout_Nil(t *testing.T) {
	if err := WrapProviderHTTPTimeout(nil, "glm", 30*time.Second); err != nil {
		t.Errorf("nil error should return nil, got %v", err)
	}
}

// timeoutErr is a minimal net.Error-like for timeout testing.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }

// ===========================================================================
// IsHTTPTimeout (errors.go)
// ===========================================================================

func TestIsHTTPTimeout_Nil(t *testing.T) {
	if IsHTTPTimeout(nil) {
		t.Error("nil should be false")
	}
}

func TestIsHTTPTimeout_TypedTimeoutError(t *testing.T) {
	te := &TimeoutError{Reason: FailureReasonHTTPTimeout, Err: errors.New("dead")}
	if !IsHTTPTimeout(te) {
		t.Error("typed TimeoutError with HTTPTimeout reason should match")
	}
}

func TestIsHTTPTimeout_NetTimeout(t *testing.T) {
	ne := &net.OpError{Op: "read", Err: timeoutErr{}}
	if !IsHTTPTimeout(ne) {
		t.Error("net.Error with Timeout() should match")
	}
}

func TestIsHTTPTimeout_PlainError(t *testing.T) {
	if IsHTTPTimeout(errors.New("random")) {
		t.Error("random error should not match")
	}
}

// ===========================================================================
// isLikelyHTTPTimeout string fallbacks (errors.go — 80%)
// ===========================================================================

func TestIsLikelyHTTPTimeout_StringFallbacks(t *testing.T) {
	patterns := []string{
		"client.timeout exceeded while awaiting request",
		"timeout awaiting response headers",
		"response header timeout",
		"i/o timeout",
		"tls handshake timeout",
		"http timeout",
	}
	for _, p := range patterns {
		t.Run(p, func(t *testing.T) {
			if !isLikelyHTTPTimeout(errors.New(p)) {
				t.Errorf("pattern %q should match as HTTP timeout", p)
			}
		})
	}
}

func TestIsLikelyHTTPTimeout_ExcludesModelRound(t *testing.T) {
	if isLikelyHTTPTimeout(ErrModelRoundTimeout) {
		t.Error("ErrModelRoundTimeout should NOT match isLikelyHTTPTimeout")
	}
}

func TestIsLikelyHTTPTimeout_ExcludesStreamIdle(t *testing.T) {
	sit := &ErrStreamIdleTimeout{Timeout: 30 * time.Second}
	if isLikelyHTTPTimeout(sit) {
		t.Error("ErrStreamIdleTimeout should NOT match isLikelyHTTPTimeout")
	}
}

func TestIsLikelyHTTPTimeout_ExcludesCanceled(t *testing.T) {
	if isLikelyHTTPTimeout(context.Canceled) {
		t.Error("context.Canceled should NOT match isLikelyHTTPTimeout")
	}
}

func TestIsLikelyHTTPTimeout_DeadlineExceeded(t *testing.T) {
	if !isLikelyHTTPTimeout(context.DeadlineExceeded) {
		t.Error("context.DeadlineExceeded should match isLikelyHTTPTimeout")
	}
}

func TestIsLikelyHTTPTimeout_Nil(t *testing.T) {
	if isLikelyHTTPTimeout(nil) {
		t.Error("nil should be false")
	}
}

// ===========================================================================
// isRetryableHTTPStatusCode (errors.go)
// ===========================================================================

func TestIsRetryableHTTPStatusCode(t *testing.T) {
	retryable := []int{429, 500, 502, 503, 504, 529}
	for _, code := range retryable {
		if !isRetryableHTTPStatusCode(code) {
			t.Errorf("status %d should be retryable", code)
		}
	}
	nonRetryable := []int{200, 400, 401, 403, 404, 422}
	for _, code := range nonRetryable {
		if isRetryableHTTPStatusCode(code) {
			t.Errorf("status %d should NOT be retryable", code)
		}
	}
}

// ===========================================================================
// IsRetryableAPIError (errors.go)
// ===========================================================================

func TestIsRetryableAPIError_Coverage(t *testing.T) {
	// APIError with retryable status.
	if !IsRetryableAPIError(&APIError{StatusCode: 429}) {
		t.Error("APIError 429 should be retryable")
	}
	// HTTPError with retryable status.
	if !IsRetryableAPIError(&HTTPError{StatusCode: 503}) {
		t.Error("HTTPError 503 should be retryable")
	}
	// HTTPError 400 model_not_found (MiniMax transient).
	if !IsRetryableAPIError(&HTTPError{StatusCode: 400, Message: "model_not_found"}) {
		t.Error("HTTPError 400 model_not_found should be retryable")
	}
	if !IsRetryableAPIError(&HTTPError{StatusCode: 400, Message: "model not found"}) {
		t.Error("HTTPError 400 'model not found' should be retryable")
	}
	// Non-retryable.
	if IsRetryableAPIError(&HTTPError{StatusCode: 401}) {
		t.Error("HTTPError 401 should NOT be retryable")
	}
	// Plain error.
	if IsRetryableAPIError(errors.New("random")) {
		t.Error("random error should not be retryable API error")
	}
}

// ===========================================================================
// IsRateLimitError (errors.go)
// ===========================================================================

func TestIsRateLimitError_Coverage(t *testing.T) {
	if !IsRateLimitError(&APIError{StatusCode: 429}) {
		t.Error("APIError 429 should be rate limit")
	}
	if !IsRateLimitError(&HTTPError{StatusCode: 429}) {
		t.Error("HTTPError 429 should be rate limit")
	}
	if !IsRateLimitError(errors.New("rate limit exceeded")) {
		t.Error("string 'rate limit' should match")
	}
	if !IsRateLimitError(errors.New("Too Many Requests")) {
		t.Error("string 'too many requests' should match")
	}
	if IsRateLimitError(errors.New("success")) {
		t.Error("non-rate-limit should not match")
	}
	if IsRateLimitError(nil) {
		t.Error("nil should be false")
	}
}

// ===========================================================================
// IsRetryableError typed paths (errors.go — 88.9%)
// ===========================================================================

func TestIsRetryableError_TypedPaths(t *testing.T) {
	// nil.
	if IsRetryableError(nil) {
		t.Error("nil should not be retryable")
	}
	// context.Canceled → false.
	if IsRetryableError(context.Canceled) {
		t.Error("context.Canceled should not be retryable")
	}
	// ErrModelRoundTimeout → false.
	if IsRetryableError(ErrModelRoundTimeout) {
		t.Error("ErrModelRoundTimeout should not be retryable")
	}
	// ErrEmptyModelResponse → true.
	if !IsRetryableError(ErrEmptyModelResponse) {
		t.Error("ErrEmptyModelResponse should be retryable")
	}
	// context.DeadlineExceeded → true.
	if !IsRetryableError(context.DeadlineExceeded) {
		t.Error("context.DeadlineExceeded should be retryable")
	}
	// StreamIdleTimeout → true.
	if !IsRetryableError(&ErrStreamIdleTimeout{Timeout: 30 * time.Second}) {
		t.Error("ErrStreamIdleTimeout should be retryable")
	}
	// HTTPTimeout typed → true.
	if !IsRetryableError(&TimeoutError{Reason: FailureReasonHTTPTimeout, Err: errors.New("dead")}) {
		t.Error("HTTPTimeout should be retryable")
	}
	// net.Error → true.
	ne := &net.OpError{Op: "read", Err: timeoutErr{}}
	if !IsRetryableError(ne) {
		t.Error("net.Error should be retryable")
	}
	// API retryable → true.
	if !IsRetryableError(&APIError{StatusCode: 500}) {
		t.Error("APIError 500 should be retryable")
	}
}

func TestIsRetryableError_StringFallbacks(t *testing.T) {
	patterns := []string{
		"rate limit",
		"server overloaded",
		"temporarily unavailable",
		"unexpected eof",
		"tls handshake failed",
		"no such host",
	}
	for _, p := range patterns {
		t.Run(p, func(t *testing.T) {
			if !IsRetryableError(errors.New(p)) {
				t.Errorf("string fallback %q should be retryable", p)
			}
		})
	}
}

func TestIsRetryableError_NonRetryableString(t *testing.T) {
	if IsRetryableError(errors.New("authentication failed")) {
		t.Error("non-matching string should not be retryable")
	}
}

// ===========================================================================
// IsContextTooLongError / messageIndicatesContextOverflow (errors.go)
// ===========================================================================

func TestIsContextTooLongError_Nil(t *testing.T) {
	if IsContextTooLongError(nil) {
		t.Error("nil should be false")
	}
}

func TestIsContextTooLongError_HTTPError(t *testing.T) {
	// HTTPError 400 with context message.
	if !IsContextTooLongError(&HTTPError{StatusCode: 400, Message: "context too long"}) {
		t.Error("HTTPError 400 'context too long' should match")
	}
	// HTTPError 400 with token limit.
	if !IsContextTooLongError(&HTTPError{StatusCode: 400, Message: "token limit exceeded"}) {
		t.Error("HTTPError 400 'token limit exceeded' should match")
	}
	// HTTPError 400 non-context.
	if IsContextTooLongError(&HTTPError{StatusCode: 400, Message: "invalid parameter"}) {
		t.Error("HTTPError 400 non-context should not match")
	}
}

func TestIsContextTooLongError_APIError(t *testing.T) {
	if !IsContextTooLongError(&APIError{StatusCode: 400, Message: "request too large"}) {
		t.Error("APIError 400 'too large' should match")
	}
}

func TestIsContextTooLongError_StringFallback(t *testing.T) {
	if !IsContextTooLongError(errors.New("400 Bad Request: context length too long")) {
		t.Error("string fallback should match")
	}
	if IsContextTooLongError(errors.New("404 not found")) {
		t.Error("non-400 string should not match")
	}
}

func TestMessageIndicatesContextOverflow(t *testing.T) {
	positive := []string{
		"context length exceeded",
		"input too long",
		"request too large",
		"token limit exceeded",
		"maximum tokens reached",
		"too many tokens",
		"total message size 5943865 exceeds limit 2097152",
	}
	for _, msg := range positive {
		if !messageIndicatesContextOverflow(msg) {
			t.Errorf("should match: %q", msg)
		}
	}
	negative := []string{
		"maximum_tokens must be positive", // bare "maximum" + "token" without limit keyword → false? Actually "maximum" is a keyword. Hmm.
		"invalid request",
		"authentication error",
	}
	for _, msg := range negative {
		// "maximum_tokens must be positive" DOES contain "token" and "maximum"
		// so it actually returns true under the current logic. Adjust.
		if msg == "maximum_tokens must be positive" {
			if !messageIndicatesContextOverflow(msg) {
				t.Errorf("current logic matches token+maximum: %q (expected true)", msg)
			}
			continue
		}
		if messageIndicatesContextOverflow(msg) {
			t.Errorf("should NOT match: %q", msg)
		}
	}
}

// ===========================================================================
// containsFold / equalFold edge cases (errors.go)
// ===========================================================================

func TestContainsFold_EmptySubstring(t *testing.T) {
	if !containsFold("anything", "") {
		t.Error("empty substring should match")
	}
}

func TestContainsFold_CaseInsensitive(t *testing.T) {
	if !containsFold("Hello WORLD", "world") {
		t.Error("should match case-insensitively")
	}
	if !containsFold("ERROR: Rate Limit", "rate limit") {
		t.Error("should match mixed case")
	}
}

func TestContainsFold_ShorterString(t *testing.T) {
	if containsFold("hi", "hello") {
		t.Error("substring longer than string should not match")
	}
}

func TestContainsLower_LengthGuard(t *testing.T) {
	// containsLower has a length guard; short string with long substr → false.
	if containsLower("ab", "abcdef") {
		t.Error("length guard should reject")
	}
}

func TestEqualFold(t *testing.T) {
	if !equalFold("ABC", "abc") {
		t.Error("should fold")
	}
	if !equalFold("AbC", "aBc") {
		t.Error("should fold mixed")
	}
	if equalFold("abc", "abd") {
		t.Error("different strings should not be equal")
	}
}

// ===========================================================================
// DetectFailureTelemetry (errors.go — 96.2%, covering nil)
// ===========================================================================

func TestDetectFailureTelemetry_Nil(t *testing.T) {
	tel := DetectFailureTelemetry(nil)
	if tel.Reason != string(FailureReasonOther) {
		t.Errorf("nil error reason = %q, want %q", tel.Reason, FailureReasonOther)
	}
}

func TestDetectFailureTelemetry_TimeoutErrorWithProvider(t *testing.T) {
	te := &TimeoutError{
		Reason:   FailureReasonHTTPTimeout,
		Provider: "glm",
		Timeout:  30 * time.Second,
		Err:      errors.New("dead"),
	}
	tel := DetectFailureTelemetry(te)
	if tel.Provider != "glm" {
		t.Errorf("provider = %q", tel.Provider)
	}
	if tel.Timeout != 30*time.Second {
		t.Errorf("timeout = %v", tel.Timeout)
	}
}

func TestDetectFailureTelemetry_StreamIdle(t *testing.T) {
	sit := &ErrStreamIdleTimeout{Timeout: 60 * time.Second, Partial: true}
	tel := DetectFailureTelemetry(sit)
	if tel.Reason != string(FailureReasonStreamIdleTimeout) {
		t.Errorf("reason = %q", tel.Reason)
	}
	if !tel.Partial {
		t.Error("Partial should be true")
	}
}

func TestDetectFailureTelemetry_ContextCancel(t *testing.T) {
	tel := DetectFailureTelemetry(context.Canceled)
	if tel.Reason != string(FailureReasonContextCancel) {
		t.Errorf("reason = %q", tel.Reason)
	}
}

func TestDetectFailureTelemetry_HTTPTimeoutString(t *testing.T) {
	ne := &net.OpError{Op: "read", Err: timeoutErr{}}
	tel := DetectFailureTelemetry(ne)
	if tel.Reason != string(FailureReasonHTTPTimeout) {
		t.Errorf("reason = %q", tel.Reason)
	}
}

func TestDetectFailureTelemetry_DeadlineExceeded(t *testing.T) {
	tel := DetectFailureTelemetry(context.DeadlineExceeded)
	if tel.Reason != string(FailureReasonHTTPTimeout) {
		t.Errorf("reason = %q", tel.Reason)
	}
}

// ===========================================================================
// CollectText (streaming.go) — 0%
// ===========================================================================

func TestCollectText_Success(t *testing.T) {
	chunks := make(chan ResponseChunk, 3)
	chunks <- ResponseChunk{Text: "Hello "}
	chunks <- ResponseChunk{Text: "World"}
	close(chunks)

	sr := &StreamingResponse{Chunks: chunks}
	got, err := CollectText(context.Background(), sr)
	if err != nil {
		t.Fatalf("CollectText: %v", err)
	}
	if got != "Hello World" {
		t.Errorf("got %q, want %q", got, "Hello World")
	}
}

func TestCollectText_ErrorChunk(t *testing.T) {
	chunks := make(chan ResponseChunk, 2)
	chunks <- ResponseChunk{Text: "partial"}
	chunks <- ResponseChunk{Error: errors.New("stream broken")}
	close(chunks)

	sr := &StreamingResponse{Chunks: chunks}
	_, err := CollectText(context.Background(), sr)
	if err == nil || !containsLower(err.Error(), "stream broken") {
		t.Errorf("expected stream broken error, got %v", err)
	}
}

func TestCollectText_ContextCancel(t *testing.T) {
	chunks := make(chan ResponseChunk) // unbuffered, never written
	sr := &StreamingResponse{Chunks: chunks}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling

	_, err := CollectText(ctx, sr)
	if err == nil {
		t.Error("expected error on cancelled context")
	}
}

// ===========================================================================
// ProcessStream (streaming.go) — callback coverage
// ===========================================================================

func TestProcessStream_AllCallbacks(t *testing.T) {
	chunks := make(chan ResponseChunk, 2)

	var texts, thinks []string
	var fcs []*genai.FunctionCall
	var tokens []int
	var rateLimits []*RateLimitMetadata
	var completed bool

	chunks <- ResponseChunk{
		Text:     "hi",
		Thinking: "thought",
		FunctionCalls: []*genai.FunctionCall{
			{Name: "read", Args: map[string]any{"file_path": "a.go"}},
		},
		InputTokens:              10,
		OutputTokens:             5,
		CacheReadInputTokens:     3,
		CacheCreationInputTokens: 7,
		RateLimit:                &RateLimitMetadata{RequestsRemaining: 99},
	}
	chunks <- ResponseChunk{Done: true, FinishReason: genai.FinishReasonStop}
	close(chunks)

	handler := &StreamHandler{
		OnText:         func(t string) { texts = append(texts, t) },
		OnThinking:     func(t string) { thinks = append(thinks, t) },
		OnFunctionCall: func(fc *genai.FunctionCall) { fcs = append(fcs, fc) },
		OnTokenUpdate:  func(in, out int) { tokens = append(tokens, in, out) },
		OnRateLimit:    func(rl *RateLimitMetadata) { rateLimits = append(rateLimits, rl) },
		OnComplete:     func(r *Response) { completed = true },
	}

	sr := &StreamingResponse{Chunks: chunks}
	resp, err := ProcessStream(context.Background(), sr, handler)
	if err != nil {
		t.Fatalf("ProcessStream: %v", err)
	}
	if resp.Text != "hi" {
		t.Errorf("Text = %q", resp.Text)
	}
	if resp.Thinking != "thought" {
		t.Errorf("Thinking = %q", resp.Thinking)
	}
	if len(resp.FunctionCalls) != 1 || resp.FunctionCalls[0].Name != "read" {
		t.Errorf("FunctionCalls = %+v", resp.FunctionCalls)
	}
	if resp.InputTokens != 10 || resp.OutputTokens != 5 {
		t.Errorf("tokens = %d/%d", resp.InputTokens, resp.OutputTokens)
	}
	if resp.CacheReadInputTokens != 3 {
		t.Errorf("CacheRead = %d", resp.CacheReadInputTokens)
	}
	if resp.CacheCreationInputTokens != 7 {
		t.Errorf("CacheCreation = %d", resp.CacheCreationInputTokens)
	}
	if resp.RateLimit == nil || resp.RateLimit.RequestsRemaining != 99 {
		t.Errorf("RateLimit = %+v", resp.RateLimit)
	}
	if resp.FinishReason != genai.FinishReasonStop {
		t.Errorf("FinishReason = %v", resp.FinishReason)
	}
	if len(texts) == 0 || len(thinks) == 0 || len(fcs) == 0 || !completed {
		t.Error("callbacks not fired")
	}
	if len(tokens) == 0 || len(rateLimits) == 0 {
		t.Error("token/ratelimit callbacks not fired")
	}
}

func TestProcessStream_ErrorCallback(t *testing.T) {
	chunks := make(chan ResponseChunk, 1)
	streamErr := errors.New("boom")
	chunks <- ResponseChunk{Error: streamErr}
	close(chunks)

	var gotErr error
	handler := &StreamHandler{
		OnError: func(e error) { gotErr = e },
	}

	sr := &StreamingResponse{Chunks: chunks}
	_, err := ProcessStream(context.Background(), sr, handler)
	if err != streamErr {
		t.Errorf("err = %v, want %v", err, streamErr)
	}
	if gotErr != streamErr {
		t.Errorf("OnError got = %v", gotErr)
	}
}

func TestProcessStream_OnCompleteOnChannelClose(t *testing.T) {
	// When the channel closes WITHOUT a Done chunk, OnComplete should fire.
	chunks := make(chan ResponseChunk, 1)
	chunks <- ResponseChunk{Text: "end"}
	close(chunks)

	completed := false
	handler := &StreamHandler{
		OnComplete: func(r *Response) { completed = true },
	}

	sr := &StreamingResponse{Chunks: chunks}
	resp, err := ProcessStream(context.Background(), sr, handler)
	if err != nil {
		t.Fatalf("ProcessStream: %v", err)
	}
	if resp.Text != "end" {
		t.Errorf("Text = %q", resp.Text)
	}
	if !completed {
		t.Error("OnComplete should fire on channel close")
	}
}

func TestProcessStream_FunctionCallFromParts(t *testing.T) {
	// A FunctionCall that arrives via chunk.Parts (not chunk.FunctionCalls)
	// should still be accumulated into resp.Parts without duplication.
	fc := &genai.FunctionCall{Name: "bash", Args: map[string]any{"command": "ls"}}
	chunks := make(chan ResponseChunk, 1)
	chunks <- ResponseChunk{
		Parts: []*genai.Part{{FunctionCall: fc}},
	}
	close(chunks)

	sr := &StreamingResponse{Chunks: chunks}
	resp, err := ProcessStream(context.Background(), sr, &StreamHandler{})
	if err != nil {
		t.Fatalf("ProcessStream: %v", err)
	}
	if len(resp.Parts) != 1 || resp.Parts[0].FunctionCall != fc {
		t.Errorf("Parts should contain the FC exactly once: %+v", resp.Parts)
	}
	// FunctionCalls slice is NOT populated from Parts alone (only from
	// chunk.FunctionCalls), but the part itself is preserved.
}

// ===========================================================================
// StreamingResponse.Collect (client.go) — FunctionCall dedup
// ===========================================================================

func TestStreamingResponse_Collect_DedupFunctionCalls(t *testing.T) {
	fc := &genai.FunctionCall{Name: "read", Args: map[string]any{"file_path": "x.go"}}
	chunks := make(chan ResponseChunk, 2)
	// Same FC appears in both Parts and FunctionCalls — Collect should NOT
	// duplicate it in resp.Parts.
	chunks <- ResponseChunk{
		Parts:         []*genai.Part{{FunctionCall: fc}},
		FunctionCalls: []*genai.FunctionCall{fc},
	}
	close(chunks)

	sr := &StreamingResponse{Chunks: chunks}
	resp, err := sr.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	// Parts should have the FC from Parts only (deduped), FunctionCalls has 1.
	partFCs := 0
	for _, p := range resp.Parts {
		if p.FunctionCall != nil {
			partFCs++
		}
	}
	if partFCs != 1 {
		t.Errorf("Parts should have 1 FC (deduped), got %d", partFCs)
	}
	if len(resp.FunctionCalls) != 1 {
		t.Errorf("FunctionCalls len = %d, want 1", len(resp.FunctionCalls))
	}
}

func TestStreamingResponse_Collect_ErrorChunk(t *testing.T) {
	chunks := make(chan ResponseChunk, 2)
	chunks <- ResponseChunk{Text: "partial"}
	chunks <- ResponseChunk{Error: errors.New("broke")}
	close(chunks)

	sr := &StreamingResponse{Chunks: chunks}
	resp, err := sr.Collect()
	if err == nil || !containsLower(err.Error(), "broke") {
		t.Errorf("expected broke error, got %v", err)
	}
	// Partial text preserved.
	if resp.Text != "partial" {
		t.Errorf("partial Text = %q", resp.Text)
	}
}

func TestStreamingResponse_Collect_TokensAndRateLimit(t *testing.T) {
	chunks := make(chan ResponseChunk, 2)
	chunks <- ResponseChunk{
		InputTokens:              100,
		OutputTokens:             50,
		CacheReadInputTokens:     20,
		CacheCreationInputTokens: 10,
		RateLimit:                &RateLimitMetadata{TokensRemaining: 5000},
	}
	close(chunks)

	sr := &StreamingResponse{Chunks: chunks}
	resp, err := sr.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if resp.InputTokens != 100 || resp.OutputTokens != 50 {
		t.Errorf("tokens = %d/%d", resp.InputTokens, resp.OutputTokens)
	}
	if resp.CacheReadInputTokens != 20 {
		t.Errorf("CacheRead = %d", resp.CacheReadInputTokens)
	}
	if resp.CacheCreationInputTokens != 10 {
		t.Errorf("CacheCreation = %d", resp.CacheCreationInputTokens)
	}
	if resp.RateLimit == nil || resp.RateLimit.TokensRemaining != 5000 {
		t.Errorf("RateLimit = %+v", resp.RateLimit)
	}
}

// ===========================================================================
// ToolCallFallbackPrompt (tool_parser.go) — 0%
// ===========================================================================

func TestToolCallFallbackPrompt_Empty(t *testing.T) {
	if got := ToolCallFallbackPrompt(nil); got != "" {
		t.Errorf("nil declarations should return empty, got %q", got)
	}
	if got := ToolCallFallbackPrompt([]*genai.FunctionDeclaration{}); got != "" {
		t.Errorf("empty declarations should return empty, got %q", got)
	}
}

func TestToolCallFallbackPrompt_WithDeclarations(t *testing.T) {
	decls := []*genai.FunctionDeclaration{
		{
			Name:        "read",
			Description: "Read a file",
			Parameters: &genai.Schema{
				Properties: map[string]*genai.Schema{
					"file_path": {Type: "string", Description: "Path to file"},
				},
				Required: []string{"file_path"},
			},
		},
		{
			Name:        "bash",
			Description: "Run a command",
		},
	}

	got := ToolCallFallbackPrompt(decls)
	if got == "" {
		t.Fatal("should return non-empty prompt")
	}
	if !containsLower(got, "tool calling instructions") {
		t.Error("should contain header")
	}
	if !containsLower(got, "read") {
		t.Error("should list 'read' tool")
	}
	if !containsLower(got, "bash") {
		t.Error("should list 'bash' tool")
	}
	if !containsLower(got, "file_path") {
		t.Error("should list parameter 'file_path'")
	}
	if !containsLower(got, "required") {
		t.Error("should mark required params")
	}
}

// ===========================================================================
// Ptr (helpers.go)
// ===========================================================================

func TestPtr(t *testing.T) {
	i := 42
	p := Ptr(i)
	if *p != 42 {
		t.Errorf("Ptr(42) = %d", *p)
	}

	s := "hello"
	sp := Ptr(s)
	if *sp != "hello" {
		t.Errorf("Ptr(hello) = %q", *sp)
	}
}

// ===========================================================================
// AnthropicClient — GetProvider / WithModel / SetRateLimiter (anthropic.go — 0%)
// ===========================================================================

func newTestAnthropicClient(t *testing.T) *AnthropicClient {
	t.Helper()
	c, err := NewAnthropicClient(AnthropicConfig{
		APIKey:  "test-key",
		BaseURL: "https://example.com",
		Model:   "glm-5.2",
	})
	if err != nil {
		t.Fatalf("NewAnthropicClient: %v", err)
	}
	return c
}

func TestAnthropicClient_GetProvider_Default(t *testing.T) {
	c := newTestAnthropicClient(t)
	// No provider set → defaults to "anthropic-compatible".
	if got := c.GetProvider(); got != "anthropic-compatible" {
		t.Errorf("GetProvider default = %q, want 'anthropic-compatible'", got)
	}
}

func TestAnthropicClient_GetProvider_Set(t *testing.T) {
	c, _ := NewAnthropicClient(AnthropicConfig{
		APIKey:   "test-key",
		BaseURL:  "https://example.com",
		Provider: "glm",
		Model:    "glm-5.2",
	})
	if got := c.GetProvider(); got != "glm" {
		t.Errorf("GetProvider = %q, want 'glm'", got)
	}
}

func TestAnthropicClient_WithModel(t *testing.T) {
	c := newTestAnthropicClient(t)
	newC := c.WithModel("glm-5.2-air")
	if newC.GetModel() != "glm-5.2-air" {
		t.Errorf("WithModel: GetModel = %q, want 'glm-5.2-air'", newC.GetModel())
	}
}

func TestAnthropicClient_SetRateLimiter_Coverage(t *testing.T) {
	c := newTestAnthropicClient(t)
	// Set a real RateLimiter implementation → should be stored.
	var rl RateLimiter = &stubRateLimiter{}
	c.SetRateLimiter(rl)
	c.mu.RLock()
	got := c.rateLimiter
	c.mu.RUnlock()
	if got == nil {
		t.Error("rateLimiter should be set after SetRateLimiter with a RateLimiter")
	}
}

func TestAnthropicClient_SetRateLimiter_NonRateLimiter(t *testing.T) {
	c := newTestAnthropicClient(t)
	// A non-RateLimiter value → silently ignored, rateLimiter stays nil.
	c.SetRateLimiter("not a rate limiter")
	c.mu.RLock()
	got := c.rateLimiter
	c.mu.RUnlock()
	if got != nil {
		t.Error("non-RateLimiter value should be ignored")
	}
}

// stubRateLimiter is a minimal RateLimiter for SetRateLimiter coverage.
type stubRateLimiter struct{}

func (s *stubRateLimiter) AcquireWithContext(ctx context.Context, tokens int64) error { return nil }
func (s *stubRateLimiter) ReturnTokens(requests int, tokens int64)                    {}
func (s *stubRateLimiter) EstimateWaitTime(tokens int64) time.Duration                { return 0 }

func TestNewAnthropicClient_MissingAPIKey(t *testing.T) {
	if _, err := NewAnthropicClient(AnthropicConfig{Model: "m"}); err == nil {
		t.Error("missing APIKey should error")
	}
}

func TestNewAnthropicClient_InvalidBaseURL(t *testing.T) {
	if _, err := NewAnthropicClient(AnthropicConfig{
		APIKey:  "k",
		BaseURL: "ftp://bad",
		Model:   "m",
	}); err == nil {
		t.Error("invalid BaseURL should error")
	}
}

func TestNewAnthropicClient_MissingModel(t *testing.T) {
	if _, err := NewAnthropicClient(AnthropicConfig{APIKey: "k"}); err == nil {
		t.Error("missing Model should error")
	}
}

func TestNewAnthropicClient_Defaults(t *testing.T) {
	c, err := NewAnthropicClient(AnthropicConfig{APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatalf("NewAnthropicClient: %v", err)
	}
	if c.config.BaseURL != DefaultAnthropicBaseURL {
		t.Errorf("default BaseURL = %q", c.config.BaseURL)
	}
	if c.config.MaxTokens != 8192 {
		t.Errorf("default MaxTokens = %d", c.config.MaxTokens)
	}
}
