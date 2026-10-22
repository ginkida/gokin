package context

import (
	"strings"
	"testing"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"google.golang.org/genai"
)

func TestSummaryGuidanceIsLocalAndRejectsToolCalls(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText("Preserve the technical decisions and outstanding follow-up tasks.").EnqueueText("Preserve the same conversation without any additional focus.")
	summary := NewSummarizer(mock)
	messages := []*genai.Content{genai.NewContentFromText("original conversation", genai.RoleUser)}
	if _, err := summary.SummarizeWithInstructions(t.Context(), messages, "focus-guidance-unique"); err != nil {
		t.Fatal(err)
	}
	if _, err := summary.Summarize(t.Context(), messages); err != nil {
		t.Fatal(err)
	}
	calls := mock.Calls()
	if len(calls) != 2 || !strings.Contains(calls[0].Message, "focus-guidance-unique") || strings.Contains(calls[1].Message, "focus-guidance-unique") {
		t.Fatal("focus guidance leaked across summary requests")
	}
	mixed := testkit.NewMockClient().EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{{Text: "A sufficiently long summary that still requests an unauthorized action.", FunctionCalls: []*genai.FunctionCall{{Name: "bash", Args: map[string]any{"command": "unrequested"}}}}, {Done: true}}})
	if _, err := NewSummarizer(mixed).Summarize(t.Context(), messages); err == nil {
		t.Fatal("summary with tool calls accepted")
	}
}
func TestSummaryGuidanceReachesAllHierarchicalRequests(t *testing.T) {
	mock := testkit.NewMockClient()
	for i := 0; i < 12; i++ {
		mock.EnqueueText("A sufficiently detailed summary of selected conversation decisions.")
	}
	messages := make([]*genai.Content, 110)
	for i := range messages {
		messages[i] = genai.NewContentFromText("Conversation detail requiring preservation.", genai.RoleUser)
	}
	if _, err := NewSummarizer(mock).SummarizeWithInstructions(t.Context(), messages, "retain migration constraints"); err != nil {
		t.Fatal(err)
	}
	calls := mock.Calls()
	if len(calls) < 2 {
		t.Fatal("hierarchical fixture did not split")
	}
	for _, call := range calls {
		if !strings.Contains(call.Message, "retain migration constraints") {
			t.Fatal("hierarchical stage lost user guidance")
		}
	}
}
