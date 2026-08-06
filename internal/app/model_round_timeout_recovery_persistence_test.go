package app

import (
	"bytes"
	"strings"
	"testing"

	"gokin/internal/chat"

	"google.golang.org/genai"
)

func TestModelRoundTimeoutPartialHistorySurvivesRestartProtocolSafe(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const (
		completedID = "read-before-timeout"
		orphanID    = "write-never-started"
		partialText = "The completed read shows the timeout path"
	)
	signature := []byte("signed-thinking-state")
	history := []*genai.Content{
		genai.NewContentFromText("inspect timeout recovery", genai.RoleUser),
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{Text: "private protocol thought", Thought: true, ThoughtSignature: signature},
				{Text: "Starting the inspection."},
				{FunctionCall: &genai.FunctionCall{
					ID: completedID, Name: "read", Args: map[string]any{"path": "internal/client/streaming.go"},
				}},
			},
		},
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
				ID: completedID, Name: "read", Response: map[string]any{"text": "stream source"},
			}}},
		},
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{Text: partialText},
				{FunctionCall: &genai.FunctionCall{
					ID: orphanID, Name: "write", Args: map[string]any{"path": "never-created"},
				}},
			},
		},
	}

	cleaned := stripOrphanFunctionCalls(history)
	session := chat.NewSession()
	session.SetWorkDir(t.TempDir())
	session.SetHistory(cleaned)
	historyManager, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := historyManager.SaveFull(session); err != nil {
		t.Fatalf("SaveFull partial timeout history: %v", err)
	}

	state, err := historyManager.LoadFull(session.GetID())
	if err != nil {
		t.Fatalf("LoadFull partial timeout history: %v", err)
	}
	restarted := chat.NewSession()
	if err := restarted.RestoreFromState(state); err != nil {
		t.Fatalf("RestoreFromState partial timeout history: %v", err)
	}

	var (
		visible                strings.Builder
		completedCalls         int
		completedResponses     int
		orphanCalls            int
		thinkingStatePreserved bool
	)
	for _, content := range restarted.GetHistory() {
		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			if !part.Thought {
				visible.WriteString(part.Text)
			}
			if part.Thought && part.Text == "private protocol thought" &&
				bytes.Equal(part.ThoughtSignature, signature) {
				thinkingStatePreserved = true
			}
			if part.FunctionCall != nil {
				switch part.FunctionCall.ID {
				case completedID:
					completedCalls++
				case orphanID:
					orphanCalls++
				}
			}
			if part.FunctionResponse != nil && part.FunctionResponse.ID == completedID {
				completedResponses++
			}
		}
	}

	if !strings.Contains(visible.String(), partialText) {
		t.Fatalf("restarted session lost partial assistant text: %q", visible.String())
	}
	if completedCalls != 1 || completedResponses != 1 {
		t.Fatalf("restarted completed tool pair = calls:%d responses:%d, want 1:1",
			completedCalls, completedResponses)
	}
	if orphanCalls != 0 {
		t.Fatalf("restarted session retained %d unexecuted orphan tool calls", orphanCalls)
	}
	if !thinkingStatePreserved {
		t.Fatal("restarted session lost signed thinking protocol state")
	}
}
