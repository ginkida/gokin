package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DeepSeek's HTTP and SSE errors share one taxonomy. In particular, balance
// failures must not enter the patient overload loop even if their text mentions
// rate limits. See api-docs.deepseek.com/quick_start/error_codes/.
func newDeepSeekProviderError(status int, retryAfter time.Duration, errType, message string) error {
	lower := strings.ToLower(errType + " " + message)
	if status == 0 {
		switch strings.ToLower(errType) {
		case "authentication_error":
			status = http.StatusUnauthorized
		case "permission_error":
			status = http.StatusForbidden
		case "insufficient_balance", "insufficient_balance_error":
			status = http.StatusPaymentRequired
		case "invalid_request_error":
			status = http.StatusBadRequest
		case "rate_limit_error":
			status = http.StatusTooManyRequests
		case "overloaded_error":
			status = http.StatusServiceUnavailable
		default:
			status = http.StatusInternalServerError
		}
	}
	terminal := func(code, hint string) error {
		return &TerminalProviderError{Code: code, Status: status, Message: terminalProviderMessage(hint, message)}
	}
	if status == http.StatusPaymentRequired || strings.Contains(lower, "insufficient balance") || strings.Contains(lower, "insufficient_balance") {
		return terminal("deepseek_insufficient_balance", "DeepSeek balance exhausted — top up at platform.deepseek.com or switch provider with /provider")
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return terminal("deepseek_authentication_failed", "DeepSeek authentication failed — verify the API key with /login deepseek")
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		if !messageIndicatesContextOverflow(strings.ToLower(message)) {
			return terminal("deepseek_invalid_request", "DeepSeek rejected the request — check the model and request parameters")
		}
	}
	return &HTTPError{StatusCode: status, RetryAfter: retryAfter, Message: fmt.Sprintf("DeepSeek API error (%s, status %d): %s", errType, status, message)}
}

func newDeepSeekHTTPError(status int, retryAfter time.Duration, body []byte) error {
	var payload struct {
		Error struct {
			Type    string          `json:"type"`
			Message string          `json:"message"`
			Code    json.RawMessage `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	message := payload.Error.Message
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	errType := payload.Error.Type
	if errType == "" && len(payload.Error.Code) > 0 {
		errType = string(payload.Error.Code)
	}
	return newDeepSeekProviderError(status, retryAfter, errType, message)
}
