package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"
)

// TestExecutorExecuteLoop_DiscussModeGatesFirstMutation pins the discuss-mode
// ask-once action gate (v0.100.51, executor Step 4.7): during an analysis turn
// the FIRST code-mutating tool pauses for a confirm; read-only tools flow free;
// on confirm the turn flips to action so later mutations don't re-prompt; on
// deny the mutation is skipped with an actionable "staying in analysis" result.
func TestExecutorExecuteLoop_DiscussModeGatesFirstMutation(t *testing.T) {
	t.Run("deny keeps analysis — mutation skipped", func(t *testing.T) {
		editTool := &scriptedStaticTool{name: "edit", content: "edited"}
		registry := NewRegistry()
		if err := registry.Register(editTool); err != nil {
			t.Fatal(err)
		}
		cl := &scriptedExecutorClient{model: "glm-5.2", responses: []*client.StreamingResponse{
			buildExecutorTestEditStream("e1", "main.go", "foo"),
			buildExecutorTestTextStream("Okay, staying in analysis."),
		}}
		exec := NewExecutor(registry, cl, time.Second)
		exec.preFlightChecks = false
		confirmCalls := 0
		exec.SetDiscussGate(func() bool { return true }) // analysis, unconfirmed
		exec.SetActionConfirmHandler(func(ctx context.Context, toolName, target string) (bool, error) {
			confirmCalls++
			return false, nil // deny
		})

		_, _, err := exec.Execute(context.Background(), nil, "let's analyze main.go")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if confirmCalls != 1 {
			t.Errorf("confirm calls = %d, want 1", confirmCalls)
		}
		if editTool.calls != 0 {
			t.Errorf("edit executed %d times on deny, want 0", editTool.calls)
		}
		if errStr := lastFunctionResultError(t, cl); !strings.Contains(errStr, "Staying in analysis") {
			t.Errorf("deny result = %q, want it to mention staying in analysis", errStr)
		}
	})

	t.Run("allow flips to action — later mutations run free", func(t *testing.T) {
		editTool := &scriptedStaticTool{name: "edit", content: "edited"}
		registry := NewRegistry()
		if err := registry.Register(editTool); err != nil {
			t.Fatal(err)
		}
		cl := &scriptedExecutorClient{model: "glm-5.2", responses: []*client.StreamingResponse{
			buildExecutorTestEditStream("e1", "main.go", "foo"),
			buildExecutorTestEditStream("e2", "main.go", "bar"),
			buildExecutorTestTextStream("Done."),
		}}
		exec := NewExecutor(registry, cl, time.Second)
		exec.preFlightChecks = false
		confirmCalls := 0
		confirmed := false
		exec.SetDiscussGate(func() bool { return !confirmed }) // discuss until confirmed
		exec.SetActionConfirmHandler(func(ctx context.Context, toolName, target string) (bool, error) {
			confirmCalls++
			confirmed = true // app flips the turn to action on allow
			return true, nil
		})

		_, _, err := exec.Execute(context.Background(), nil, "the build is broken")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if confirmCalls != 1 {
			t.Errorf("confirm calls = %d, want 1 (only the first mutation prompts)", confirmCalls)
		}
		if editTool.calls != 2 {
			t.Errorf("edit executed %d times, want 2 (both ran after one confirm)", editTool.calls)
		}
	})

	t.Run("read-only flows free in discuss mode", func(t *testing.T) {
		readTool := &scriptedReadTool{}
		registry := NewRegistry()
		if err := registry.Register(readTool); err != nil {
			t.Fatal(err)
		}
		cl := &scriptedExecutorClient{model: "glm-5.2", responses: []*client.StreamingResponse{
			buildExecutorTestReadStream("r1"),
			buildExecutorTestTextStream("Here's my analysis."),
		}}
		exec := NewExecutor(registry, cl, time.Second)
		exec.preFlightChecks = false
		confirmCalls := 0
		exec.SetDiscussGate(func() bool { return true })
		exec.SetActionConfirmHandler(func(ctx context.Context, toolName, target string) (bool, error) {
			confirmCalls++
			return true, nil
		})

		_, _, err := exec.Execute(context.Background(), nil, "how does main.go work?")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if confirmCalls != 0 {
			t.Errorf("confirm calls = %d, want 0 (read is not gated)", confirmCalls)
		}
		if readTool.calls == 0 {
			t.Error("read tool did not execute — read-only must flow free in discuss mode")
		}
	})

	t.Run("nil gate disabled — mutation runs ungated", func(t *testing.T) {
		editTool := &scriptedStaticTool{name: "edit", content: "edited"}
		registry := NewRegistry()
		if err := registry.Register(editTool); err != nil {
			t.Fatal(err)
		}
		cl := &scriptedExecutorClient{model: "glm-5.2", responses: []*client.StreamingResponse{
			buildExecutorTestEditStream("e1", "main.go", "foo"),
			buildExecutorTestTextStream("Done."),
		}}
		exec := NewExecutor(registry, cl, time.Second)
		exec.preFlightChecks = false
		// No SetDiscussGate / SetActionConfirmHandler (tests/headless): gate off.
		_, _, err := exec.Execute(context.Background(), nil, "fix it")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if editTool.calls != 1 {
			t.Errorf("edit executed %d times, want 1 (gate disabled when callbacks nil)", editTool.calls)
		}
	})
}
