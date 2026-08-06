package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"gokin/internal/repl"

	"google.golang.org/genai"
)

type replExecutor interface {
	Execute(context.Context, string) (repl.Result, error)
	Reset(context.Context) error
	Stats() repl.Stats
}

// ReplExecTool exposes the session-scoped, read-only computation plane. The
// Python worker has no ambient mutation/network capability; future privileged
// context methods must callback through Executor rather than being added here.
type ReplExecTool struct {
	mu      sync.RWMutex
	manager replExecutor
}

func NewReplExecTool(manager replExecutor) *ReplExecTool {
	return &ReplExecTool{manager: manager}
}

func (t *ReplExecTool) SetManager(manager replExecutor) {
	t.mu.Lock()
	t.manager = manager
	t.mu.Unlock()
}

func (t *ReplExecTool) Name() string { return "repl_exec" }

// Description documents the exact Python surface, not just its existence.
//
// Listing capability names ("context (search/read/git/...)") without signatures
// or return shapes made every first call a guess: search_code returns a dict,
// not a list, and workspace is a property, not a method. A caller that guesses
// wrong spends a round on a TypeError, while grep next door has a precise
// schema that works immediately — so the cheaper tool wins regardless of which
// one suits the question. The signatures below are pinned by a test that
// executes each one, so this text cannot drift from the runtime.
func (t *ReplExecTool) Description() string {
	return `Persistent workspace-read-only Python session for multi-step codebase analysis. State survives across execute calls.

Best for questions ANSWERED BY AGGREGATION over many files (counts, rankings, cross-file joins): return the conclusion instead of pulling every match into the transcript. For "show me the matches", grep is simpler.

context.workspace -> str (property, not a call)
context.search_code(query, path=".", limit=50, case_sensitive=False)
    -> {"matches": [{"path","line","text"}], "scanned_files": int, "truncated": bool}
context.read_slice(path, start_line=1, end_line=200)
    -> {"path", "start_line", "end_line", "lines": [{"line","text"}]}
context.git_status() -> str ; context.git_diff(staged=False) -> str
context.artifact_get(id, offset=0, limit=...) -> {"id","offset","size","content","has_more"}
context.runtime_limits() -> dict

rlm(instruction, dynamic_context=None, *, agent_type="general", max_turns=20, model="") -> dict
rlm.async_call(...) -> future with .result(timeout=600), .poll(), .cancel()
rlm.harness: create_prompt/update_prompt/list_prompts/delete_prompt, put_memory/get_memory/list_memory/delete_memory, create_skill/list_skills/delete_skill

The final expression is returned like an interactive REPL. Direct writes, subprocesses, sockets and native libraries are blocked; use structured tools for external actions.`
}

func (t *ReplExecTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"action": {
					Type: genai.TypeString, Enum: []string{"execute", "status", "reset"},
					Description: "execute (default), status (inspect bounded kernel health), or reset (discard Python globals/artifacts before the next cell).",
				},
				"code": {
					Type:        genai.TypeString,
					Description: "Python code to execute. The final expression is returned like an interactive REPL. Keep large intermediate data in variables or artifacts instead of printing it.",
				},
			},
		},
	}
}

func (t *ReplExecTool) Validate(args map[string]any) error {
	action := strings.ToLower(strings.TrimSpace(GetStringDefault(args, "action", "execute")))
	if action != "execute" && action != "status" && action != "reset" {
		return NewValidationError("action", "must be execute, status, or reset")
	}
	if action != "execute" {
		return nil
	}
	code, ok := GetString(args, "code")
	if !ok || strings.TrimSpace(code) == "" {
		return NewValidationError("code", "must be a non-empty string")
	}
	return nil
}

func (t *ReplExecTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if err := t.Validate(args); err != nil {
		return NewErrorResult("validation error: " + err.Error()), nil
	}
	t.mu.RLock()
	manager := t.manager
	t.mu.RUnlock()
	if manager == nil {
		return NewErrorResult("stateful REPL is unavailable in this session; continue with structured read/search tools"), nil
	}
	action := strings.ToLower(strings.TrimSpace(GetStringDefault(args, "action", "execute")))
	if action == "status" {
		stats := manager.Stats()
		return NewSuccessResultWithData(formatREPLStats(stats), stats), nil
	}
	if action == "reset" {
		if err := manager.Reset(ctx); err != nil {
			return NewErrorResult("stateful REPL reset failed: " + err.Error()), nil
		}
		stats := manager.Stats()
		return NewSuccessResultWithData("stateful REPL reset; the next cell will start a clean generation", stats), nil
	}
	code, _ := GetString(args, "code")
	result, err := manager.Execute(ctx, code)
	if err != nil {
		stats := manager.Stats()
		failure := NewErrorResult("stateful REPL execution failed: " + err.Error())
		failure.Content = formatREPLStats(stats) + "\nThe failed kernel was discarded; retrying code starts a clean generation."
		failure.Data = stats
		return failure, nil
	}
	content := formatREPLResult(result)
	if result.Error != nil {
		failure := fmt.Sprintf("Python %s: %s", result.Error.Type, result.Error.Message)
		if result.Error.Traceback != "" {
			failure += "\n" + result.Error.Traceback
		}
		return ToolResult{Success: false, Error: failure, Content: content}, nil
	}
	return NewSuccessResultWithData(content, result), nil
}

func formatREPLStats(stats repl.Stats) string {
	status := "stopped"
	if stats.Running {
		status = "running"
	}
	result := fmt.Sprintf(
		"kernel %s; generation=%d restarts=%d manual_resets=%d executions=%d transport_failures=%d timeouts=%d",
		status, stats.Generation, stats.Restarts, stats.ManualResets,
		stats.Executions, stats.TransportFailures, stats.Timeouts,
	)
	if stats.LastError != "" {
		result += "\nlast transport failure: " + stats.LastError
	}
	return result
}

func formatREPLResult(result repl.Result) string {
	var out strings.Builder
	fmt.Fprintf(&out, "kernel generation: %d", result.Generation)
	if result.Stdout != "" {
		out.WriteString("\nstdout:\n")
		out.WriteString(result.Stdout)
	}
	if result.Stderr != "" {
		out.WriteString("\nstderr:\n")
		out.WriteString(result.Stderr)
	}
	if result.Value != "" {
		out.WriteString("\nvalue:\n")
		out.WriteString(result.Value)
	}
	if result.Artifact != nil {
		fmt.Fprintf(&out, "\nartifact: %s (%d bytes", result.Artifact.ID, result.Artifact.Size)
		if result.Artifact.Truncated {
			out.WriteString(", capped")
		}
		out.WriteString(") — inspect with context.artifact_get")
	}
	if result.Truncated {
		out.WriteString("\noutput was bounded; keep processing the artifact inside the REPL")
	}
	return out.String()
}
