package client

import (
	"testing"

	"google.golang.org/genai"
)

// fakeFallbackClient is a minimal stand-in for a client that does (or does not)
// need the text-based tool-call fallback (real impl: OllamaClient).
type fakeFallbackClient struct{ needs bool }

func (f fakeFallbackClient) NeedsToolCallFallback() bool { return f.needs }

func TestApplyTextToolCallFallback(t *testing.T) {
	const blob = "Let me explore.\n```json\n{\"tool\": \"glob\", \"args\": {\"pattern\": \"**/*.py\"}}\n```"

	// Client needs fallback + text carries a tool call → parsed + text stripped.
	resp := &Response{Text: blob}
	if n := ApplyTextToolCallFallback(fakeFallbackClient{needs: true}, resp); n != 1 {
		t.Fatalf("parsed = %d, want 1", n)
	}
	if len(resp.FunctionCalls) != 1 || resp.FunctionCalls[0].Name != "glob" {
		t.Fatalf("FunctionCalls = %+v, want one glob call", resp.FunctionCalls)
	}
	if resp.Text != "" {
		t.Fatalf("text must be stripped after a tool call is parsed, got %q", resp.Text)
	}

	// Client exists but does NOT need fallback → no-op (text preserved).
	noFb := &Response{Text: blob}
	if n := ApplyTextToolCallFallback(fakeFallbackClient{needs: false}, noFb); n != 0 || len(noFb.FunctionCalls) != 0 || noFb.Text == "" {
		t.Fatalf("non-fallback client must be a no-op: n=%d calls=%d text=%q", n, len(noFb.FunctionCalls), noFb.Text)
	}

	// Client without the capability at all → no-op.
	plain := &Response{Text: blob}
	if n := ApplyTextToolCallFallback(struct{}{}, plain); n != 0 || plain.Text == "" {
		t.Fatalf("client lacking NeedsToolCallFallback must be a no-op: n=%d", n)
	}

	// Native function calls already present → never re-parse / never strip text.
	native := &Response{Text: blob, FunctionCalls: []*genai.FunctionCall{{Name: "read"}}}
	if n := ApplyTextToolCallFallback(fakeFallbackClient{needs: true}, native); n != 0 {
		t.Fatalf("must not touch a response that already has native calls: n=%d", n)
	}
	if len(native.FunctionCalls) != 1 || native.FunctionCalls[0].Name != "read" || native.Text == "" {
		t.Fatalf("existing native call + text must be preserved, got %+v text=%q", native.FunctionCalls, native.Text)
	}

	// Needs fallback but text has no tool call → no-op.
	prose := &Response{Text: "just some thoughts, no tools here"}
	if n := ApplyTextToolCallFallback(fakeFallbackClient{needs: true}, prose); n != 0 || prose.Text == "" {
		t.Fatalf("prose without a tool call must be a no-op: n=%d", n)
	}

	// nil response → no panic, no-op.
	if n := ApplyTextToolCallFallback(fakeFallbackClient{needs: true}, nil); n != 0 {
		t.Fatalf("nil response must be a no-op: n=%d", n)
	}
}

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

func TestParseToolCallsFromText_JSONArray(t *testing.T) {
	text := "```json\n[{\"tool\":\"read\",\"args\":{\"path\":\"a.go\"}},{\"tool\":\"grep\",\"args\":{\"pattern\":\"TODO\"}}]\n```"
	calls := ParseToolCallsFromText(text)
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
	if calls[0].Name != "read" || calls[0].Args["path"] != "a.go" {
		t.Fatalf("first call = %#v", calls[0])
	}
	if calls[1].Name != "grep" || calls[1].Args["pattern"] != "TODO" {
		t.Fatalf("second call = %#v", calls[1])
	}
}

func TestParseToolCallsFromText_AnthropicToolUse(t *testing.T) {
	text := "```json\n{\"type\":\"tool_use\",\"name\":\"edit\",\"input\":{\"file_path\":\"main.go\",\"old_string\":\"a\",\"new_string\":\"b\"}}\n```"
	calls := ParseToolCallsFromText(text)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].Name != "edit" {
		t.Fatalf("Name = %q, want edit", calls[0].Name)
	}
	if calls[0].Args["file_path"] != "main.go" {
		t.Fatalf("Args[file_path] = %v", calls[0].Args["file_path"])
	}
}

func TestParseToolCallsFromText_OpenAIToolCalls(t *testing.T) {
	text := `{"tool_calls":[{"type":"function","function":{"name":"bash","arguments":"{\"command\":\"go test ./...\"}"}}]}`
	calls := ParseToolCallsFromText(text)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].Name != "bash" {
		t.Fatalf("Name = %q, want bash", calls[0].Name)
	}
	if calls[0].Args["command"] != "go test ./..." {
		t.Fatalf("Args[command] = %v", calls[0].Args["command"])
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
