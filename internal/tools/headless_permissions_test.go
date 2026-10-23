package tools

import (
	"context"
	"google.golang.org/genai"
	"strings"
	"testing"
)

func TestHeadlessPermissionResultsKeepTerminalBoundaries(t *testing.T) {
	call := &genai.FunctionCall{ID: "call-1", Name: "bash", Args: map[string]any{"command": "secret"}}
	for _, test := range []struct {
		name        string
		strict      bool
		result      ToolResult
		recoverable bool
	}{
		{"permission", false, NewPolicyBlockedResult(PolicyBlockPermission, "secret"), true},
		{"plan", false, NewPolicyBlockedResult(PolicyBlockPlan, "secret"), true},
		{"strict", true, NewPolicyBlockedResult(PolicyBlockPermission, "secret"), false},
		{"ceiling", false, newTerminalPermissionResult("secret"), false},
		{"hook", false, NewPolicyBlockedResult(PolicyBlockHook, "secret"), false},
		{"safety", false, NewPolicyBlockedResult(PolicyBlockSafety, "secret"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, state := ContextWithHeadlessPermissions(t.Context(), test.strict, "dontAsk")
			got := ApplyHeadlessPermissionResult(ctx, call, test.result)
			if (got.PolicyBlock != nil && got.PolicyBlock.Recoverable) != test.recoverable {
				t.Fatalf("result=%+v", got)
			}
			if test.recoverable || test.strict {
				denial := state.Denials()
				if len(denial) != 1 || denial[0].ToolUseID != "call-1" || strings.Contains(denial[0].Reason, "secret") || !strings.Contains(got.Error, "Do not retry") {
					t.Fatalf("denial=%+v result=%+v", denial, got)
				}
			} else if len(state.Denials()) != 0 {
				t.Fatal("terminal boundary recorded as ordinary denial")
			}
			if (PermissionStopError(ctx) != nil) != test.strict {
				t.Fatal("strict stop mismatch")
			}
		})
	}
	// An ordinary interactive call retains its metadata and text verbatim.
	original := NewPolicyBlockedResult(PolicyBlockPermission, "original reason")
	got := ApplyHeadlessPermissionResult(context.Background(), call, original)
	if got.Error != original.Error || got.PolicyBlock != original.PolicyBlock {
		t.Fatal("interactive policy behavior changed")
	}
}
