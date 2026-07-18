package context

import (
	"strings"
	"testing"
)

// v0.100.105 field report: the app itself (a GLM session) produced a 7-item
// MCP improvement list where 4 items ALREADY EXISTED in the code — the model
// never checked its own suggestions against the codebase. The base prompt now
// carries a standing verify-before-proposing rule. STATIC text by design (it
// lives in the cached system prefix and must stay byte-identical across
// turns).
func TestBasePromptCarriesVerifyBeforeProposing(t *testing.T) {
	pb := NewPromptBuilder("/tmp/x", &ProjectInfo{Type: ProjectTypeGo})
	prompt := pb.Build()
	for _, want := range []string{
		"Verify before you propose",
		"does not already exist",
		"not verified against the code",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("base prompt missing the proposal-honesty rule fragment %q", want)
		}
	}
	// Stability: two builds must be byte-identical (cached-prefix invariant).
	if prompt != pb.Build() {
		t.Error("prompt must be byte-stable across builds")
	}
}
