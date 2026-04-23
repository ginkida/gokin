package app

import (
	"strings"
	"testing"
)

func TestTokenizeResponseForPaths_ExtractsFilelikeTokens(t *testing.T) {
	response := `
Updated internal/app/foo.go and pkg/bar.py.
Also touched config.toml. Ran go test ./internal/app/ — all good.
`
	got := tokenizeResponseForPaths(response)
	want := map[string]bool{
		"internal/app/foo.go": true,
		"pkg/bar.py":          true,
		"config.toml":         true,
	}
	if len(got) < len(want) {
		t.Fatalf("got %d tokens, want at least %d: %v", len(got), len(want), got)
	}
	for _, g := range got {
		if !want[g] {
			// Not an error — the tokenizer can over-trigger; we assert
			// only the minimum set.
			continue
		}
		delete(want, g)
	}
	if len(want) > 0 {
		t.Errorf("missed expected path tokens: %v (got: %v)", want, got)
	}
}

func TestTokenizeResponseForPaths_IgnoresProseWithoutExtensions(t *testing.T) {
	got := tokenizeResponseForPaths("Updated the handler. No files mentioned here.")
	if len(got) != 0 {
		t.Errorf("prose without file-extensions should produce zero tokens, got: %v", got)
	}
}

func TestDetectFalselyClaimedPaths_FlagsMissingFromDiff(t *testing.T) {
	response := "Updated internal/app/foo.go and internal/app/ghost.go."
	gitChanged := []string{"internal/app/foo.go"} // ghost.go NOT in diff

	got := detectFalselyClaimedPaths(response, gitChanged)
	found := false
	for _, g := range got {
		if strings.Contains(g, "ghost.go") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected ghost.go to be flagged as falsely claimed, got: %v", got)
	}
	for _, g := range got {
		if strings.Contains(g, "foo.go") {
			t.Errorf("foo.go is in diff, should NOT be flagged: %v", got)
		}
	}
}

func TestDetectFalselyClaimedPaths_MatchesByBasename(t *testing.T) {
	// Response refers to a file only by its basename but git shows the
	// full path. The two should still reconcile (no false claim).
	response := "Updated foo.go with the new handler."
	gitChanged := []string{"internal/app/foo.go"}

	got := detectFalselyClaimedPaths(response, gitChanged)
	for _, g := range got {
		if strings.Contains(g, "foo.go") {
			t.Errorf("basename foo.go should match full path in diff, not flagged: %v", got)
		}
	}
}

func TestDetectFalselyClaimedPaths_EmptyInputsReturnNil(t *testing.T) {
	if got := detectFalselyClaimedPaths("", []string{"a.go"}); got != nil {
		t.Errorf("empty response should yield nil, got: %v", got)
	}
	if got := detectFalselyClaimedPaths("touched a.go", nil); got != nil {
		t.Errorf("empty git list should yield nil, got: %v", got)
	}
}

func TestBuildCompletionReviewPrompt_IncludesGitGroundTruth(t *testing.T) {
	prompt := buildCompletionReviewPrompt(
		"fix the bug",
		"Updated foo.go.",
		[]string{"foo.go"},                // tool-tracked
		[]string{"go test ./..."},         // verification command
		[]string{"foo.go", "foo_test.go"}, // ground truth from git
		nil,                               // no false claims
	)
	if !strings.Contains(prompt, "Files actually modified on disk") {
		t.Error("missing git ground-truth section")
	}
	if !strings.Contains(prompt, "foo_test.go") {
		t.Error("git ground-truth should list foo_test.go")
	}
	if !strings.Contains(prompt, "Only cite files that appear") {
		t.Error("review requirements should mention the file-citation guardrail")
	}
}

func TestBuildCompletionReviewPrompt_IncludesFalseClaimsSection(t *testing.T) {
	prompt := buildCompletionReviewPrompt(
		"fix the bug",
		"Also updated ghost.go.",
		nil,
		nil,
		[]string{"foo.go"},
		[]string{"ghost.go"},
	)
	if !strings.Contains(prompt, "[VERIFICATION FAILED]") {
		t.Error("missing [VERIFICATION FAILED] marker")
	}
	if !strings.Contains(prompt, "ghost.go") {
		t.Error("false-claim section should list ghost.go")
	}
}

func TestBuildCompletionReviewPrompt_SkipsEmptyOptionalSections(t *testing.T) {
	// With nil/empty inputs for the new sections, the prompt must not
	// emit empty headers.
	prompt := buildCompletionReviewPrompt(
		"fix it",
		"",
		nil,
		nil,
		nil,
		nil,
	)
	if strings.Contains(prompt, "Files actually modified on disk") {
		t.Error("should not emit git section when list is nil")
	}
	if strings.Contains(prompt, "[VERIFICATION FAILED]") {
		t.Error("should not emit verification-failed section when no false claims")
	}
}
