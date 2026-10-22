package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCommandTestFile(t *testing.T, root, relative, document string) string {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClaudeCommandFilesNamesMetadataAndLiveRefresh(t *testing.T) {
	root := t.TempDir()
	writeCommandTestFile(t, root, "plain.md", "Inspect the actual proof.")
	path := writeCommandTestFile(t, root, "frontend/ui/review.md", "---\nname: ignored-name\npaths: [ignored]\nallowed-tools: Read\n---\nReview component $ARGUMENTS.")
	catalog := NewCatalog([]Root{{Path: root, Source: "claude-project", Commands: true}})
	plain, ok := catalog.Get("plain")
	if !ok || plain.Body != "Inspect the actual proof." || plain.Description == "" {
		t.Fatal("plain Markdown command was not discovered", plain, catalog.Warnings())
	}
	command, ok := catalog.Get("frontend:ui:review")
	if !ok || command.DisplayName != command.Name || command.Path != path || len(command.AllowedTools) != 1 || command.AllowedTools[0] != "read" {
		t.Fatal("nested command lost its invocation identity or policy", command, catalog.Warnings())
	}
	if _, ok := catalog.Get("ignored-name"); ok {
		t.Fatal("command frontmatter renamed its file-based invocation")
	}
	writeCommandTestFile(t, root, "frontend/ui/review.md", "Updated command proof with different length.")
	catalog.ReloadIfChanged()
	updated, ok := catalog.Get("frontend:ui:review")
	if !ok || updated.Body != "Updated command proof with different length." {
		t.Fatal("nested edit did not invalidate declaration metadata", updated)
	}
	if !ValidInvocationName(command.Name) {
		t.Fatal("command identity cannot survive an invocation ledger round trip")
	}
}

func TestClaudeCommandFilesStayContainedAndBounded(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeCommandTestFile(t, outside, "escaped.md", "Outside instruction.")
	if err := os.Symlink(filepath.Join(outside, "escaped.md"), filepath.Join(root, "file.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "directory")); err != nil {
		t.Fatal(err)
	}
	writeCommandTestFile(t, root, "bad.md", "---\nshell: powershell\n---\nUnsupported execution mode.")
	writeCommandTestFile(t, root, "good.md", "Read the proof.")
	catalog := NewCatalog([]Root{{Path: root, Source: "claude-project", Commands: true}})
	if got := catalog.List(); len(got) != 1 || got[0].Name != "good" || !strings.Contains(strings.Join(catalog.Warnings(), "\n"), "shell") {
		t.Fatal("command discovery followed a link or dropped validation", got, catalog.Warnings())
	}
	path := strings.Repeat("nested/", 17) + "deep.md"
	writeCommandTestFile(t, root, path, "Too deeply nested.")
	if warnings := catalog.Reload(); !strings.Contains(strings.Join(warnings, "\n"), "nesting exceeds") {
		t.Fatal("command recursion was not bounded", warnings)
	}
}

func TestDefaultRootsSkillsWinOverCommandsAndUserCommandsWinOverProject(t *testing.T) {
	project, user := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", user)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeCommandTestFile(t, filepath.Join(user, "commands"), "review.md", "User command")
	writeCommandTestFile(t, filepath.Join(project, ".claude/commands"), "review.md", "Project command")
	catalog := NewCatalog(DefaultRoots(project))
	command, ok := catalog.Get("review")
	if !ok || command.Source != "claude-global" || command.Body != "User command" {
		t.Fatal("user command precedence changed", command)
	}
	writeSkill(t, filepath.Join(project, ".claude/skills"), "review", "---\ndescription: Skill preference\n---\nProject skill")
	catalog.Reload()
	command, ok = catalog.Get("review")
	if !ok || command.Source != "claude-project" || command.Body != "Project skill" {
		t.Fatal("a command shadowed a skill", command)
	}
}
