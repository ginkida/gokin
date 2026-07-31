package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/tasks"
)

func TestTaskOutputShellOffsetZeroReadsPersistentLog(t *testing.T) {
	manager := tasks.NewManager(t.TempDir())
	id, err := manager.Start(context.Background(), "printf 'first\\nsecond\\n'")
	if err != nil {
		t.Fatal(err)
	}
	task, ok := manager.Get(id)
	if !ok {
		t.Fatal("started task missing from manager")
	}
	<-task.Done()

	tool := NewTaskOutputTool()
	tool.SetManager(manager)
	result, err := tool.Execute(context.Background(), map[string]any{
		"task_id": id,
		"offset":  0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || !strings.Contains(result.Content, "(incremental read)") ||
		!strings.Contains(result.Content, "first\nsecond") {
		t.Fatalf("offset=0 did not read the persistent shell log: %+v", result)
	}
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("result data type = %T", result.Data)
	}
	if got, ok := data["next_offset"].(int64); !ok || got <= 0 {
		t.Fatalf("next_offset = %#v, want positive int64", data["next_offset"])
	}
}

func TestTaskOutputRejectsNegativeOffset(t *testing.T) {
	tool := NewTaskOutputTool()
	if err := tool.Validate(map[string]any{"task_id": "task_1", "offset": -1}); err == nil {
		t.Fatal("negative output offset passed validation")
	}
}

func TestTaskOutputRunningAgentReadsLiveTranscriptWithoutExplicitOffset(t *testing.T) {
	const agentID = "715ca37352122226"
	outputFile := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(outputFile, []byte("cargo test: crate 4/10\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &stubAgentRunner{
		known: []string{agentID},
		results: map[string]AgentResult{
			agentID: {
				AgentID:    agentID,
				Type:       "bash",
				Status:     "running",
				Completed:  false,
				OutputFile: outputFile,
			},
		},
	}
	tool := NewTaskOutputTool()
	tool.SetRunner(runner)

	result, err := tool.Execute(context.Background(), map[string]any{"task_id": agentID})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || !strings.Contains(result.Content, "cargo test: crate 4/10") {
		t.Fatalf("default live agent read did not return transcript: %+v", result)
	}
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("result data type = %T", result.Data)
	}
	if got := data["next_offset"]; got != int64(len("cargo test: crate 4/10\n")) {
		t.Fatalf("next_offset = %#v", got)
	}

	blocked, err := tool.Execute(context.Background(), map[string]any{
		"task_id":    agentID,
		"block":      true,
		"timeout_ms": 1, // clamped to the tool's 100ms minimum
	})
	if err != nil {
		t.Fatal(err)
	}
	if !blocked.Success || !strings.Contains(blocked.Content, "Timeout waiting") ||
		!strings.Contains(blocked.Content, "cargo test: crate 4/10") {
		t.Fatalf("blocking timeout lost the live transcript: %+v", blocked)
	}
	blockedData, ok := blocked.Data.(map[string]any)
	if !ok || blockedData["timeout"] != true {
		t.Fatalf("blocking timeout metadata = %#v", blocked.Data)
	}
}
