package chat

import (
	"strings"
	"testing"

	"gokin/internal/skills"

	"google.golang.org/genai"
)

func TestSessionStateGenerateSummaryUsesFirstUserMessage(t *testing.T) {
	session := NewSession()
	session.SetSystemInstruction("internal system instruction")
	session.AddUserMessage("Fix the flaky integration test")

	state := session.GetState()
	if got := state.GenerateSummary(); got != "Fix the flaky integration test" {
		t.Fatalf("GenerateSummary() = %q, want first user message", got)
	}
	if state.Summary != "Fix the flaky integration test" {
		t.Fatalf("GetState().Summary = %q, want generated first-turn summary", state.Summary)
	}
}

func TestSessionStateGenerateSummaryKeepsEarliestRealUserTurn(t *testing.T) {
	state := SessionState{History: []SerializedContent{
		SerializeContent(genai.NewContentFromText("first request", genai.RoleUser)),
		SerializeContent(genai.NewContentFromText("first response", genai.RoleModel)),
		SerializeContent(genai.NewContentFromText("follow-up request", genai.RoleUser)),
	}}

	if got := state.GenerateSummary(); got != "first request" {
		t.Fatalf("GenerateSummary() = %q, want earliest user turn", got)
	}
}

func TestSessionStateGenerateSummarySkipsSyntheticSkillCarry(t *testing.T) {
	carry := skills.ReattachInvocations(nil, []skills.Invocation{{
		Name:     "review",
		Rendered: "Review the implementation carefully.",
		Sequence: 1,
	}}, 0)
	if len(carry) != 1 {
		t.Fatalf("carry fixture len = %d, want 1", len(carry))
	}

	state := SessionState{History: []SerializedContent{
		SerializeContent(carry[0]),
		SerializeContent(genai.NewContentFromText("Review this pull request", genai.RoleUser)),
	}}
	if got := state.GenerateSummary(); got != "Review this pull request" {
		t.Fatalf("GenerateSummary() = %q, want real user message", got)
	}
}

func TestSessionStateGenerateSummarySkipsEmptyTextAndTruncatesRunes(t *testing.T) {
	long := strings.Repeat("界", 101)
	state := SessionState{History: []SerializedContent{
		{Role: "user", Parts: []SerializedPart{{Type: "text", Text: " \n\t "}}},
		SerializeContent(genai.NewContentFromText(long, genai.RoleUser)),
	}}

	want := strings.Repeat("界", 97) + "..."
	if got := state.GenerateSummary(); got != want {
		t.Fatalf("GenerateSummary() = %q, want %q", got, want)
	}
}
