package app

import (
	"strings"
	"testing"

	"gokin/internal/tools"
)

func TestShouldRunCompletionReview_CodeChangeWithoutProof(t *testing.T) {
	if !shouldRunCompletionReview(
		"fix the handler bug",
		"Updated the implementation.",
		[]string{"edit"},
		[]string{"internal/handler.go"},
		nil,
	) {
		t.Fatal("shouldRunCompletionReview() = false, want true for code change without diff/verification proof")
	}
}

func TestShouldRunCompletionReview_SkipsWhenProofAlreadyExists(t *testing.T) {
	if shouldRunCompletionReview(
		"fix the handler bug",
		"Updated internal/handler.go and verified with go test ./internal/...",
		[]string{"edit", "git_diff", "verify_code"},
		[]string{"internal/handler.go"},
		[]string{"go test ./internal/..."},
	) {
		t.Fatal("shouldRunCompletionReview() = true, want false when diff and verification proof already exist")
	}
}

func TestShouldRunCompletionReview_SkipsDocsOnlyChange(t *testing.T) {
	if shouldRunCompletionReview(
		"update the documentation",
		"Updated README.md.",
		[]string{"edit"},
		[]string{"README.md"},
		nil,
	) {
		t.Fatal("shouldRunCompletionReview() = true, want false for docs-only change")
	}
}

func TestBuildCompletionReviewPromptIncludesRelevantContext(t *testing.T) {
	prompt := buildCompletionReviewPrompt(
		"fix the handler bug",
		"Updated internal/handler.go.",
		[]string{"internal/handler.go", "internal/handler_test.go"},
		[]string{"go test ./internal/..."},
		nil, // no git ground truth for this shape test
		nil, // no false claims
	)

	for _, needle := range []string{
		"Original user request:",
		"fix the handler bug",
		"Files changed in this turn",
		"internal/handler.go",
		"Successful commands already run:",
		"go test ./internal/...",
		"Current draft response already given to the user:",
		"Review requirements:",
	} {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("buildCompletionReviewPrompt() missing %q:\n%s", needle, prompt)
		}
	}
}

func TestAppRecordResponseCommand_TracksSuccessfulBashOnly(t *testing.T) {
	app := &App{}

	app.recordResponseCommand("read", map[string]any{"file_path": "internal/handler.go"}, tools.ToolResult{Success: true})
	app.recordResponseCommand("bash", map[string]any{"command": "go test ./internal/..."}, tools.ToolResult{Success: false})
	app.recordResponseCommand("bash", map[string]any{"command": "go test ./internal/..."}, tools.ToolResult{Success: true})
	app.recordResponseCommand("bash", map[string]any{"command": "go test ./internal/..."}, tools.ToolResult{Success: true})
	app.recordResponseCommand("bash", map[string]any{"command": "git diff --stat"}, tools.ToolResult{Success: true})

	got := app.snapshotResponseCommands()
	want := []string{"go test ./internal/...", "git diff --stat"}
	if len(got) != len(want) {
		t.Fatalf("snapshotResponseCommands() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("snapshotResponseCommands()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
