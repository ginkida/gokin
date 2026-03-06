package tools

import (
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestCheckpointJournalRecordAndLookup(t *testing.T) {
	j := NewCheckpointJournal()

	call := &genai.FunctionCall{
		ID:   "call-1",
		Name: "write",
		Args: map[string]any{"file_path": "/tmp/test.go", "content": "package main"},
	}
	result := ToolResult{Content: "File written", Success: true}

	j.Record(call, result)

	if j.Len() != 1 {
		t.Errorf("Len() = %d, want 1", j.Len())
	}

	// Lookup by call ID
	got, reason, ok := j.Lookup(call)
	if !ok {
		t.Fatal("should find by call ID")
	}
	if reason != "checkpoint_call_id" {
		t.Errorf("reason = %q, want checkpoint_call_id", reason)
	}
	if got.Content != "File written" {
		t.Errorf("content = %q", got.Content)
	}
}

func TestCheckpointJournalLookupBySignature(t *testing.T) {
	j := NewCheckpointJournal()

	call := &genai.FunctionCall{
		Name: "edit",
		Args: map[string]any{"file_path": "/tmp/test.go", "old_string": "a", "new_string": "b"},
	}
	result := ToolResult{Content: "Edited", Success: true}

	j.Record(call, result)

	// Lookup with a different call object but same args (no call ID)
	call2 := &genai.FunctionCall{
		Name: "edit",
		Args: map[string]any{"file_path": "/tmp/test.go", "old_string": "a", "new_string": "b"},
	}
	got, reason, ok := j.Lookup(call2)
	if !ok {
		t.Fatal("should find by signature")
	}
	if reason != "checkpoint_signature" {
		t.Errorf("reason = %q, want checkpoint_signature", reason)
	}
	if got.Content != "Edited" {
		t.Errorf("content = %q", got.Content)
	}
}

func TestCheckpointJournalLookupNotFound(t *testing.T) {
	j := NewCheckpointJournal()

	call := &genai.FunctionCall{
		ID:   "call-99",
		Name: "write",
		Args: map[string]any{"file_path": "/tmp/other.go"},
	}

	_, _, ok := j.Lookup(call)
	if ok {
		t.Error("should not find non-recorded call")
	}
}

func TestCheckpointJournalNilCall(t *testing.T) {
	j := NewCheckpointJournal()

	// Record nil should not panic
	j.Record(nil, ToolResult{})
	if j.Len() != 0 {
		t.Error("nil record should be ignored")
	}

	// Lookup nil should not panic
	_, _, ok := j.Lookup(nil)
	if ok {
		t.Error("nil lookup should return false")
	}
}

func TestCheckpointJournalClear(t *testing.T) {
	j := NewCheckpointJournal()

	call := &genai.FunctionCall{ID: "c1", Name: "bash", Args: map[string]any{"command": "ls"}}
	j.Record(call, ToolResult{Content: "file.go", Success: true})

	if j.Len() != 1 {
		t.Fatal("should have 1 entry")
	}

	j.Clear()
	if j.Len() != 0 {
		t.Errorf("after clear, Len() = %d", j.Len())
	}

	_, _, ok := j.Lookup(call)
	if ok {
		t.Error("should not find after clear")
	}
}

func TestCheckpointJournalEntries(t *testing.T) {
	j := NewCheckpointJournal()

	j.Record(&genai.FunctionCall{ID: "c1", Name: "write", Args: map[string]any{}}, ToolResult{Success: true})
	j.Record(&genai.FunctionCall{ID: "c2", Name: "edit", Args: map[string]any{}}, ToolResult{Success: true})

	entries := j.Entries()
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}

	// Entries should be a copy
	entries[0].ToolName = "modified"
	orig := j.Entries()
	if orig[0].ToolName == "modified" {
		t.Error("Entries should return a copy")
	}
}

func TestCheckpointJournalMultipleEntries(t *testing.T) {
	j := NewCheckpointJournal()

	for i := 0; i < 50; i++ {
		call := &genai.FunctionCall{
			ID:   "call-" + string(rune('A'+i%26)),
			Name: "write",
			Args: map[string]any{"file_path": "/tmp/test" + string(rune('A'+i%26)) + ".go"},
		}
		j.Record(call, ToolResult{Content: "ok", Success: true})
	}

	if j.Len() != 50 {
		t.Errorf("Len() = %d, want 50", j.Len())
	}
}

func TestCheckpointJournalRecordSerialized(t *testing.T) {
	j := NewCheckpointJournal()

	sig := checkpointSignature("write", map[string]any{"file_path": "/tmp/restored.go"})
	j.RecordSerialized("call-restored", "write", map[string]any{"file_path": "/tmp/restored.go"}, "File written", sig, time.Now())

	if j.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", j.Len())
	}

	// Should be findable by call ID
	call := &genai.FunctionCall{
		ID:   "call-restored",
		Name: "write",
		Args: map[string]any{"file_path": "/tmp/restored.go"},
	}
	result, reason, ok := j.Lookup(call)
	if !ok {
		t.Fatal("should find restored checkpoint by call ID")
	}
	if reason != "checkpoint_call_id" {
		t.Errorf("reason = %q, want checkpoint_call_id", reason)
	}
	if result.Content != "File written" {
		t.Errorf("restored result = %q, want %q", result.Content, "File written")
	}

	// Should also be findable by signature
	call2 := &genai.FunctionCall{
		Name: "write",
		Args: map[string]any{"file_path": "/tmp/restored.go"},
	}
	_, reason2, ok := j.Lookup(call2)
	if !ok {
		t.Fatal("should find restored checkpoint by signature")
	}
	if reason2 != "checkpoint_signature" {
		t.Errorf("reason = %q, want checkpoint_signature", reason2)
	}
}

func TestCheckpointSignature(t *testing.T) {
	sig1 := checkpointSignature("write", map[string]any{"path": "/tmp/a.go"})
	sig2 := checkpointSignature("write", map[string]any{"path": "/tmp/a.go"})
	sig3 := checkpointSignature("write", map[string]any{"path": "/tmp/b.go"})
	sig4 := checkpointSignature("edit", map[string]any{"path": "/tmp/a.go"})

	if sig1 != sig2 {
		t.Error("same tool+args should have same signature")
	}
	if sig1 == sig3 {
		t.Error("different args should have different signature")
	}
	if sig1 == sig4 {
		t.Error("different tool should have different signature")
	}

	empty := checkpointSignature("", nil)
	if empty != "" {
		t.Error("empty tool name should return empty signature")
	}
}
