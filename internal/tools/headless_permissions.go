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

	verification        map[string]VerificationCheck
	childStatuses       map[string]string
	deniedMutationPaths []string
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

// Incomplete reports observed child work that has not completed successfully.
// A later successful resume of the same child replaces its earlier failure.
func (s *HeadlessPermissions) Incomplete() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, status := range s.childStatuses {
		switch status {
		case "pending", "running", "failed", "cancelled", "interrupted", "timeout":
			return true
		}
	}
	return false
}

// HeadlessPermissionDenied prevents automatic completion exchanges from
// asking the model to repeat work the invocation's policy already refused.
// The model still receives the denial and can finish its current response.
func HeadlessPermissionDenied(ctx context.Context) bool {
	state, _ := ctx.Value(headlessPermissionsKey{}).(*HeadlessPermissions)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return len(state.denials) > 0
}

// HeadlessDeniedMutationPaths stays private to the invocation: public denial
// receipts deliberately omit raw arguments. Only actual mutation targets may
// explain why a claimed edit is absent; a denied build/read cannot do so.
func HeadlessDeniedMutationPaths(ctx context.Context) []string {
	state, _ := ctx.Value(headlessPermissionsKey{}).(*HeadlessPermissions)
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return append([]string(nil), state.deniedMutationPaths...)
}

func deniedMutationTargets(name string, args map[string]any) []string {
	if preview, _ := args["dry_run"].(bool); preview {
		return nil
	}
	var keys []string
	switch name {
	case "write", "edit", "refactor":
		keys = []string{"file_path"}
	case "delete", "mkdir":
		keys = []string{"path"}
	case "batch":
		// Explicit files below are known targets; a pattern is not a path.
	case "copy":
		keys = []string{"destination"}
	case "move":
		keys = []string{"source", "destination"}
	case "ssh":
		if action, _ := args["action"].(string); action == "download" {
			keys = []string{"local_path"}
		}
	default:
		return nil
	}
	var paths []string
	for _, key := range keys {
		if path, _ := args[key].(string); path != "" {
			paths = append(paths, path)
		}
	}
	if name == "batch" {
		switch files := args["files"].(type) {
		case []string:
			paths = append(paths, files...)
		case []any:
			for _, value := range files {
				if path, ok := value.(string); ok {
					paths = append(paths, path)
				}
			}
		}
	}
	return paths
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
	args := call.Args
	if result.HookUpdatedInput != nil {
		args = result.HookUpdatedInput
	}
	state.deniedMutationPaths = append(state.deniedMutationPaths, deniedMutationTargets(call.Name, args)...)
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
