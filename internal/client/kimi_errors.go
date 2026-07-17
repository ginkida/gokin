package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// newKimiHTTPError gives Kimi Code's documented account, quota, overload,
// request, and context failures the same typed semantics GLM business codes
// receive. Kimi often uses HTTP 429 for both a transient engine overload and a
// hard 5-hour/monthly quota exhaustion; status-only classification cannot tell
// them apart and used to park exhausted accounts in the patient overload loop.
func newKimiHTTPError(status int, retryAfter time.Duration, body []byte) error {
	errType, providerMsg := parseAnthropicErrorBody(body)
	if providerMsg == "" {
		providerMsg = strings.TrimSpace(string(body))
	}
	return newKimiProviderError(status, retryAfter, errType, providerMsg)
}

// newKimiProviderError classifies both non-200 HTTP bodies and HTTP-200 SSE
// error events. A zero status means the caller only has the Anthropic error
// type; infer the corresponding status so the ordinary retry/context helpers
// continue to work without Kimi-specific branches elsewhere.
func newKimiProviderError(status int, retryAfter time.Duration, errType, providerMsg string) error {
	if status == 0 {
		status = kimiStatusFromErrorType(errType)
	}
	lower := strings.ToLower(providerMsg)

	terminal := func(code, message string) error {
		return &TerminalProviderError{
			Code:    code,
			Status:  status,
			Message: terminalProviderMessage(message, providerMsg),
		}
	}

	// Kimi Code's 429 is overloaded: it represents both recoverable capacity
	// pressure and non-recoverable subscription quota exhaustion. Match the
	// official quota phrases before returning the generic retryable 429.
	if (status == http.StatusTooManyRequests || status == http.StatusForbidden) && kimiQuotaExhausted(lower) {
		return terminal("kimi_quota_exhausted",
			"Kimi quota exhausted — wait for the usage window to reset, upgrade the plan, or switch provider with /provider")
	}

	switch {
	case strings.Contains(lower, "does not have access to k3"):
		return terminal("kimi_k3_access_denied",
			"Kimi plan does not include K3 — upgrade to Moderato or above, or switch to /model kimi-for-coding")
	case strings.Contains(lower, "supports only kimi-k3 up to 256k") ||
		strings.Contains(lower, "1m context is available on higher-tier"):
		return terminal("kimi_k3_1m_access_denied",
			"Kimi plan supports K3 up to 256K context — set context.max_input_tokens to 262144, upgrade to Allegretto or above, or switch to /model kimi-for-coding")
	case strings.Contains(lower, "does not have access to kimi-for-coding-highspeed"):
		return terminal("kimi_highspeed_access_denied",
			"Kimi plan does not include HighSpeed — upgrade to Allegretto or above, or switch to /model kimi-for-coding")
	case status == http.StatusUnauthorized:
		return terminal("kimi_authentication_failed",
			"Kimi authentication or plan access failed — verify the Kimi Code key, subscription, model, and endpoint")
	case status == http.StatusForbidden:
		return terminal("kimi_access_denied",
			"Kimi access or billing quota is unavailable — check the subscription, wait for reset, or switch provider with /provider")
	case status == http.StatusNotFound:
		return terminal("kimi_model_not_found",
			"Kimi model or endpoint is unavailable — choose a supported Kimi model and verify the Kimi Code endpoint")
	case status == http.StatusPaymentRequired:
		// Kimi documents 402 as a temporary membership-verification outage.
		// Preserve 402 for diagnostics, but include the stable retry keyword so
		// both the request and background-task retry classifiers wait it out.
		return &HTTPError{
			StatusCode: status,
			RetryAfter: retryAfter,
			Message:    kimiErrorMessage("Kimi membership verification is temporarily unavailable — retrying", errType, providerMsg),
		}
	case status == http.StatusBadRequest && messageIndicatesContextOverflow(lower):
		// Keep context overflow as HTTP 400: the app recognizes this typed error
		// and compacts history before retrying with a smaller prompt.
		return &HTTPError{
			StatusCode: status,
			RetryAfter: retryAfter,
			Message:    kimiErrorMessage("Kimi context limit exceeded", errType, providerMsg),
		}
	case strings.Contains(lower, "reasoning_content is missing"):
		return terminal("kimi_reasoning_replay_missing",
			"Kimi rejected incomplete preserved thinking — start a new session; if this repeats, report the reasoning replay failure")
	case status == http.StatusBadRequest:
		return terminal("kimi_invalid_request",
			"Kimi rejected the request as invalid — check the model, tool declarations, and request content")
	}

	return &HTTPError{
		StatusCode: status,
		RetryAfter: retryAfter,
		Message:    kimiErrorMessage(fmt.Sprintf("Kimi API error (status %d)", status), errType, providerMsg),
	}
}

func kimiQuotaExhausted(message string) bool {
	return strings.Contains(message, "usage limit for this billing cycle") ||
		strings.Contains(message, "usage limit for this period") ||
		strings.Contains(message, "monthly usage limit") ||
		strings.Contains(message, "quota exhausted") ||
		strings.Contains(message, "quota has been fully")
}

func kimiStatusFromErrorType(errType string) int {
	switch strings.ToLower(strings.TrimSpace(errType)) {
	case "authentication_error":
		return http.StatusUnauthorized
	case "permission_error":
		return http.StatusForbidden
	case "invalid_request_error":
		return http.StatusBadRequest
	case "rate_limit_error":
		return http.StatusTooManyRequests
	case "overloaded_error":
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func kimiErrorMessage(prefix, errType, providerMsg string) string {
	details := strings.TrimSpace(providerMsg)
	if details == "" {
		details = strings.TrimSpace(errType)
	}
	if details == "" {
		return prefix
	}
	return prefix + ": " + details
}

// parseAnthropicErrorBody extracts the standard compatible error shape used by
// Kimi (`{"error":{"type":"...","message":"..."}}`). It deliberately
// stays separate from parseProviderErrorBody, whose numeric-code semantics are
// specific to Z.AI/GLM.
func parseAnthropicErrorBody(body []byte) (errType, message string) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", ""
	}
	errObj, ok := payload["error"].(map[string]any)
	if !ok {
		return "", ""
	}
	return stringFromMap(errObj, "type"), stringFromMap(errObj, "message")
}
