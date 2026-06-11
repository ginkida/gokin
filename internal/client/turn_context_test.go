package client

import (
	"strings"
	"testing"
)

// TestAppendTurnContextBlock pins the roadmap-#7 delivery contract: turn
// context lands as an extra text block on the FINAL user-role message — and
// nowhere else — so the cached prefix stays byte-stable while working memory
// changes every turn.
func TestAppendTurnContextBlock(t *testing.T) {
	// String content gets normalized to a block array.
	msgs := []map[string]any{
		{"role": "user", "content": "old question"},
		{"role": "assistant", "content": "answer"},
		{"role": "user", "content": "new question"},
	}
	out := appendTurnContextBlock(msgs, "WM SNAPSHOT")
	last := out[len(out)-1]
	blocks, ok := last["content"].([]map[string]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("expected 2 blocks on last message, got %T %v", last["content"], last["content"])
	}
	if blocks[0]["text"] != "new question" {
		t.Fatalf("original text must be preserved first, got %v", blocks[0])
	}
	txt, _ := blocks[1]["text"].(string)
	if !strings.Contains(txt, "WM SNAPSHOT") || !strings.Contains(txt, "<turn-context>") {
		t.Fatalf("turn-context block malformed: %q", txt)
	}
	// Earlier messages untouched (prefix stability).
	if msgs[0]["content"] != "old question" {
		t.Fatalf("history message mutated: %v", msgs[0])
	}

	// Block-array content gets appended to.
	msgs = []map[string]any{
		{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "x", "content": "ok"}}},
	}
	out = appendTurnContextBlock(msgs, "CTX")
	blocks = out[0]["content"].([]map[string]any)
	if len(blocks) != 2 || blocks[1]["type"] != "text" {
		t.Fatalf("expected appended text block after tool_result, got %v", blocks)
	}

	// Non-user final message: untouched.
	msgs = []map[string]any{{"role": "assistant", "content": "a"}}
	out = appendTurnContextBlock(msgs, "CTX")
	if out[0]["content"] != "a" {
		t.Fatalf("assistant-final message must not be touched: %v", out[0])
	}

	// Empty context: untouched.
	msgs = []map[string]any{{"role": "user", "content": "q"}}
	out = appendTurnContextBlock(msgs, "")
	if out[0]["content"] != "q" {
		t.Fatalf("empty context must be a no-op: %v", out[0])
	}
}
