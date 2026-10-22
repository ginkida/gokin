package app

import (
	"strings"
	"testing"

	"gokin/internal/donegate"
	"gokin/internal/ui"

	"google.golang.org/genai"
)

func TestSessionConversationRestoresToolsAndHidesInternalReview(t *testing.T) {
	history := []*genai.Content{
		genai.NewContentFromText("Fix the total", genai.RoleUser),
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{Text: "private thinking", Thought: true},
			{FunctionCall: &genai.FunctionCall{ID: "read-1", Name: "read", Args: map[string]any{"file_path": "sum.go"}}},
		}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			ID: "read-1", Name: "read", Response: map[string]any{"content": "package sum", "success": true},
		}}}},
		genai.NewContentFromText("Fixed sum.go", genai.RoleModel),
		genai.NewContentFromText(donegate.BuildInteractiveCompletionReviewPrompt("Fix the total", "Fixed sum.go", nil, nil, nil, nil, ""), genai.RoleUser),
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "GOKIN_REVIEW_"}, {Text: "UNCHANGED"}}},
		genai.NewContentFromText("Next request", genai.RoleUser),
		genai.NewContentFromText("Next answer", genai.RoleModel),
	}
	got := sessionConversation(history)
	if len(got.Entries) != 5 {
		t.Fatalf("unexpected visible entries: %+v", got.Entries)
	}
	tool := got.Entries[1].Tool
	if tool == nil || tool.Args["file_path"] != "sum.go" || tool.Content != "package sum" || tool.Failed {
		t.Fatalf("restored tool lost its context or outcome: %+v", tool)
	}
	if got.Entries[4].Role != "model" || got.Entries[4].Text != "Next answer" {
		t.Fatal("a subsequent ordinary reply inherited the review role")
	}
	for _, entry := range got.Entries {
		if strings.Contains(entry.Text, "private thinking") || strings.Contains(entry.Text, "Final completion review") || strings.Contains(entry.Text, donegate.CompletionReviewUnchanged) {
			t.Fatalf("internal exchange leaked: %+v", entry)
		}
	}
	// Conversion must leave the provider-visible history intact.
	if history[5].Parts[0].Text != "GOKIN_REVIEW_" || len(history) != 8 {
		t.Fatal("presentation mutated the model's saved context")
	}
}

func TestSessionConversationKeepsCorrectedVerdictAndFailedTool(t *testing.T) {
	history := []*genai.Content{
		genai.NewContentFromText(donegate.BuildInteractiveCompletionReviewPrompt("task", "draft", nil, nil, nil, nil, ""), genai.RoleUser),
		{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			Name: "bash", Response: map[string]any{"success": false, "error": "tests failed", "content": "FAIL sum"},
		}}}},
		genai.NewContentFromText("Tests failed; work remains.", genai.RoleModel),
	}
	got := sessionConversation(history)
	if len(got.Entries) != 2 || got.Entries[1].Role != "revision" || !got.Entries[0].Tool.Failed || got.Entries[0].Tool.Error != "tests failed" {
		t.Fatalf("review correction/failure was lost: %+v", got.Entries)
	}
}

func TestSwitchSessionPublishesConversationOnlyAfterSuccessfulSwitch(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	workDir := t.TempDir()
	a := newSessionLeaseTestApp(t, "visible-source", "glm", workDir, "source message")
	program, captured := newCapturingProgram(t)
	a.program = program
	state := saveSessionLeaseFixture(t, "visible-target", "glm", workDir, "target message")
	if _, err := a.SwitchSession(t.Context(), state, false); err != nil {
		t.Fatal(err)
	}
	// A following synchronous message is an event-loop barrier for the capture.
	program.Send("barrier")
	captured.mu.Lock()
	var restored []ui.SessionConversationMsg
	for _, msg := range captured.msgs {
		if message, ok := msg.(ui.SessionConversationMsg); ok {
			restored = append(restored, message)
		}
	}
	captured.mu.Unlock()
	if len(restored) != 1 || restored[0].Entries[0].Text != "target message" {
		t.Fatalf("successful switch did not publish the loaded history: %+v", restored)
	}
	state.ID = "missing-target"
	if _, err := a.SwitchSession(t.Context(), state, false); err == nil {
		t.Fatal("missing session was accepted")
	}
	program.Send("barrier")
	captured.mu.Lock()
	defer captured.mu.Unlock()
	count := 0
	for _, msg := range captured.msgs {
		if _, ok := msg.(ui.SessionConversationMsg); ok {
			count++
		}
	}
	if count != 1 {
		t.Fatal("failed switch replaced the visible conversation")
	}
}
