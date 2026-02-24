package agent

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/logging"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

// RecoveryExecutor attempts automatic recovery from tool errors using AutoFixAction.
type RecoveryExecutor struct {
	maxAttempts int
}

// NewRecoveryExecutor creates a new RecoveryExecutor with the given max attempts per call key.
func NewRecoveryExecutor(maxAttempts int) *RecoveryExecutor {
	if maxAttempts <= 0 {
		maxAttempts = 2
	}
	return &RecoveryExecutor{maxAttempts: maxAttempts}
}

// AttemptAutoFix tries to automatically recover from a tool error.
// Returns (result, true) if recovery succeeded, (nil, false) otherwise.
func (r *RecoveryExecutor) AttemptAutoFix(
	ctx context.Context,
	agent *Agent,
	originalCall *genai.FunctionCall,
	reflection *Reflection,
	attempt int,
) (tools.ToolResult, bool) {
	if reflection == nil {
		return tools.ToolResult{}, false
	}

	if attempt >= r.maxAttempts {
		return r.tryAlternativePath(ctx, agent, originalCall, reflection)
	}

	if reflection.AutoFix != nil {
		if result, handled := r.executeFix(ctx, agent, originalCall, reflection.AutoFix); handled {
			return result, true
		}
	}

	for _, fix := range r.deterministicFixChain(reflection, originalCall) {
		if result, handled := r.executeFix(ctx, agent, originalCall, fix); handled {
			return result, true
		}
	}

	return tools.ToolResult{}, false
}

func (r *RecoveryExecutor) executeFix(
	ctx context.Context,
	agent *Agent,
	originalCall *genai.FunctionCall,
	fix *AutoFixAction,
) (tools.ToolResult, bool) {
	if fix == nil {
		return tools.ToolResult{}, false
	}

	switch fix.FixType {
	case "retry_with_args":
		return r.retryWithArgs(ctx, agent, originalCall, fix)
	case "run_tool_first":
		return r.runToolFirst(ctx, agent, originalCall, fix)
	case "modify_and_retry":
		return r.modifyAndRetry(ctx, agent, originalCall, fix)
	default:
		logging.Debug("unknown auto-fix type", "type", fix.FixType)
		return tools.ToolResult{}, false
	}
}

func (r *RecoveryExecutor) deterministicFixChain(reflection *Reflection, originalCall *genai.FunctionCall) []*AutoFixAction {
	if reflection == nil || originalCall == nil {
		return nil
	}

	var chain []*AutoFixAction
	category := strings.ToLower(strings.TrimSpace(reflection.Category))
	switch category {
	case "file_not_found":
		chain = append(chain, &AutoFixAction{
			FixType:  "modify_and_retry",
			ToolName: "glob",
			ArgModifier: func(originalArgs map[string]any, fixResult string) map[string]any {
				foundPath := firstGlobPath(fixResult)
				if foundPath == "" {
					return nil
				}
				modified := make(map[string]any, len(originalArgs))
				for k, v := range originalArgs {
					modified[k] = v
				}
				for _, key := range []string{"file_path", "path", "filepath", "file"} {
					if _, ok := modified[key]; ok {
						modified[key] = foundPath
						return modified
					}
				}
				modified["file_path"] = foundPath
				return modified
			},
		})
		chain = append(chain, &AutoFixAction{
			FixType:  "run_tool_first",
			ToolName: "glob",
			ToolArgs: buildGlobArgs(originalCall.Args),
		})
	case "unique_match_error":
		chain = append(chain, &AutoFixAction{
			FixType:  "run_tool_first",
			ToolName: "read",
		})
	case "command_not_found":
		chain = append(chain, &AutoFixAction{
			FixType:  "run_tool_first",
			ToolName: "bash",
		})
	case "invalid_args":
		if _, ok := originalCall.Args["file_path"]; ok {
			chain = append(chain, &AutoFixAction{
				FixType:  "run_tool_first",
				ToolName: "read",
			})
		}
		if globArgs := buildGlobArgs(originalCall.Args); globArgs != nil {
			chain = append(chain, &AutoFixAction{
				FixType:  "run_tool_first",
				ToolName: "glob",
				ToolArgs: globArgs,
			})
		}
	case "timeout", "network_error", "rate_limit":
		chain = append(chain, &AutoFixAction{
			FixType:      "retry_with_args",
			ModifiedArgs: originalCall.Args,
		})
	}

	if reflection.Alternative != "" {
		alternative := strings.TrimSpace(strings.ToLower(reflection.Alternative))
		if alternative == "read" || alternative == "glob" || alternative == "bash" {
			chain = append(chain, &AutoFixAction{
				FixType:  "run_tool_first",
				ToolName: alternative,
			})
		}
	}

	seen := make(map[string]bool)
	out := make([]*AutoFixAction, 0, len(chain))
	for _, fix := range chain {
		if fix == nil {
			continue
		}
		key := fix.FixType + ":" + fix.ToolName
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, fix)
	}
	return out
}

func (r *RecoveryExecutor) tryAlternativePath(
	ctx context.Context,
	agent *Agent,
	originalCall *genai.FunctionCall,
	reflection *Reflection,
) (tools.ToolResult, bool) {
	if reflection == nil {
		return tools.ToolResult{}, false
	}
	alt := strings.TrimSpace(strings.ToLower(reflection.Alternative))
	if alt == "" {
		return tools.ToolResult{}, false
	}
	if alt != "read" && alt != "glob" && alt != "bash" {
		return tools.ToolResult{}, false
	}

	logging.Info("recovery budget exhausted, switching to alternative path",
		"tool", originalCall.Name,
		"alternative", alt,
		"category", reflection.Category)

	return r.executeFix(ctx, agent, originalCall, &AutoFixAction{
		FixType:  "run_tool_first",
		ToolName: alt,
	})
}

// retryWithArgs retries the original tool with modified static args.
func (r *RecoveryExecutor) retryWithArgs(
	ctx context.Context,
	agent *Agent,
	originalCall *genai.FunctionCall,
	fix *AutoFixAction,
) (tools.ToolResult, bool) {
	if fix.ModifiedArgs == nil {
		return tools.ToolResult{}, false
	}

	retryCall := &genai.FunctionCall{
		ID:   originalCall.ID,
		Name: originalCall.Name,
		Args: fix.ModifiedArgs,
	}

	result := agent.executeTool(ctx, retryCall)
	if result.Success {
		return result, true
	}
	return tools.ToolResult{}, false
}

// runToolFirst runs a prerequisite tool, then retries the original if it succeeds.
// For "read" fixes, we don't retry — we return the file content as enriched context.
func (r *RecoveryExecutor) runToolFirst(
	ctx context.Context,
	agent *Agent,
	originalCall *genai.FunctionCall,
	fix *AutoFixAction,
) (tools.ToolResult, bool) {
	// Build args for the fix tool
	var fixArgs map[string]any
	switch fix.ToolName {
	case "read":
		fixArgs = buildReadArgs(originalCall.Args)
	case "glob":
		fixArgs = buildGlobArgs(originalCall.Args)
	case "bash":
		fixArgs = buildWhichArgs(originalCall.Args)
	default:
		fixArgs = fix.ToolArgs
	}

	if fixArgs == nil {
		return tools.ToolResult{}, false
	}

	// Execute the fix tool
	fixCall := &genai.FunctionCall{
		Name: fix.ToolName,
		Args: fixArgs,
	}

	fixResult := agent.executeTool(ctx, fixCall)
	if !fixResult.Success {
		logging.Debug("auto-fix prerequisite tool failed", "tool", fix.ToolName, "error", fixResult.Error)
		return tools.ToolResult{}, false
	}

	// For read/bash prerequisite tools, return enriched context rather than blindly retrying.
	// The model needs to see the file content to formulate a better edit.
	// We mark Success=false so the model knows the original call failed,
	// but provide the prerequisite output as context for a smarter retry.
	enrichedContent := fmt.Sprintf(
		"**Auto-recovery:** The original `%s` call failed. Ran `%s` to gather context for retry.\n\n---\n%s",
		originalCall.Name, fix.ToolName, fixResult.Content,
	)
	return tools.ToolResult{
		Content: enrichedContent,
		Success: false,
		Data: map[string]any{
			"auto_fix":      true,
			"fix_type":      "enriched_context",
			"original_tool": originalCall.Name,
			"prerequisite":  fix.ToolName,
		},
	}, true
}

// modifyAndRetry runs a fix tool (e.g., glob), then uses ArgModifier to build new args and retries.
func (r *RecoveryExecutor) modifyAndRetry(
	ctx context.Context,
	agent *Agent,
	originalCall *genai.FunctionCall,
	fix *AutoFixAction,
) (tools.ToolResult, bool) {
	if fix.ArgModifier == nil {
		return tools.ToolResult{}, false
	}

	// Build args for the fix tool (e.g., glob)
	var fixArgs map[string]any
	switch fix.ToolName {
	case "glob":
		fixArgs = buildGlobArgs(originalCall.Args)
	default:
		fixArgs = fix.ToolArgs
	}

	if fixArgs == nil {
		return tools.ToolResult{}, false
	}

	// Execute the fix tool
	fixCall := &genai.FunctionCall{
		Name: fix.ToolName,
		Args: fixArgs,
	}

	fixResult := agent.executeTool(ctx, fixCall)
	if !fixResult.Success || fixResult.Content == "" {
		logging.Debug("auto-fix tool returned no results", "tool", fix.ToolName)
		return tools.ToolResult{}, false
	}

	// Use ArgModifier to build modified args
	modifiedArgs := fix.ArgModifier(originalCall.Args, fixResult.Content)
	if modifiedArgs == nil {
		return tools.ToolResult{}, false
	}

	// Retry the original tool with modified args
	retryCall := &genai.FunctionCall{
		ID:   originalCall.ID,
		Name: originalCall.Name,
		Args: modifiedArgs,
	}

	result := agent.executeTool(ctx, retryCall)
	if result.Success {
		logging.Info("auto-fix modify_and_retry succeeded",
			"tool", originalCall.Name,
			"fix_tool", fix.ToolName)
		return result, true
	}

	return tools.ToolResult{}, false
}
