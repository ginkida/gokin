package donegate

import (
	"strings"
	"testing"
)

func TestShouldRunCompletionReview_CodeChangeWithoutProof(t *testing.T) {
	if !ShouldRunCompletionReview(
		"fix the handler bug",
		"Updated the implementation.",
		[]string{"edit"},
		[]string{"internal/handler.go"},
		nil,
	) {
		t.Fatal("ShouldRunCompletionReview() = false, want true for code change without diff/verification proof")
	}
}

func TestShouldRunCompletionReview_SkipsWhenProofAlreadyExists(t *testing.T) {
	if ShouldRunCompletionReview(
		"fix the handler bug",
		"Updated internal/handler.go and verified with go test ./internal/...",
		[]string{"edit", "git_diff", "verify_code"},
		[]string{"internal/handler.go"},
		[]string{"go test ./internal/..."},
	) {
		t.Fatal("ShouldRunCompletionReview() = true, want false when diff and verification proof already exist")
	}
}

func TestShouldRunCompletionReview_SkipsDocsOnlyChange(t *testing.T) {
	if ShouldRunCompletionReview(
		"update the documentation",
		"Updated README.md.",
		[]string{"edit"},
		[]string{"README.md"},
		nil,
	) {
		t.Fatal("ShouldRunCompletionReview() = true, want false for docs-only change")
	}
}

func TestBuildCompletionReviewPromptIncludesRelevantContext(t *testing.T) {
	prompt := BuildCompletionReviewPrompt(
		"fix the handler bug",
		"Updated internal/handler.go.",
		[]string{"internal/handler.go", "internal/handler_test.go"},
		[]string{"go test ./internal/..."},
		nil, // no git ground truth for this shape test
		nil, // no false claims
		"- files_read: internal/handler.go\n- searches: grep \"Handle\" in **/*.go",
	)

	for _, needle := range []string{
		"Original user request:",
		"fix the handler bug",
		"Runtime evidence ledger gathered this turn:",
		"files_read: internal/handler.go",
		"searches: grep \"Handle\" in **/*.go",
		"Files changed in this turn",
		"internal/handler.go",
		"Successful commands already run:",
		"go test ./internal/...",
		"Current draft response already given to the user:",
		"Review requirements:",
	} {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("BuildCompletionReviewPrompt() missing %q:\n%s", needle, prompt)
		}
	}
}
