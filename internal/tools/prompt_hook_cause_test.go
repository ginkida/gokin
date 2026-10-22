package tools

import (
	"encoding/json"
	"errors"
	"testing"

	"gokin/internal/client"
	"gokin/internal/hooks"
	"google.golang.org/genai"
)

func TestPromptHookFailureCauseSurvivesDurableToolResult(t *testing.T) {
	for _, source := range []error{ErrMaxTurnsExceeded, ErrBudgetExceeded, ErrCostUnavailable, &client.TerminalProviderError{Code: "1113", Status: 400, Message: "insufficient balance"}} {
		result, ok := HookStopResult([]hooks.Result{{StopReason: "required model check failed", Error: source}})
		if !ok {
			t.Fatal("missing hook stop")
		}
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var restored ToolResult
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		cause := HookStopError([]*genai.FunctionResponse{{Response: restored.ToMap()}})
		if !IsHookStopError(cause) {
			t.Fatalf("lost hook origin: %v", cause)
		}
		var terminal *client.TerminalProviderError
		if errors.As(source, &terminal) {
			var actual *client.TerminalProviderError
			if !errors.As(cause, &actual) || actual.Code != terminal.Code || actual.Status != terminal.Status {
				t.Fatalf("lost typed provider cause: %v", cause)
			}
		} else if !errors.Is(cause, source) {
			t.Fatalf("lost hard limit cause: %v", cause)
		}
	}
}
