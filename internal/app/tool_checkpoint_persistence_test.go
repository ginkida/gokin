package app

import (
	"encoding/json"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

func TestToolCheckpointPersistencePreservesOutcome(t *testing.T) {
	executor := tools.NewExecutor(tools.NewRegistry(), nil, time.Second)
	application := &App{session: chat.NewSession(), executor: executor}
	call := &genai.FunctionCall{
		ID: "write-1", Name: "write", Args: map[string]any{"file_path": "x.go"},
	}
	want := tools.ToolResult{
		Success: false,
		Content: "partial write completed",
		Error:   "provider disconnected after tool execution",
		Data:    map[string]any{"bytes": float64(17)},
	}
	executor.GetCheckpointJournal().Record(call, want)

	application.syncToolCheckpoints()
	executor.GetCheckpointJournal().Clear()
	application.restoreToolCheckpoints()
	executor.GetCheckpointJournal().BeginReplay()

	got, _, ok := executor.GetCheckpointJournal().ConsumeReplay(call)
	if !ok {
		t.Fatal("persisted checkpoint was not replayable")
	}
	if got.Success != want.Success || got.Content != want.Content || got.Error != want.Error {
		t.Fatalf("restored outcome = %+v, want success=%v content=%q error=%q",
			got, want.Success, want.Content, want.Error)
	}
	data, ok := got.Data.(map[string]any)
	bytes, numberOK := data["bytes"].(json.Number)
	if !ok || !numberOK || bytes.String() != "17" {
		t.Fatalf("restored data = %#v, want JSON object", got.Data)
	}
}

func TestLegacyToolCheckpointRestoresFailSafeAsCompleted(t *testing.T) {
	journal := tools.NewCheckpointJournal()
	call := &genai.FunctionCall{
		ID: "legacy-write", Name: "write", Args: map[string]any{"file_path": "legacy.go"},
	}
	journal.RecordSerialized(call.ID, call.Name, call.Args, "legacy write output", "", time.Now())
	journal.BeginReplay()

	got, _, ok := journal.ConsumeReplay(call)
	if !ok {
		t.Fatal("legacy checkpoint was not replayable")
	}
	if !got.Success || got.Content != "legacy write output" {
		t.Fatalf("legacy outcome = %+v, want completed replay", got)
	}
}
