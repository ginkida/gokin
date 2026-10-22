package permission

import (
	"context"
	"encoding/json"
)

type hookDecisionKey struct{}
type hookDecision struct{ tool, input, decision string }

// ContextWithHookDecision binds a trusted hook's decision to this exact call.
// It cannot authorize a changed argument map or a delegated/nested tool.
func ContextWithHookDecision(ctx context.Context, tool string, args map[string]any, decision string) context.Context {
	input, err := json.Marshal(args)
	if err != nil {
		return ctx
	}
	return context.WithValue(ctx, hookDecisionKey{}, hookDecision{tool, string(input), decision})
}

func hookDecisionFor(ctx context.Context, tool string, args map[string]any) string {
	decision, ok := ctx.Value(hookDecisionKey{}).(hookDecision)
	if !ok || decision.tool != tool {
		return ""
	}
	input, err := json.Marshal(args)
	if err != nil || string(input) != decision.input {
		return ""
	}
	return decision.decision
}
