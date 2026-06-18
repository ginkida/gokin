package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"
)

// TestIncompleteTodoSummary pins the completion signal: completed items don't
// count, pending/in_progress do, and a missing/empty todo tool is a safe 0.
func TestIncompleteTodoSummary(t *testing.T) {
	if n, _ := IncompleteTodoSummary(nil); n != 0 {
		t.Errorf("nil registry → %d, want 0", n)
	}
	reg := NewRegistry()
	if n, _ := IncompleteTodoSummary(reg); n != 0 {
		t.Errorf("no todo tool → %d, want 0", n)
	}
	td := NewTodoTool()
	if err := reg.Register(td); err != nil {
		t.Fatal(err)
	}
	td.RestoreItems([]TodoItem{{Content: "a", Status: "completed"}, {Content: "b", Status: "completed"}})
	if n, _ := IncompleteTodoSummary(reg); n != 0 {
		t.Errorf("all complete → %d, want 0", n)
	}
	td.RestoreItems([]TodoItem{
		{Content: "done", Status: "completed"},
		{Content: "doing", Status: "in_progress"},
		{Content: "todo1", Status: "pending"},
		{Content: "todo2", Status: "pending"},
	})
	n, summary := IncompleteTodoSummary(reg)
	if n != 3 {
		t.Fatalf("incomplete count = %d, want 3", n)
	}
	if !strings.Contains(summary, "doing") || !strings.Contains(summary, "todo1") || !strings.Contains(summary, "todo2") {
		t.Errorf("summary missing incomplete items: %q", summary)
	}
	if strings.Contains(summary, "done") {
		t.Errorf("summary must exclude completed items: %q", summary)
	}
}

// TestExecutorExecuteLoop_IncompleteWorkContinuesThenBounded: a model that stops
// with text (no tool calls) while a todo is unfinished is nudged to keep going —
// the "Продолжаю… then stops" failure — but bounded so a model that only narrates
// can't loop forever.
func TestExecutorExecuteLoop_IncompleteWorkContinuesThenBounded(t *testing.T) {
	registry := NewRegistry()
	td := NewTodoTool()
	if err := registry.Register(td); err != nil {
		t.Fatal(err)
	}
	td.RestoreItems([]TodoItem{
		{Content: "implement backup/restore", Status: "in_progress", ActiveForm: "Implementing backup/restore"},
	})

	// Model keeps narrating and never acts → nudged MaxIncompleteWorkContinuations
	// times, then the turn ends. Provide a couple extra responses as a safety net.
	responses := make([]*client.StreamingResponse, 0, MaxIncompleteWorkContinuations+2)
	for i := 0; i < MaxIncompleteWorkContinuations+2; i++ {
		responses = append(responses, buildExecutorTestTextStream("Продолжаю с backup/restore командой."))
	}
	cl := &scriptedExecutorClient{model: "glm-5.1", responses: responses}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false

	history, finalText, err := exec.Execute(context.Background(), nil, "build the backup feature")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	historyText := strings.Join(flattenHistoryTexts(history), "\n")
	nudges := strings.Count(historyText, "task list still has")
	if nudges != MaxIncompleteWorkContinuations {
		t.Errorf("continuation nudges = %d, want %d", nudges, MaxIncompleteWorkContinuations)
	}
	if finalText == "" {
		t.Error("finalText should not be empty")
	}
	// Must terminate: initial call + MaxIncompleteWorkContinuations continuations.
	if cl.next != MaxIncompleteWorkContinuations+1 {
		t.Errorf("model calls = %d, want %d (initial + %d continuations)", cl.next, MaxIncompleteWorkContinuations+1, MaxIncompleteWorkContinuations)
	}
}

// TestExecutorExecuteLoop_IncompleteWorkPreservesCarriedText pins the fix for
// the carriedText-interleave defect: a max_tokens continuation followed by
// incomplete-work continuations must not silently drop any text segment from the
// final answer (pre-fix, only "AAA"+last survived).
func TestExecutorExecuteLoop_IncompleteWorkPreservesCarriedText(t *testing.T) {
	registry := NewRegistry()
	td := NewTodoTool()
	if err := registry.Register(td); err != nil {
		t.Fatal(err)
	}
	td.RestoreItems([]TodoItem{{Content: "ship it", Status: "in_progress", ActiveForm: "Shipping"}})

	cl := &scriptedExecutorClient{model: "glm-5.1", responses: []*client.StreamingResponse{
		buildExecutorTestMaxTokensTextStream("AAA"), // truncated → carriedText="AAA", truncation continue
		buildExecutorTestTextStream("BBB"),          // incomplete-work continue #1
		buildExecutorTestTextStream("CCC"),          // incomplete-work continue #2
		buildExecutorTestTextStream("DDD"),          // incomplete-work continue #3
		buildExecutorTestTextStream("EEE"),          // budget exhausted → break
	}}
	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false

	_, finalText, err := exec.Execute(context.Background(), nil, "do the work")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, want := range []string{"AAA", "BBB", "CCC", "DDD", "EEE"} {
		if !strings.Contains(finalText, want) {
			t.Errorf("finalText silently dropped %q: %q", want, finalText)
		}
	}
}

// TestExecutorExecuteLoop_NoContinuationWhenTodosComplete: all todos done → a
// text-only response ends the turn normally (no nudge, single model call).
func TestExecutorExecuteLoop_NoContinuationWhenTodosComplete(t *testing.T) {
	registry := NewRegistry()
	td := NewTodoTool()
	if err := registry.Register(td); err != nil {
		t.Fatal(err)
	}
	td.RestoreItems([]TodoItem{{Content: "done", Status: "completed"}})

	cl := &scriptedExecutorClient{model: "glm-5.1", responses: []*client.StreamingResponse{
		buildExecutorTestTextStream("All done."),
	}}
	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false

	_, finalText, err := exec.Execute(context.Background(), nil, "finish")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if finalText != "All done." {
		t.Errorf("finalText = %q, want 'All done.'", finalText)
	}
	if cl.next != 1 {
		t.Errorf("model calls = %d, want 1 (no continuation when complete)", cl.next)
	}
}
