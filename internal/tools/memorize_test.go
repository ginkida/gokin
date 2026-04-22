package tools

import (
	"context"
	"os"
	"strings"
	"testing"

	"gokin/internal/memory"
)

func TestMemorizeTool_UpdatesProjectMemoryMarkdown(t *testing.T) {
	dir := t.TempDir()
	learning, err := memory.NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning() error = %v", err)
	}

	tool := NewMemorizeTool(learning)
	result, err := tool.Execute(context.Background(), map[string]any{
		"type":    "fact",
		"key":     "test_command",
		"content": "use go test ./internal/ui -count=1",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() success = false: %+v", result)
	}
	if !strings.Contains(result.Content, "project-memory.md") {
		t.Fatalf("expected result to mention project memory markdown, got %q", result.Content)
	}

	data, err := os.ReadFile(learning.MarkdownPath())
	if err != nil {
		t.Fatalf("failed to read project memory markdown: %v", err)
	}

	rendered := string(data)
	if !strings.Contains(rendered, "## Facts") || !strings.Contains(rendered, "test_command") {
		t.Fatalf("project memory markdown missing memorized fact:\n%s", rendered)
	}
}

func TestMemorizeTool_OverwritesExistingKnowledgeEntry(t *testing.T) {
	dir := t.TempDir()
	learning, err := memory.NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning() error = %v", err)
	}

	tool := NewMemorizeTool(learning)
	_, _ = tool.Execute(context.Background(), map[string]any{
		"type":    "preference",
		"key":     "test_command",
		"content": "use go test ./...",
	})
	_, _ = tool.Execute(context.Background(), map[string]any{
		"type":    "preference",
		"key":     "test_command",
		"content": "use go test ./internal/ui -count=1",
	})

	data, err := os.ReadFile(learning.MarkdownPath())
	if err != nil {
		t.Fatalf("failed to read project memory markdown: %v", err)
	}

	rendered := string(data)
	if !strings.Contains(rendered, "use go test ./internal/ui -count=1") {
		t.Fatalf("updated value missing from markdown:\n%s", rendered)
	}
	if strings.Contains(rendered, "use go test ./...\n") {
		t.Fatalf("stale value should not remain in markdown:\n%s", rendered)
	}
}
