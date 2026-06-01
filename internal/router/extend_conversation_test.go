package router

import (
	"errors"
	"testing"

	"google.golang.org/genai"
)

// TestExtendConversation pins the v0.86.0 fix: the sub-agent and coordinated
// router handlers return a MINIMAL history (just the synthesized response).
// extendConversation must splice that onto the prior conversation + the user
// message so the caller's SetHistory doesn't wipe every prior turn.
func TestExtendConversation(t *testing.T) {
	prior := []*genai.Content{
		genai.NewContentFromText("turn 1 user", genai.RoleUser),
		genai.NewContentFromText("turn 1 model", genai.RoleModel),
	}
	result := []*genai.Content{
		genai.NewContentFromText("sub-agent answer", genai.RoleModel),
	}

	got, resp, err := extendConversation(prior, "fix the bug", result, "sub-agent answer", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "sub-agent answer" {
		t.Errorf("response = %q, want passthrough", resp)
	}
	// prior(2) + user(1) + result(1) = 4; the conversation must NOT shrink.
	if len(got) != 4 {
		t.Fatalf("extended history len = %d, want 4 (prior+user+result); a wipe would give 1", len(got))
	}
	if got[0] != prior[0] || got[1] != prior[1] {
		t.Error("prior turns were not preserved at the front")
	}
	if got[2].Role != genai.RoleUser {
		t.Errorf("spliced turn 3 role = %v, want User (the user message)", got[2].Role)
	}
	if got[3] != result[0] {
		t.Error("sub-agent result not appended last")
	}

	// Empty user message → no synthetic user turn, but still extends.
	if g, _, _ := extendConversation(prior, "   ", result, "x", nil); len(g) != 3 {
		t.Errorf("blank user message: len = %d, want 3 (prior+result)", len(g))
	}

	// On handler error, return the raw result unchanged (caller handles the error).
	raw, _, e := extendConversation(prior, "msg", nil, "", errors.New("boom"))
	if e == nil || raw != nil {
		t.Errorf("on error want (nil result, err), got (%v, %v)", raw, e)
	}
}
