package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

// The GLM 5-hour usage cap (code 1308) is delivered as HTTP 429 with error
// type "rate_limit_error". Its raw body therefore matches IsOverloadError's
// "rate_limit" keyword, so before this fix the HTTP-status error path handed it
// straight to the 10-minute patient-overload retry — the app looked frozen for
// 10 minutes on a hard quota cap that retrying can never fix. The fix classifies
// terminal GLM codes at the source (doStreamRequest) into a TerminalProviderError
// that every retry decider rejects, so it surfaces immediately with an actionable
// message. This is the end-to-end proof against the real request path.
func TestGLM1308_HTTP429_SurfacesAsTerminalNotRetried(t *testing.T) {
	const body = `{"type":"error","error":{"type":"rate_limit_error","code":"1308","message":"[1308][Usage limit reached for 5 hour. Your limit will reset at 2026-07-03 19:43:33][20260703]"},"request_id":"20260703"}`

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests) // 429
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := &AnthropicClient{
		config:     AnthropicConfig{Model: "glm-5.2", BaseURL: srv.URL, APIKey: "test", Provider: "glm", StreamIdleTimeout: 5 * time.Second, MaxRetries: 3},
		httpClient: &http.Client{},
	}

	_, err := c.SendMessageWithHistory(context.Background(),
		[]*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, "")
	if err == nil {
		t.Fatal("expected a terminal error, got nil")
	}

	if !IsTerminalProviderError(err) {
		t.Errorf("IsTerminalProviderError(%q) = false, want true", err.Error())
	}
	if IsOverloadError(err) {
		t.Errorf("IsOverloadError(%q) = true, want false — a 5-hour quota cap must NOT be parked on the patient overload budget", err.Error())
	}
	if IsRetryableError(err) {
		t.Errorf("IsRetryableError(%q) = true, want false — retrying a quota cap can't help", err.Error())
	}
	// It must NOT have burned the whole retry budget: a terminal error stops
	// after the FIRST attempt (the freeze was ~15 attempts over 10 minutes).
	if hits != 1 {
		t.Errorf("server was hit %d times, want exactly 1 (terminal error must not retry)", hits)
	}
	// Actionable + informative: switch-provider hint AND the reset time.
	if !strings.Contains(err.Error(), "/provider") {
		t.Errorf("message %q should tell the user how to recover (switch provider)", err.Error())
	}
	if !strings.Contains(err.Error(), "reset at 2026-07-03 19:43:33") {
		t.Errorf("message %q should surface the provider's reset time", err.Error())
	}
}

// A TerminalProviderError is rejected by every retry decider regardless of the
// HTTP status it originally carried (1308 arrives with a normally-retryable 429).
func TestTerminalProviderError_RejectedByAllRetryDeciders(t *testing.T) {
	term := &TerminalProviderError{Code: "1308", Status: 429, Message: "GLM quota/balance exhausted — switch provider with /provider"}

	if !IsTerminalProviderError(term) {
		t.Fatal("IsTerminalProviderError should be true for a *TerminalProviderError")
	}
	if IsOverloadError(term) {
		t.Error("IsOverloadError must be false for a terminal error")
	}
	if IsRetryableError(term) {
		t.Error("IsRetryableError must be false for a terminal error")
	}
	if IsTransientProviderError(term) {
		t.Error("IsTransientProviderError must be false for a terminal error (a /loop must not treat a quota cap as a transient blip)")
	}
	// Even with a normally-retryable status (429), the client method rejects it.
	c := &AnthropicClient{config: AnthropicConfig{Provider: "glm"}}
	if c.isRetryableError(term, 429) {
		t.Error("isRetryableError(term, 429) must be false — the terminal classification overrides the retryable status code")
	}
	if IsTerminalProviderError(nil) {
		t.Error("IsTerminalProviderError(nil) must be false")
	}
}

// The underlying trap the fix routes around: the RAW 1308 body string still
// matches IsOverloadError (via "rate_limit"), which is exactly why the terminal
// classification at the source — not keyword matching — is required.
func TestIsGLMTerminalCode_AndTheOverloadTrap(t *testing.T) {
	rawBody := `{"error":{"type":"rate_limit_error","code":"1308","message":"Usage limit reached"}}`
	if !IsOverloadError(errString(rawBody)) {
		t.Fatal("precondition: the raw 1308 body DOES match IsOverloadError via rate_limit — that is the trap the fix avoids by classifying at the source")
	}

	terminal := []string{
		"1000", "1001", "1003", "1005", "1113",
		"1210", "1211", "1212", "1213", "1214", "1215", "1220", "1221", "1222",
		"1301", "1308", "1309", "1310", "1311", "1313", "1314", "1315",
		"1316", "1317", "1318", "1319", "1320", "1321",
	}
	for _, code := range terminal {
		if !isGLMTerminalCode(code) {
			t.Errorf("isGLMTerminalCode(%q) = false, want true", code)
		}
	}
	// Retryable / transient / unknown codes must NOT be terminal.
	for _, code := range []string{"-500", "1200", "1230", "1234", "1261", "1302", "1305", "1303", "1312", "9999", ""} {
		if isGLMTerminalCode(code) {
			t.Errorf("isGLMTerminalCode(%q) = true, want false (retryable/unknown must stay on the normal retry path)", code)
		}
	}
}

// A standard Anthropic-compatible 429 body (deepseek/kimi/minimax) carries a
// "type", not a numeric "code" — so it must NOT be classified as a GLM terminal
// error, preserving those providers' transient rate-limit retry behavior.
func TestParseProviderErrorBody_StandardShapeIsNotTerminal(t *testing.T) {
	// GLM shape → code extracted.
	code, msg := parseProviderErrorBody([]byte(`{"error":{"code":"1308","message":"cap"}}`))
	if code != "1308" || msg != "cap" {
		t.Fatalf("parseProviderErrorBody(glm) = (%q,%q), want (1308,cap)", code, msg)
	}
	// Official docs are inconsistent across endpoints: Error Shapes uses a
	// string, while the Chat Completion default error schema renders a number.
	code, msg = parseProviderErrorBody([]byte(`{"error":{"code":1316,"message":"cap"}}`))
	if code != "1316" || msg != "cap" {
		t.Fatalf("parseProviderErrorBody(numeric code) = (%q,%q), want (1316,cap)", code, msg)
	}
	if code = providerErrorCodeFromMap(map[string]any{"code": json.Number("1234")}); code != "1234" {
		t.Errorf("providerErrorCodeFromMap(json.Number) = %q, want 1234", code)
	}
	if code = providerErrorCodeFromMap(map[string]any{"code": 12.5}); code != "" {
		t.Errorf("providerErrorCodeFromMap(non-integer) = %q, want empty", code)
	}
	// Standard Anthropic shape (deepseek) → no numeric code → not terminal.
	code, _ = parseProviderErrorBody([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))
	if code != "" {
		t.Errorf("standard shape yielded code %q, want empty (must fall through to retryable/overload for deepseek/kimi/minimax)", code)
	}
	if isGLMTerminalCode(code) {
		t.Error("standard-shape empty code must not be terminal")
	}
	// Non-JSON body → empty, no panic.
	if code, _ = parseProviderErrorBody([]byte("not json")); code != "" {
		t.Errorf("non-JSON body yielded code %q, want empty", code)
	}
}

func TestTerminalProviderMessage_IncludesResetTime(t *testing.T) {
	desc := "GLM quota/balance exhausted — top up your GLM plan or switch provider with /provider"
	glmMsg := "[1308][Usage limit reached for 5 hour. Your limit Resets at 2026-07-03 19:43:33][20260703]"
	got := terminalProviderMessage(desc, glmMsg)
	if !strings.Contains(got, "/provider") {
		t.Errorf("message %q should keep the actionable description", got)
	}
	if !strings.Contains(got, "Resets at 2026-07-03 19:43:33") {
		t.Errorf("message %q should append the reset time", got)
	}
	// No reset clause → just the description, no trailing parens.
	if got := terminalProviderMessage(desc, "no timing info"); got != desc {
		t.Errorf("message without a reset clause = %q, want the bare description", got)
	}
	if got := terminalProviderMessage(desc, ""); got != desc {
		t.Errorf("message with empty provider text = %q, want the bare description", got)
	}
}

// errString is a tiny error wrapper for asserting keyword-based deciders on a
// raw provider body without constructing a full request.
type errStringT string

func (e errStringT) Error() string { return string(e) }
func errString(s string) error     { return errStringT(s) }
