package tools

import (
	"context"
	"strings"
	"testing"
)

func wiredLoopControl(loops []LoopControlInfo, log *[]string) *LoopControlTool {
	t := NewLoopControlTool()
	rec := func(action string) func(string) error {
		return func(id string) error {
			*log = append(*log, action+":"+id)
			return nil
		}
	}
	t.SetCallbacks(func() []LoopControlInfo { return loops }, rec("pause"), rec("resume"), rec("stop"))
	return t
}

// TestLoopControl_SoleLoopNoIDNeeded: "отмени мой loop" must work without the
// model hunting for an id — with exactly one eligible loop the action resolves
// automatically.
func TestLoopControl_SoleLoopNoIDNeeded(t *testing.T) {
	var log []string
	tool := wiredLoopControl([]LoopControlInfo{
		{ID: "loop-abc", Task: "improve the app", Status: "running"},
		{ID: "loop-old", Task: "done thing", Status: "stopped"},
	}, &log)

	res, err := tool.Execute(context.Background(), map[string]any{"action": "pause"})
	if err != nil || !res.Success {
		t.Fatalf("pause without id must succeed for the sole running loop: %v / %+v", err, res)
	}
	if len(log) != 1 || log[0] != "pause:loop-abc" {
		t.Fatalf("expected pause:loop-abc, got %v", log)
	}
}

// TestLoopControl_AmbiguousRequiresID: several eligible loops → the tool must
// NOT guess; it lists candidates as an error the model relays.
func TestLoopControl_AmbiguousRequiresID(t *testing.T) {
	var log []string
	tool := wiredLoopControl([]LoopControlInfo{
		{ID: "loop-a", Task: "one", Status: "running"},
		{ID: "loop-b", Task: "two", Status: "running"},
	}, &log)

	res, _ := tool.Execute(context.Background(), map[string]any{"action": "stop"})
	if res.Success {
		t.Fatal("ambiguous stop must not succeed")
	}
	if len(log) != 0 {
		t.Fatalf("no operation may run on ambiguity, got %v", log)
	}
	if !strings.Contains(res.Error, "loop-a") || !strings.Contains(res.Error, "loop-b") {
		t.Fatalf("error must list the candidates, got %q", res.Error)
	}
}

// TestLoopControl_ListAndExplicitID: list renders every loop; an explicit id
// bypasses eligibility resolution.
func TestLoopControl_ListAndExplicitID(t *testing.T) {
	var log []string
	tool := wiredLoopControl([]LoopControlInfo{
		{ID: "loop-x", Task: "watch deploy", Status: "paused"},
	}, &log)

	res, _ := tool.Execute(context.Background(), map[string]any{"action": "list"})
	if !res.Success || !strings.Contains(res.Content, "loop-x") || !strings.Contains(res.Content, "paused") {
		t.Fatalf("list must show id+status, got %+v", res)
	}

	res, _ = tool.Execute(context.Background(), map[string]any{"action": "resume", "id": "loop-x"})
	if !res.Success || len(log) != 1 || log[0] != "resume:loop-x" {
		t.Fatalf("explicit-id resume failed: %+v / %v", res, log)
	}
}

// TestLoopControl_UnwiredIsHonest: without builder wiring the tool must say
// so, never pretend.
func TestLoopControl_UnwiredIsHonest(t *testing.T) {
	tool := NewLoopControlTool()
	res, _ := tool.Execute(context.Background(), map[string]any{"action": "list"})
	if res.Success || !strings.Contains(res.Error, "not wired") {
		t.Fatalf("unwired tool must return the setup hint, got %+v", res)
	}
}
