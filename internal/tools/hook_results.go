package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gokin/internal/client"
	"gokin/internal/hooks"
	"google.golang.org/genai"
)

// ApplyHookInput merges the documented updatedInput fields. The caller must
// validate and authorize the effective input after this operation.
func ApplyHookInput(call *genai.FunctionCall, results []hooks.Result) string {
	decision := ""
	for _, result := range results {
		if result.PermissionDecision == "ask" || decision == "" {
			decision = result.PermissionDecision
		}
		if result.UpdatedInput != nil {
			input := make(map[string]any, len(call.Args)+len(result.UpdatedInput))
			for name, value := range call.Args {
				input[name] = value
			}
			for name, value := range result.UpdatedInput {
				input[name] = value
			}
			call.Args = input
		}
	}
	return decision
}

func HookStopResult(results []hooks.Result) (ToolResult, bool) {
	for _, result := range results {
		if result.StopReason != "" {
			stopped := NewHookStoppedResult(result.StopReason)
			stopped.HookStopKind = hookFailureKind(result.Error)
			return stopped, true
		}
	}
	return ToolResult{}, false
}

func AppendHookContext(result ToolResult, hookResults []hooks.Result) ToolResult {
	if context := strings.TrimSpace(hooks.AdditionalContext(hookResults)); context != "" {
		result.Content += "\n\n[Hook context]\n" + context
	}
	for _, hookResult := range hookResults {
		if hookResult.StopReason != "" {
			result.HookStopReason = hookResult.StopReason
			result.HookStopKind = hookFailureKind(hookResult.Error)
		}
	}
	return result
}

func HookStopError(responses []*genai.FunctionResponse) error {
	for _, response := range responses {
		if response == nil {
			continue
		}
		if reason, _ := response.Response["hook_stop_reason"].(string); reason != "" {
			kind, _ := response.Response["hook_stop_kind"].(string)
			return HookStoppedError(ToolResult{HookStopReason: reason, HookStopKind: kind})
		}
	}
	return nil
}

func hookFailureKind(err error) string {
	switch {
	case errors.Is(err, ErrMaxTurnsExceeded):
		return "max_turns"
	case errors.Is(err, ErrBudgetExceeded):
		return "budget_exceeded"
	case errors.Is(err, ErrCostUnavailable):
		return "cost_unavailable"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, client.ErrModelRoundTimeout):
		return "timeout"
	}
	var terminal *client.TerminalProviderError
	if errors.As(err, &terminal) && len(terminal.Code) <= 128 {
		data, _ := json.Marshal(struct {
			Code   string `json:"code"`
			Status int    `json:"status"`
		}{terminal.Code, terminal.Status})
		return "provider_terminal:" + string(data)
	}
	var httpErr *client.HTTPError
	var apiErr *client.APIError
	if errors.As(err, &httpErr) {
		return "provider_http:" + strconv.Itoa(httpErr.StatusCode)
	}
	if errors.As(err, &apiErr) {
		return "provider_http:" + strconv.Itoa(apiErr.StatusCode)
	}
	return ""
}

func HookStoppedError(result ToolResult) error {
	stopped := &HookExecutionStoppedError{Reason: result.HookStopReason}
	var cause error
	switch result.HookStopKind {
	case "max_turns":
		cause = ErrMaxTurnsExceeded
	case "budget_exceeded":
		cause = ErrBudgetExceeded
	case "cost_unavailable":
		cause = ErrCostUnavailable
	case "timeout":
		cause = context.DeadlineExceeded
	}
	if raw, ok := strings.CutPrefix(result.HookStopKind, "provider_terminal:"); ok {
		var info struct {
			Code   string `json:"code"`
			Status int    `json:"status"`
		}
		if len(raw) <= 256 && json.Unmarshal([]byte(raw), &info) == nil {
			cause = &client.TerminalProviderError{Code: info.Code, Status: info.Status, Message: result.HookStopReason}
		}
	}
	if raw, ok := strings.CutPrefix(result.HookStopKind, "provider_http:"); ok {
		if status, err := strconv.Atoi(raw); err == nil && status >= 100 && status <= 599 {
			cause = &client.HTTPError{StatusCode: status, Message: result.HookStopReason}
		}
	}
	if cause != nil {
		return errors.Join(stopped, cause)
	}
	return stopped
}

// HookExecutionStoppedError is an intentional terminal control signal. Text
// in its reason (including rate-limit/timeout words) must not schedule retry.
type HookExecutionStoppedError struct{ Reason string }

func (e *HookExecutionStoppedError) Error() string {
	return fmt.Sprintf("execution stopped by hook: %s", e.Reason)
}

func (*HookExecutionStoppedError) AutomaticRetrySafe() bool { return false }

func IsHookStopError(err error) bool {
	var stopped *HookExecutionStoppedError
	return errors.As(err, &stopped)
}

func NewHookStoppedResult(reason string) ToolResult {
	result := NewErrorResult(reason)
	result.HookStopReason = reason
	return result
}

// HookStopErrorWithContext preserves a simultaneous caller cancellation while
// retaining hook provenance for pair-complete persistence and retry refusal.
func HookStopErrorWithContext(ctx context.Context, responses []*genai.FunctionResponse) error {
	err := HookStopError(responses)
	if err != nil && ctx.Err() != nil {
		return errors.Join(client.ContextErr(ctx), err)
	}
	return err
}
