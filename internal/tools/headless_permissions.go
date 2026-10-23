package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"google.golang.org/genai"
)

// PermissionDenial deliberately contains no raw input or policy reason: either
// may contain shell credentials or secret values supplied by hooks.
type PermissionDenial struct {
	Tool      string `json:"tool"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Reason    string `json:"reason"`
}

type HeadlessPermissions struct {
	mu      sync.Mutex
	strict  bool
	mode    string
	denials []PermissionDenial
	stopped bool
}
type headlessPermissionsKey struct{}

var ErrStrictPermissionDenied = errors.New("strict permissions: tool call denied")

func ContextWithHeadlessPermissions(ctx context.Context, strict bool, mode string) (context.Context, *HeadlessPermissions) {
	switch mode {
	case "default", "acceptEdits", "dontAsk", "bypassPermissions", "plan":
	default:
		mode = "default"
	}
	state := &HeadlessPermissions{strict: strict, mode: mode, denials: []PermissionDenial{}}
	return context.WithValue(ctx, headlessPermissionsKey{}, state), state
}
func (s *HeadlessPermissions) Denials() []PermissionDenial {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PermissionDenial{}, s.denials...)
}
func PermissionStopError(ctx context.Context) error {
	state, _ := ctx.Value(headlessPermissionsKey{}).(*HeadlessPermissions)
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.stopped {
		return ErrStrictPermissionDenied
	}
	return nil
}

// ApplyHeadlessPermissionResult is shared by foreground and delegated tools.
// Capability/schema ceilings and successful plan protocol refusals stay terminal.
func ApplyHeadlessPermissionResult(ctx context.Context, call *genai.FunctionCall, result ToolResult) ToolResult {
	state, _ := ctx.Value(headlessPermissionsKey{}).(*HeadlessPermissions)
	if state == nil || result.PolicyBlock == nil || result.PolicyBlock.Terminal || result.PolicyBlock.Recoverable || result.Success && result.PolicyBlock.Kind == PolicyBlockPlan {
		return result
	}
	kind := result.PolicyBlock.Kind
	if kind != PolicyBlockPermission && kind != PolicyBlockPlan {
		return result
	}
	state.mu.Lock()
	if state.stopped {
		state.mu.Unlock()
		return result
	}
	reason := fmt.Sprintf("Denied by permission mode %s or a permission rule. Do not retry this call; continue without it.", state.mode)
	state.denials = append(state.denials, PermissionDenial{Tool: call.Name, ToolUseID: call.ID, Reason: reason})
	strict := state.strict
	if strict {
		state.stopped = true
	}
	state.mu.Unlock()
	result.Success = false
	result.Error = reason
	if !strict {
		block := *result.PolicyBlock
		block.Recoverable = true
		result.PolicyBlock = &block
	}
	return result
}

type noSelfReviewKey struct{}

func ContextWithoutSelfReview(ctx context.Context) context.Context {
	return context.WithValue(ctx, noSelfReviewKey{}, true)
}
func SelfReviewDisabled(ctx context.Context) bool {
	disabled, _ := ctx.Value(noSelfReviewKey{}).(bool)
	return disabled
}

func newTerminalPermissionResult(reason string) ToolResult {
	result := NewPolicyBlockedResult(PolicyBlockPermission, reason)
	result.PolicyBlock.Terminal = true
	return result
}
