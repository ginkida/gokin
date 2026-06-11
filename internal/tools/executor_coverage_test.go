package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"gokin/internal/client"
)

func buildExecutorTestReadOffsetStream(id, filePath string, offset, limit int) *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		FunctionCalls: []*genai.FunctionCall{{
			ID:   id,
			Name: "read",
			Args: map[string]any{
				"file_path": filePath,
				"offset":    float64(offset),
				"limit":     float64(limit),
			},
		}},
		Done:         true,
		FinishReason: genai.FinishReasonStop,
	})
}

func buildExecutorTestGrepPatternStream(id, pattern string) *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		FunctionCalls: []*genai.FunctionCall{{
			ID:   id,
			Name: "grep",
			Args: map[string]any{"pattern": pattern},
		}},
		Done:         true,
		FinishReason: genai.FinishReasonStop,
	})
}

func buildExecutorTestEditStream(id, filePath, oldString string) *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		FunctionCalls: []*genai.FunctionCall{{
			ID:   id,
			Name: "edit",
			Args: map[string]any{
				"file_path":  filePath,
				"old_string": oldString,
				"new_string": oldString + "x",
			},
		}},
		Done:         true,
		FinishReason: genai.FinishReasonStop,
	})
}

// lastFunctionResultError extracts the "error" field of the last batch's
// first function result sent back to the model.
func lastFunctionResultError(t *testing.T, cl *scriptedExecutorClient) string {
	t.Helper()
	if len(cl.functionResults) == 0 {
		t.Fatal("no function results recorded")
	}
	last := cl.functionResults[len(cl.functionResults)-1]
	if len(last) == 0 {
		t.Fatal("empty function result batch")
	}
	msg, _ := last[0].Response["error"].(string)
	return msg
}

func TestExecutorCoverage_PerturbedReadLoopGetsRecoveryHint(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// 9 reads of the same file with a drifting offset (1..9, limit 100 — all
	// overlapping spans, nothing else in between). Distinct offsets mean
	// distinct stagnationFingerprints, so the exact-pattern guard never
	// fires; the re-coverage guard must.
	responses := make([]*client.StreamingResponse, 0, 11)
	for i := 1; i <= 9; i++ {
		responses = append(responses, buildExecutorTestReadOffsetStream("r"+string(rune('0'+i)), "project.go", i, 100))
	}
	responses = append(responses, buildExecutorTestTextStream("done"))

	cl := &scriptedExecutorClient{model: "glm-5.1", responses: responses}
	exec := NewExecutor(registry, cl, time.Second)

	_, finalText, err := exec.Execute(context.Background(), nil, "inspect project.go")
	if err != nil {
		t.Fatalf("Execute() error = %v, want graceful hint, not abort", err)
	}
	if finalText != "done" {
		t.Fatalf("finalText = %q, want %q", finalText, "done")
	}
	// Budget is 8 consecutive no-progress re-coverages: reads 1-8 execute
	// (redundancy 0..7), the 9th observes redundancy 8 and is skipped.
	if readTool.calls != 8 {
		t.Fatalf("readTool.calls = %d, want 8 (9th skipped by re-coverage guard)", readTool.calls)
	}
	msg := lastFunctionResultError(t, cl)
	if !strings.Contains(msg, "Loop guard:") || !strings.Contains(msg, "re-read") {
		t.Fatalf("recovery hint missing loop-guard wording: %q", msg)
	}
	if !strings.Contains(msg, "Do not call it again") {
		t.Fatalf("recovery hint missing shared loop-break phrase: %q", msg)
	}
}

func TestExecutorCoverage_ForwardPagingDoesNotTrip(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// 12 forward pages through one large file: non-overlapping spans, each a
	// progress signal. Must execute all 12 with no intervention.
	responses := make([]*client.StreamingResponse, 0, 13)
	for i := range 12 {
		responses = append(responses, buildExecutorTestReadOffsetStream("p", "big.go", 1+i*2000, 2000))
	}
	responses = append(responses, buildExecutorTestTextStream("paged"))

	cl := &scriptedExecutorClient{model: "glm-5.1", responses: responses}
	exec := NewExecutor(registry, cl, time.Second)

	_, _, err := exec.Execute(context.Background(), nil, "page through big.go")
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil for forward paging", err)
	}
	if readTool.calls != 12 {
		t.Fatalf("readTool.calls = %d, want 12 (paging must not trip the guard)", readTool.calls)
	}
}

func TestExecutorCoverage_DistinctFilesDoNotTrip(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	responses := make([]*client.StreamingResponse, 0, 13)
	for i := range 12 {
		responses = append(responses, buildExecutorTestReadOffsetStream("f", "file"+string(rune('a'+i))+".go", 1, 200))
	}
	responses = append(responses, buildExecutorTestTextStream("explored"))

	cl := &scriptedExecutorClient{model: "glm-5.1", responses: responses}
	exec := NewExecutor(registry, cl, time.Second)

	_, _, err := exec.Execute(context.Background(), nil, "explore the package")
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil for distinct files", err)
	}
	if readTool.calls != 12 {
		t.Fatalf("readTool.calls = %d, want 12 (distinct files must not trip the guard)", readTool.calls)
	}
}

func TestExecutorCoverage_RotatingGrepTrips(t *testing.T) {
	registry := NewRegistry()
	grepTool := &scriptedStaticTool{name: "grep", content: "No matches found."}
	if err := registry.Register(grepTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// 5 path-less greps with rotating patterns over one scope, nothing in
	// between. Budget for grep is 4: greps 1-4 execute, the 5th is skipped.
	responses := make([]*client.StreamingResponse, 0, 6)
	for _, pattern := range []string{"alpha", "beta", "gamma", "delta", "epsilon"} {
		responses = append(responses, buildExecutorTestGrepPatternStream("g", pattern))
	}
	responses = append(responses, buildExecutorTestTextStream("searched"))

	cl := &scriptedExecutorClient{model: "glm-5.1", responses: responses}
	exec := NewExecutor(registry, cl, time.Second)

	_, _, err := exec.Execute(context.Background(), nil, "find the handler")
	if err != nil {
		t.Fatalf("Execute() error = %v, want graceful hint, not abort", err)
	}
	if grepTool.calls != 4 {
		t.Fatalf("grepTool.calls = %d, want 4 (5th skipped by re-coverage guard)", grepTool.calls)
	}
	msg := lastFunctionResultError(t, cl)
	if !strings.Contains(msg, "Loop guard:") || !strings.Contains(msg, "re-searched") {
		t.Fatalf("recovery hint missing grep loop-guard wording: %q", msg)
	}
}

func TestExecutorCoverage_ReadEditCycleDoesNotTrip(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	editTool := &scriptedStaticTool{name: "edit", content: "Edited."}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register(read) error = %v", err)
	}
	if err := registry.Register(editTool); err != nil {
		t.Fatalf("Register(edit) error = %v", err)
	}

	// read→edit→read→edit over ONE file with overlapping read spans: every
	// edit between reads is a progress signal that resets the chain, so this
	// classic verify cycle must never trip.
	responses := make([]*client.StreamingResponse, 0, 17)
	for i := range 8 {
		responses = append(responses, buildExecutorTestReadOffsetStream("r"+string(rune('a'+i)), "main.go", 1+i, 100))
		responses = append(responses, buildExecutorTestEditStream("e"+string(rune('a'+i)), "main.go", "old"+string(rune('a'+i))))
	}
	responses = append(responses, buildExecutorTestTextStream("cycled"))

	cl := &scriptedExecutorClient{model: "glm-5.1", responses: responses}
	exec := NewExecutor(registry, cl, time.Second)

	_, _, err := exec.Execute(context.Background(), nil, "iterate on main.go")
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil for read→edit cycles", err)
	}
	if readTool.calls != 8 || editTool.calls != 8 {
		t.Fatalf("calls read=%d edit=%d, want 8/8 (progress gate must reset the chain)", readTool.calls, editTool.calls)
	}
}

func TestExecutorCoverage_AbortsAfterRecoveryBudget(t *testing.T) {
	// Tighten the read budget so the abort path fits inside maxIterations.
	origRead := execCoverageReadBudget
	execCoverageReadBudget = 2
	t.Cleanup(func() { execCoverageReadBudget = origRead })

	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// With budget 2: reads 1-2 execute, 3rd trips (hint 1, window resets),
	// reads 4-5 execute, 6th trips (hint 2, window resets), reads 7-8
	// execute, 9th trips with trips==maxCoverageRecoveryAttempts → abort.
	responses := make([]*client.StreamingResponse, 0, 9)
	for i := 1; i <= 9; i++ {
		responses = append(responses, buildExecutorTestReadOffsetStream("r", "stuck.go", i, 50))
	}

	cl := &scriptedExecutorClient{model: "glm-5.1", responses: responses}
	exec := NewExecutor(registry, cl, time.Second)

	_, _, err := exec.Execute(context.Background(), nil, "inspect stuck.go")
	if err == nil {
		t.Fatal("Execute() error = nil, want re-coverage abort after exhausted hints")
	}
	if !strings.Contains(err.Error(), "re-coverage loop") {
		t.Fatalf("error = %v, want re-coverage loop abort", err)
	}
	if readTool.calls != 6 {
		t.Fatalf("readTool.calls = %d, want 6 (three trips each skip one read)", readTool.calls)
	}
}

func TestExecutorCoverageTracker_GrepWithPathSeparateFromScope(t *testing.T) {
	tr := newExecutorCoverageTracker("/work")

	// Same pattern over two different paths = two targets, no redundancy.
	if r, _, ok := tr.record("grep", map[string]any{"pattern": "x", "path": "internal/a"}); !ok || r != 0 {
		t.Fatalf("first grep on path a: redundant=%d ok=%v, want 0/true", r, ok)
	}
	if r, _, ok := tr.record("grep", map[string]any{"pattern": "x", "path": "internal/b"}); !ok || r != 0 {
		t.Fatalf("first grep on path b: redundant=%d ok=%v, want 0/true", r, ok)
	}
	// Re-searching path a immediately after... but path b's first sight was
	// progress, so the chain stays reset.
	if r, _, _ := tr.record("grep", map[string]any{"pattern": "y", "path": "internal/a"}); r != 0 {
		t.Fatalf("re-grep after progress: redundant=%d, want 0", r)
	}
	// Now rotate over path a with no progress: redundancy climbs.
	if r, _, _ := tr.record("grep", map[string]any{"pattern": "z", "path": "internal/a"}); r != 1 {
		t.Fatalf("no-progress re-grep: redundant=%d, want 1", r)
	}
}

func TestExecutorCoverageTracker_ResetWindowClearsChains(t *testing.T) {
	tr := newExecutorCoverageTracker("")

	for i := range 4 {
		tr.record("read", map[string]any{"file_path": "x.go", "offset": float64(1 + i), "limit": float64(100)})
	}
	tr.resetWindow()
	r, _, _ := tr.record("read", map[string]any{"file_path": "x.go", "offset": float64(1), "limit": float64(100)})
	if r != 0 {
		t.Fatalf("redundant after resetWindow = %d, want 0 (fresh window)", r)
	}
}
