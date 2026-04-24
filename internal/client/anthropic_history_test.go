package client

import (
	"testing"

	"google.golang.org/genai"
)

func TestConvertHistoryToMessages_SkipsUnsignedThinkingOnlyAssistantTurns(t *testing.T) {
	c := &AnthropicClient{}

	history := []*genai.Content{
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{Thought: true, Text: "private reasoning without signature"},
			},
		},
	}

	messages := c.convertHistoryToMessages(history, "follow up")
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}
	if role := stringFromMap(messages[0], "role"); role != "user" {
		t.Fatalf("role = %q, want user", role)
	}

	content := extractContentArray(messages[0]["content"])
	if len(content) != 1 {
		t.Fatalf("content len = %d, want 1", len(content))
	}
	if got := stringFromMap(content[0], "text"); got != "follow up" {
		t.Errorf("text = %q, want %q", got, "follow up")
	}
}

func TestBuildAssistantMessage_DropsUnsignedThinkingButKeepsToolUse(t *testing.T) {
	c := &AnthropicClient{}

	msg := c.buildAssistantMessage([]*genai.Part{
		{Thought: true, Text: "unsigned reasoning"},
		{FunctionCall: &genai.FunctionCall{
			ID:   "tool_1",
			Name: "read",
			Args: map[string]any{"path": "/tmp/x.go"},
		}},
	})

	content := extractContentArray(msg["content"])
	if len(content) != 1 {
		t.Fatalf("content len = %d, want 1", len(content))
	}
	if got := stringFromMap(content[0], "type"); got != "tool_use" {
		t.Fatalf("content[0].type = %q, want tool_use", got)
	}
	if got := stringFromMap(content[0], "id"); got != "tool_1" {
		t.Errorf("tool id = %q, want tool_1", got)
	}
}

func TestBuildAssistantMessage_EmitsSignedThinkingBeforeToolUse(t *testing.T) {
	c := &AnthropicClient{}

	msg := c.buildAssistantMessage([]*genai.Part{
		{
			Thought:          true,
			Text:             "reason first",
			ThoughtSignature: []byte("sig-123"),
		},
		{FunctionCall: &genai.FunctionCall{
			ID:   "tool_2",
			Name: "glob",
			Args: map[string]any{"pattern": "*.go"},
		}},
	})

	content := extractContentArray(msg["content"])
	if len(content) != 2 {
		t.Fatalf("content len = %d, want 2", len(content))
	}
	if got := stringFromMap(content[0], "type"); got != "thinking" {
		t.Fatalf("content[0].type = %q, want thinking", got)
	}
	if got := stringFromMap(content[0], "signature"); got != "sig-123" {
		t.Errorf("signature = %q, want sig-123", got)
	}
	if got := stringFromMap(content[1], "type"); got != "tool_use" {
		t.Fatalf("content[1].type = %q, want tool_use", got)
	}
}

func TestConvertHistoryWithResults_OrdersToolResultBeforeNotificationText(t *testing.T) {
	c := &AnthropicClient{}

	history := []*genai.Content{
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{
					ID:   "tool_1",
					Name: "read",
					Args: map[string]any{"file_path": "main.go"},
				},
			}},
		},
		{
			Role:  genai.RoleUser,
			Parts: []*genai.Part{genai.NewPartFromText("[System: reuse tool results]")},
		},
	}
	results := []*genai.FunctionResponse{{
		ID:       "tool_1",
		Name:     "read",
		Response: map[string]any{"content": "package main"},
	}}

	messages, _ := c.convertHistoryWithResultsAndSystem(history, results)
	if len(messages) != 2 {
		t.Fatalf("messages len = %d, want 2: %+v", len(messages), messages)
	}
	if role := stringFromMap(messages[1], "role"); role != "user" {
		t.Fatalf("role = %q, want user", role)
	}

	content := extractContentArray(messages[1]["content"])
	if len(content) != 2 {
		t.Fatalf("content len = %d, want 2: %+v", len(content), content)
	}
	if got := stringFromMap(content[0], "type"); got != "tool_result" {
		t.Fatalf("content[0].type = %q, want tool_result; content=%+v", got, content)
	}
	if got := stringFromMap(content[1], "type"); got != "text" {
		t.Fatalf("content[1].type = %q, want text; content=%+v", got, content)
	}
}
