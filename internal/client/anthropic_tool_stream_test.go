package client

import (
	"errors"
	"io"
	"testing"
)

func TestProcessStreamEvent_ToolUseStartCarriesInput(t *testing.T) {
	c := &AnthropicClient{}
	acc := &toolCallAccumulator{}

	c.processStreamEvent(map[string]any{
		"type": "content_block_start",
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    "call_1",
			"name":  "read",
			"input": map[string]any{"path": "main.go"},
		},
	}, acc)
	chunk := c.processStreamEvent(map[string]any{"type": "content_block_stop"}, acc)

	if len(chunk.FunctionCalls) != 0 {
		t.Fatalf("content_block_stop should not directly emit calls; got %d", len(chunk.FunctionCalls))
	}
	if len(acc.completedCalls) != 1 {
		t.Fatalf("completed calls = %d, want 1", len(acc.completedCalls))
	}
	call := acc.completedCalls[0]
	if call.Name != "read" || call.ID != "call_1" {
		t.Fatalf("call = %#v", call)
	}
	if call.Args["path"] != "main.go" {
		t.Fatalf("path arg = %#v, want main.go", call.Args["path"])
	}
}

// TestProcessStreamEvent_ToolUseCanonicalEmptyInputThenDeltas covers the
// canonical Anthropic streaming protocol: content_block_start carries an EMPTY
// input placeholder ({}) and the real arguments arrive via input_json_delta.
// Regression guard: appending the "{}" placeholder used to concatenate with the
// deltas into "{}{...}" — invalid JSON that unmarshaled to empty args, breaking
// every streamed tool call for all AnthropicClient-compat providers.
func TestProcessStreamEvent_ToolUseCanonicalEmptyInputThenDeltas(t *testing.T) {
	c := &AnthropicClient{}
	acc := &toolCallAccumulator{}

	c.processStreamEvent(map[string]any{
		"type": "content_block_start",
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    "call_3",
			"name":  "edit",
			"input": map[string]any{}, // canonical empty placeholder
		},
	}, acc)
	// Real args streamed in fragments.
	c.processStreamEvent(map[string]any{
		"type":  "content_block_delta",
		"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"file_path":"main.go",`},
	}, acc)
	c.processStreamEvent(map[string]any{
		"type":  "content_block_delta",
		"delta": map[string]any{"type": "input_json_delta", "partial_json": `"old_string":"a","new_string":"b"}`},
	}, acc)
	c.processStreamEvent(map[string]any{"type": "content_block_stop"}, acc)

	if len(acc.completedCalls) != 1 {
		t.Fatalf("completed calls = %d, want 1", len(acc.completedCalls))
	}
	call := acc.completedCalls[0]
	if call.Name != "edit" || call.ID != "call_3" {
		t.Fatalf("call = %#v", call)
	}
	if call.Args["file_path"] != "main.go" || call.Args["old_string"] != "a" || call.Args["new_string"] != "b" {
		t.Fatalf("args lost the streamed values: %#v", call.Args)
	}
}

func TestProcessStreamEvent_ToolUseDeltaWithoutTypeCarriesPartialJSON(t *testing.T) {
	c := &AnthropicClient{}
	acc := &toolCallAccumulator{}

	c.processStreamEvent(map[string]any{
		"type": "content_block_start",
		"content_block": map[string]any{
			"type": "tool_use",
			"id":   "call_2",
			"name": "bash",
		},
	}, acc)
	c.processStreamEvent(map[string]any{
		"type":  "content_block_delta",
		"delta": map[string]any{"partial_json": `{"command":"go test ./internal/client"}`},
	}, acc)
	c.processStreamEvent(map[string]any{"type": "content_block_stop"}, acc)

	if len(acc.completedCalls) != 1 {
		t.Fatalf("completed calls = %d, want 1", len(acc.completedCalls))
	}
	call := acc.completedCalls[0]
	if call.Name != "bash" {
		t.Fatalf("call name = %q, want bash", call.Name)
	}
	if call.Args["command"] != "go test ./internal/client" {
		t.Fatalf("command arg = %#v", call.Args["command"])
	}
}

func TestProcessStreamEvent_MalformedToolInputFailsClosed(t *testing.T) {
	c := &AnthropicClient{}
	acc := &toolCallAccumulator{}

	c.processStreamEvent(map[string]any{
		"type": "content_block_start",
		"content_block": map[string]any{
			"type": "tool_use",
			"id":   "call_bad",
			"name": "edit",
		},
	}, acc)
	c.processStreamEvent(map[string]any{
		"type":  "content_block_delta",
		"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"file_path":"main.go"`},
	}, acc)
	chunk := c.processStreamEvent(map[string]any{"type": "content_block_stop"}, acc)

	if !chunk.Done || !errors.Is(chunk.Error, io.ErrUnexpectedEOF) {
		t.Fatalf("chunk error/done = %v/%v, want retryable malformed-stream failure", chunk.Error, chunk.Done)
	}
	if len(acc.completedCalls) != 0 || len(chunk.FunctionCalls) != 0 {
		t.Fatalf("malformed tool input escaped as executable call: acc=%#v chunk=%#v", acc.completedCalls, chunk.FunctionCalls)
	}
	if acc.currentToolID != "" || acc.currentToolName != "" || acc.currentToolInput.Len() != 0 {
		t.Fatalf("tool accumulator was not reset after malformed input: %#v", acc)
	}
}

func TestProcessStreamEvent_NullToolInputFailsClosed(t *testing.T) {
	c := &AnthropicClient{}
	acc := &toolCallAccumulator{}
	c.processStreamEvent(map[string]any{
		"type": "content_block_start",
		"content_block": map[string]any{
			"type": "tool_use", "id": "call_null", "name": "write",
		},
	}, acc)
	c.processStreamEvent(map[string]any{
		"type":  "content_block_delta",
		"delta": map[string]any{"type": "input_json_delta", "partial_json": "null"},
	}, acc)
	chunk := c.processStreamEvent(map[string]any{"type": "content_block_stop"}, acc)

	if !chunk.Done || !errors.Is(chunk.Error, io.ErrUnexpectedEOF) {
		t.Fatalf("chunk error/done = %v/%v, want fail-closed malformed input", chunk.Error, chunk.Done)
	}
	if len(acc.completedCalls) != 0 {
		t.Fatalf("null input escaped as executable call: %#v", acc.completedCalls)
	}
}

func TestProcessStreamEvent_NonStringPartialJSONFailsClosed(t *testing.T) {
	c := &AnthropicClient{}
	acc := &toolCallAccumulator{}
	c.processStreamEvent(map[string]any{
		"type": "content_block_start",
		"content_block": map[string]any{
			"type": "tool_use", "id": "call_wrong_type", "name": "bash",
		},
	}, acc)
	chunk := c.processStreamEvent(map[string]any{
		"type":  "content_block_delta",
		"delta": map[string]any{"type": "input_json_delta", "partial_json": map[string]any{"command": "date"}},
	}, acc)

	if !chunk.Done || !errors.Is(chunk.Error, io.ErrUnexpectedEOF) {
		t.Fatalf("chunk error/done = %v/%v, want fail-closed schema error", chunk.Error, chunk.Done)
	}
	if len(acc.completedCalls) != 0 {
		t.Fatalf("wrong-typed input escaped as executable call: %#v", acc.completedCalls)
	}
}

func TestProcessStreamEvent_OverlappingBlocksFailClosed(t *testing.T) {
	c := &AnthropicClient{}
	acc := &toolCallAccumulator{}
	c.processStreamEvent(map[string]any{
		"type": "content_block_start",
		"content_block": map[string]any{
			"type": "tool_use", "id": "call_first", "name": "edit",
		},
	}, acc)
	chunk := c.processStreamEvent(map[string]any{
		"type": "content_block_start",
		"content_block": map[string]any{
			"type": "text",
		},
	}, acc)

	if !chunk.Done || !errors.Is(chunk.Error, io.ErrUnexpectedEOF) {
		t.Fatalf("chunk error/done = %v/%v, want fail-closed overlapping blocks", chunk.Error, chunk.Done)
	}
	if len(acc.completedCalls) != 0 {
		t.Fatalf("overlapped tool escaped as executable call: %#v", acc.completedCalls)
	}
}

func TestProcessStreamEvent_TerminalDeltaFlushesPartialThinkTag(t *testing.T) {
	c := &AnthropicClient{}
	acc := &toolCallAccumulator{}
	c.processStreamEvent(map[string]any{
		"type":          "content_block_start",
		"content_block": map[string]any{"type": "text"},
	}, acc)
	first := c.processStreamEvent(map[string]any{
		"type":  "content_block_delta",
		"delta": map[string]any{"type": "text_delta", "text": "answer<thi"},
	}, acc)
	if first.Text != "answer" {
		t.Fatalf("initial text = %q, want answer with partial tag buffered", first.Text)
	}
	// A well-formed stream normally sends content_block_stop before
	// message_delta; keep the parser buffer intact while closing the block.
	c.processStreamEvent(map[string]any{"type": "content_block_stop"}, acc)
	terminal := c.processStreamEvent(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn"},
	}, acc)
	if !terminal.Done || terminal.Text != "<thi" {
		t.Fatalf("terminal chunk = done:%v text:%q, want buffered partial tag", terminal.Done, terminal.Text)
	}
}
