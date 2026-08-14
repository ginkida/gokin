package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	return `Persistent read-only Python for multi-file counts/ranks/joins; globals survive. Prefer ONE cell returning conclusions plus compact evidence. Use grep/read/bash for targeted matches; each execute costs a model round.

context.workspace -> str property (not a call)
context.search_code(query, path=".", limit=50, case_sensitive=False, regex=False)
    -> {"matches":[{"path","line","text"}],"scanned_files","searched_files","skipped_files","truncated"}; literal by default.
context.count_code(query, path=".", case_sensitive=False, regex=False, group_by=None, sample_limit=0)
    -> {"matching_lines","matching_files","groups", shared scan metadata, optional "samples"/"samples_truncated"}
    Exact count; group_by: None|"file"|"top_dir"|"extension"; sample_limit adds evidence.
context.count_code_many(queries, path=".", case_sensitive=False, regex=False, group_by=None, sample_limit=0)
    -> {"counts": [{"query","matching_lines","matching_files","groups", optional samples}], shared scan metadata}
    Counts bounded queries in ONE inventory/read pass; prefer for comparisons.
context.list_files(path=".", pattern=None) -> {"files":[{"path","size"}],"scanned_files","truncated"}
context.file_stats(path=".", pattern=None, exclude_pattern=None, group_by=None)
    -> {"matching_files","total_bytes","groups":{key:{"files","bytes"}},"scanned_files","truncated"}; group_by: "extension" or "top_dir". Prefer for totals.
    Patterns are workspace-relative fnmatch. Scans honor Git ignores; file_stats streams, other scans share one scope snapshot/cell.
context.read_slice(path, start_line=1, end_line=200)
    -> {"path", "start_line", "end_line", "lines": [{"line","text"}]}
context.artifact_get(id, offset=0, limit=...) -> {"id","offset","next_offset","size","content","has_more"}
    UTF-8 byte offsets; continue with next_offset.
context.runtime_limits() -> dict of memory/output/file/search/read bounds

rlm(instruction, dynamic_context=None, *, agent_type="general", max_turns=20, model="") -> dict
rlm.async_call(...) -> future with .result(timeout=600), .poll(), .cancel()
rlm.harness (loaded on first use when available): create_prompt/update_prompt/list_prompts/delete_prompt, put_memory/get_memory/list_memory/delete_memory, create_skill/list_skills/delete_skill

Final expression returns; large channels become artifacts. Imports allow analytical stdlib only (JSON/regex/math/stats/collections/CSV/date/hash/encoding). Directory enumeration, direct open, reflection, dynamic code, writes, processes, threads, sockets, native libraries, and Git are blocked; use bounded context APIs and git_status/git_diff for external actions.`
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
					Description: "Python code to execute. Prefer one complete scan/filter/aggregate cell because every additional call costs another model round. The final expression is returned like an interactive REPL; keep large intermediates in variables or artifacts.",
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
		if errors.Is(err, repl.ErrUnavailable) {
			failure.Content = formatREPLStats(stats) +
				"\nSecure REPL initialization failed; continue this session with structured read/search tools."
		} else {
			failure.Content = formatREPLStats(stats) + "\nThe failed kernel was discarded; retrying code starts a clean generation."
		}
		failure.Data = stats
		return failure, nil
	}
	content := formatREPLResult(result)
	if result.Error != nil {
		failure := fmt.Sprintf("Python %s: %s", result.Error.Type, result.Error.Message)
		if result.Error.Traceback != "" {
			failure += "\n" + result.Error.Traceback
		}
		return ToolResult{Success: false, Error: failure, Content: content, Data: replResultMetadata(result)}, nil
	}
	return NewSuccessResultWithData(content, replResultMetadata(result)), nil
}

// replResultMetadata avoids serializing stdout/stderr/value twice: Content is
// already the model-visible representation, while Data only needs generation,
// overflow handles, and bounded runtime telemetry for structured consumers.
func replResultMetadata(result repl.Result) repl.Result {
	result.Stdout = ""
	result.Stderr = ""
	result.Value = ""
	return result
}

func formatREPLStats(stats repl.Stats) string {
	status := "stopped"
	if stats.Running {
		status = "running"
	}
	result := fmt.Sprintf(
		"kernel %s; generation=%d restarts=%d manual_resets=%d executions=%d transport_failures=%d timeouts=%d resource_limit_failures=%d",
		status, stats.Generation, stats.Restarts, stats.ManualResets,
		stats.Executions, stats.TransportFailures, stats.Timeouts, stats.ResourceLimitFailures,
	)
	if stats.LastError != "" {
		result += "\nlast runtime failure: " + stats.LastError
	}
	return result
}

func formatREPLResult(result repl.Result) string {
	var out strings.Builder
	fmt.Fprintf(&out, "kernel generation: %d", result.Generation)
	// Put recovery handles ahead of inline payloads. ToolResult has a final
	// global output cap, so metadata must remain visible even if a malformed or
	// legacy worker returns more inline text than the current worker permits.
	if len(result.Artifacts) > 0 {
		names := make([]string, 0, len(result.Artifacts))
		for name := range result.Artifacts {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			appendREPLArtifact(&out, "artifact["+name+"]", result.Artifacts[name])
		}
	} else if result.Artifact != nil {
		appendREPLArtifact(&out, "artifact", result.Artifact)
	}
	if result.Truncated {
		out.WriteString("\noutput was bounded; keep processing the named artifact(s) inside the REPL")
	}
	if result.KernelReset {
		out.WriteString("\nresource limit reached; this kernel generation was discarded")
	}
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
	return out.String()
}

func appendREPLArtifact(out *strings.Builder, label string, artifact *repl.ArtifactRef) {
	if out == nil || artifact == nil {
		return
	}
	fmt.Fprintf(out, "\n%s: %s (%d bytes", label, artifact.ID, artifact.Size)
	if artifact.Truncated {
		out.WriteString(", capped")
	}
	out.WriteString(") — inspect with context.artifact_get")
}
