package app

import (
	"strings"
	"testing"

	"gokin/internal/tools"
)

func TestAppRecordResponseEvidence_TracksReadsSearchesAndVerification(t *testing.T) {
	app := &App{workDir: "/repo"}
	app.responseToolsUsed = []string{"read", "grep", "bash", "edit"}
	app.responseTouchedPaths = []string{"internal/handler.go"}
	app.responseCommands = []string{"go test ./internal/..."}

	app.recordResponseEvidence("read", map[string]any{"file_path": "/repo/internal/handler.go"}, tools.ToolResult{Success: true})
	app.recordResponseEvidence("read", map[string]any{"file_path": "/repo/internal/handler.go"}, tools.ToolResult{Success: true})
	app.recordResponseEvidence("grep", map[string]any{"pattern": "HandleUser", "glob": "**/*.go"}, tools.ToolResult{Success: true})
	app.recordResponseEvidence("glob", map[string]any{"pattern": "internal/**/*.go"}, tools.ToolResult{Success: false})

	snapshot := app.snapshotResponseEvidence()
	if got, want := snapshot.ReadPaths, []string{"internal/handler.go"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("ReadPaths = %v, want %v", got, want)
	}
	if got := snapshot.Searches; len(got) != 1 || got[0] != `grep "HandleUser" in **/*.go` {
		t.Fatalf("Searches = %v", got)
	}
	if got := snapshot.VerificationCommands; len(got) != 1 || got[0] != "go test ./internal/..." {
		t.Fatalf("VerificationCommands = %v", got)
	}

	formatted := snapshot.FormatForCompletionReview()
	for _, needle := range []string{
		"tools_used: read, grep, bash, edit",
		"files_read: internal/handler.go",
		`searches: grep "HandleUser" in **/*.go`,
		"files_changed_by_tools: internal/handler.go",
		"verification: go test ./internal/...",
	} {
		if !strings.Contains(formatted, needle) {
			t.Fatalf("formatted ledger missing %q:\n%s", needle, formatted)
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
