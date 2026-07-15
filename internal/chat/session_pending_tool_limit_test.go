package chat

import (
	"fmt"
	"testing"

	"google.golang.org/genai"
)

func TestSessionHardLimitPreservesNewestPendingToolCallUntilResponse(t *testing.T) {
	tests := []struct {
		name string
		add  func(*Session, *genai.Content)
	}{
		{
			name: "AddContent",
			add: func(session *Session, content *genai.Content) {
				session.AddContent(content)
			},
		},
		{
			name: "AddContentWithTokens",
			add: func(session *Session, content *genai.Content) {
				session.AddContentWithTokens(content, 1)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := NewSession()
			for index := 0; index < MaxMessages; index++ {
				session.AddContent(genai.NewContentFromText(
					fmt.Sprintf("old-%03d", index),
					genai.RoleUser,
				))
			}

			const callID = "pending-at-hard-limit"
			call := &genai.Content{
				Role: string(genai.RoleModel),
				Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
					ID:   callID,
					Name: "read",
					Args: map[string]any{"path": "README.md"},
				}}},
			}
			test.add(session, call)

			history := session.GetHistory()
			if len(history) > MaxMessages {
				t.Errorf("history len with pending call = %d, exceeds %d", len(history), MaxMessages)
			}
			calls, responses := pendingToolIDCounts(history, callID)
			if calls != 1 || responses != 0 {
				t.Errorf("pending tool IDs before response = calls:%d responses:%d, want calls:1 responses:0", calls, responses)
			}

			response := &genai.Content{
				Role: string(genai.RoleUser),
				Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
					ID:       callID,
					Name:     "read",
					Response: map[string]any{"success": true},
				}}},
			}
			test.add(session, response)

			history = session.GetHistory()
			if len(history) > MaxMessages {
				t.Errorf("history len with completed pair = %d, exceeds %d", len(history), MaxMessages)
			}
			calls, responses = pendingToolIDCounts(history, callID)
			if calls != 1 || responses != 1 {
				t.Errorf("completed tool IDs = calls:%d responses:%d, want calls:1 responses:1", calls, responses)
			}
		})
	}
}

func pendingToolIDCounts(history []*genai.Content, id string) (calls, responses int) {
	for _, content := range history {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			if part.FunctionCall != nil && part.FunctionCall.ID == id {
				calls++
			}
			if part.FunctionResponse != nil && part.FunctionResponse.ID == id {
				responses++
			}
		}
	}
	return calls, responses
}
