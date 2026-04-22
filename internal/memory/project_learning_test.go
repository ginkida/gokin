package memory

import (
	"os"
	"strings"
	"testing"
)

func TestProjectLearningFlush_WritesProjectMemoryMarkdown(t *testing.T) {
	dir := t.TempDir()

	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning() error = %v", err)
	}

	pl.SetPreference("test_command", "use go test ./internal/ui -count=1")
	pl.SetPreference("fact:auth_provider", "Kimi Coding Plan is the default provider")
	pl.SetPreference("convention:gofmt", "run gofmt after UI changes")
	pl.LearnPattern("live-card", "status and activity should stay visible during long tasks", nil, nil)

	if err := pl.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	data, err := os.ReadFile(pl.MarkdownPath())
	if err != nil {
		t.Fatalf("project memory markdown not written: %v", err)
	}

	rendered := string(data)
	for _, want := range []string{
		"# Project Memory",
		"## Preferences",
		"test_command",
		"## Facts",
		"auth_provider",
		"## Conventions",
		"gofmt",
		"## Learned Patterns",
		"live-card",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("project memory markdown missing %q:\n%s", want, rendered)
		}
	}
}

func TestProjectLearningFormatForPrompt_GroupsTypedKnowledge(t *testing.T) {
	dir := t.TempDir()

	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning() error = %v", err)
	}

	pl.SetPreference("test_command", "use go test ./...")
	pl.SetPreference("fact:retry_policy", "Kimi gets softer idle retries")
	pl.SetPreference("convention:formatting", "run gofmt before tests")

	prompt := pl.FormatForPrompt()
	for _, want := range []string{
		"### Preferences",
		"test_command",
		"### Facts",
		"retry_policy",
		"### Conventions",
		"formatting",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("project learning prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "fact:retry_policy") {
		t.Fatalf("fact keys should be normalized in prompt output:\n%s", prompt)
	}
}
