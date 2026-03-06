package client

import (
	"testing"
)

func TestParseToolCallsFromText_CodeBlock(t *testing.T) {
	text := "Let me read the file.\n```json\n{\"tool\": \"read\", \"args\": {\"path\": \"/tmp/test.go\"}}\n```"
	calls := ParseToolCallsFromText(text)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].Name != "read" {
		t.Errorf("Name = %q, want read", calls[0].Name)
	}
	if calls[0].Args["path"] != "/tmp/test.go" {
		t.Errorf("Args[path] = %v", calls[0].Args["path"])
	}
}

func TestParseToolCallsFromText_BareJSON(t *testing.T) {
	text := `I'll edit the file. {"tool": "write", "args": {"path": "/tmp/out.go", "content": "package main"}}`
	calls := ParseToolCallsFromText(text)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].Name != "write" {
		t.Errorf("Name = %q, want write", calls[0].Name)
	}
}

func TestParseToolCallsFromText_NameField(t *testing.T) {
	text := `{"name": "glob", "args": {"pattern": "*.go"}}`
	calls := ParseToolCallsFromText(text)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].Name != "glob" {
		t.Errorf("Name = %q, want glob", calls[0].Name)
	}
}

func TestParseToolCallsFromText_Multiple(t *testing.T) {
	text := "```json\n{\"tool\": \"read\", \"args\": {}}\n```\nsome text\n```json\n{\"tool\": \"write\", \"args\": {}}\n```"
	calls := ParseToolCallsFromText(text)
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
	if calls[0].Name != "read" {
		t.Errorf("calls[0].Name = %q, want read", calls[0].Name)
	}
	if calls[1].Name != "write" {
		t.Errorf("calls[1].Name = %q, want write", calls[1].Name)
	}
}

func TestParseToolCallsFromText_Empty(t *testing.T) {
	if calls := ParseToolCallsFromText(""); calls != nil {
		t.Errorf("empty text should return nil, got %v", calls)
	}
}

func TestParseToolCallsFromText_NoToolCalls(t *testing.T) {
	if calls := ParseToolCallsFromText("just regular text without any JSON"); calls != nil {
		t.Errorf("no tool calls should return nil, got %v", calls)
	}
}

func TestParseToolCallsFromText_InvalidJSON(t *testing.T) {
	text := `{"tool": "read", "args": {INVALID}}`
	calls := ParseToolCallsFromText(text)
	if len(calls) != 0 {
		t.Errorf("invalid JSON should return 0 calls, got %d", len(calls))
	}
}

func TestParseToolCallsFromText_NoToolName(t *testing.T) {
	text := `{"args": {"path": "/tmp"}}`
	calls := ParseToolCallsFromText(text)
	if len(calls) != 0 {
		t.Errorf("no tool name should return 0 calls, got %d", len(calls))
	}
}

func TestParseToolCallsFromText_NilArgs(t *testing.T) {
	text := `{"tool": "list_dir"}`
	calls := ParseToolCallsFromText(text)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].Args == nil {
		t.Error("Args should be initialized to empty map, not nil")
	}
}

func TestFindJSONObjects(t *testing.T) {
	text := `prefix {"tool": "read", "args": {"nested": {"key": "val"}}} suffix`
	objects := findJSONObjects(text)
	if len(objects) != 1 {
		t.Fatalf("got %d objects, want 1", len(objects))
	}

	// Nested braces should be handled
	text = `{"tool": "bash", "args": {"command": "echo '{test}'"}}`
	objects = findJSONObjects(text)
	if len(objects) != 1 {
		t.Fatalf("got %d objects, want 1 (nested braces in strings)", len(objects))
	}
}

func TestFindJSONObjects_UnmatchedBrace(t *testing.T) {
	text := `{"tool": "read" some garbage without closing brace`
	objects := findJSONObjects(text)
	if len(objects) != 0 {
		t.Errorf("unmatched brace should return 0, got %d", len(objects))
	}
}

func TestNextTextToolCallID(t *testing.T) {
	id := nextTextToolCallID("read")
	if id == "" {
		t.Error("should generate non-empty ID")
	}
	if !containsLower(id, "read") {
		t.Errorf("ID %q should contain tool name", id)
	}

	// Special characters should be sanitized
	id = nextTextToolCallID("tool/with/slashes")
	if containsLower(id, "/") {
		t.Errorf("ID %q should not contain slashes", id)
	}
}
