package context

import (
	"testing"

	"google.golang.org/genai"
)

// Importance refinement used to retain a high-priority bash call while its
// ordinary successful result stayed in the summarized set. Applying that plan
// then left an orphan FunctionCall in history, which strict providers reject.
func TestCreateSummaryPlanImportanceKeepsToolExchangeAtomic(t *testing.T) {
	call := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID:   "call-1",
			Name: "bash",
			Args: map[string]any{"command": "go test ./..."},
		}}},
	}
	result := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			ID:       "call-1",
			Name:     "bash",
			Response: map[string]any{"success": true, "content": "ok"},
		}}},
	}

	history := []*genai.Content{
		genai.NewContentFromText("initial task", genai.RoleUser),
		genai.NewContentFromText("ordinary context before tool", genai.RoleModel),
		call,
		result,
		genai.NewContentFromText("ordinary context 1", genai.RoleModel),
		genai.NewContentFromText("ordinary context 2", genai.RoleUser),
		genai.NewContentFromText("ordinary context 3", genai.RoleModel),
		genai.NewContentFromText("ordinary context 4", genai.RoleUser),
		genai.NewContentFromText("ordinary context 5", genai.RoleModel),
		genai.NewContentFromText("ordinary context 6", genai.RoleUser),
		genai.NewContentFromText("ordinary context 7", genai.RoleModel),
		genai.NewContentFromText("ordinary context 8", genai.RoleUser),
		genai.NewContentFromText("ordinary context 9", genai.RoleModel),
		genai.NewContentFromText("recent turn", genai.RoleUser),
	}
	strategy := SummaryStrategy{
		KeepToolCalls:         true,
		InitialMessageCount:   1,
		RecentMessageCount:    1,
		UseImportanceScoring:  true,
		MinMessagesForSummary: 4,
		MaxHistorySize:        100,
		TargetRatio:           0.5,
	}

	plan := CreateSummaryPlan(history, strategy, NewMessageScorer())
	compacted := ApplySummaryPlan(plan, genai.NewContentFromText("summary", genai.RoleUser))

	var calls, responses int
	for _, msg := range compacted {
		for _, part := range msg.Parts {
			if part.FunctionCall != nil && part.FunctionCall.ID == "call-1" {
				calls++
			}
			if part.FunctionResponse != nil && part.FunctionResponse.ID == "call-1" {
				responses++
			}
		}
	}
	if calls != 1 || responses != 1 {
		t.Fatalf("compacted tool exchange is not atomic: calls=%d responses=%d", calls, responses)
	}
}
