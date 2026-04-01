package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- EnsureGokinGitignore ----------

func TestGitignore_CreatesFileInGitRepo(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	EnsureGokinGitignore(dir)

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("expected .gitignore to be created: %v", err)
	}
	content := string(data)

	for _, entry := range gokinGitignoreEntries {
		if !strings.Contains(content, entry) {
			t.Errorf("missing entry %q in .gitignore", entry)
		}
	}
	if !strings.Contains(content, "# Gokin") {
		t.Error("missing section header")
	}
}

func TestGitignore_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	EnsureGokinGitignore(dir)
	first, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))

	EnsureGokinGitignore(dir)
	second, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))

	if string(first) != string(second) {
		t.Errorf("second call changed .gitignore.\nFirst:\n%s\nSecond:\n%s", first, second)
	}
}

func TestGitignore_ExistingFilePartialEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Pre-populate .gitignore with one of the entries already present
	existing := gokinGitignoreEntries[0] + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	EnsureGokinGitignore(dir)

	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	content := string(data)

	// All entries must be present
	for _, entry := range gokinGitignoreEntries {
		if !strings.Contains(content, entry) {
			t.Errorf("missing entry %q", entry)
		}
	}

	// The pre-existing entry must appear exactly once
	if strings.Count(content, gokinGitignoreEntries[0]) != 1 {
		t.Errorf("duplicate entry %q found", gokinGitignoreEntries[0])
	}
}

func TestGitignore_NoGitDir_NoOp(t *testing.T) {
	dir := t.TempDir()

	EnsureGokinGitignore(dir)

	_, err := os.Stat(filepath.Join(dir, ".gitignore"))
	if !os.IsNotExist(err) {
		t.Error("expected .gitignore to NOT be created when .git is absent")
	}
}

// ---------- processIncludes ----------

func TestProcessIncludes_RelativePath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "docs")
	os.MkdirAll(sub, 0755)
	os.WriteFile(filepath.Join(sub, "extra.md"), []byte("included content"), 0644)

	input := "line1\n@./docs/extra.md\nline3"
	got := processIncludes(input, dir)

	if !strings.Contains(got, "included content") {
		t.Errorf("expected included content, got:\n%s", got)
	}
	if !strings.Contains(got, "line1") || !strings.Contains(got, "line3") {
		t.Error("surrounding lines should be preserved")
	}
}

func TestProcessIncludes_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	absFile := filepath.Join(dir, "abs.md")
	os.WriteFile(absFile, []byte("absolute include"), 0644)

	input := "before\n@" + absFile + "\nafter"
	got := processIncludes(input, "/some/other/base")

	if !strings.Contains(got, "absolute include") {
		t.Errorf("expected absolute include, got:\n%s", got)
	}
}

func TestProcessIncludes_HomePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}

	// Create a temp file under the real home directory
	tmpFile := filepath.Join(home, ".gokin_test_include_tmp.md")
	os.WriteFile(tmpFile, []byte("home content"), 0644)
	defer os.Remove(tmpFile)

	input := "@~/.gokin_test_include_tmp.md"
	got := processIncludes(input, "/irrelevant")

	if !strings.Contains(got, "home content") {
		t.Errorf("expected home content, got:\n%s", got)
	}
}

func TestProcessIncludes_NonIncludeLinesPassThrough(t *testing.T) {
	input := "plain line\n@[link](url)\nanother line"
	got := processIncludes(input, "/tmp")

	if !strings.Contains(got, "plain line") {
		t.Error("plain line missing")
	}
	if !strings.Contains(got, "@[link](url)") {
		t.Error("markdown link should be left unchanged")
	}
	if !strings.Contains(got, "another line") {
		t.Error("trailing line missing")
	}
}

func TestProcessIncludes_MissingFileKeepsOriginalLine(t *testing.T) {
	input := "@./nonexistent/file.md"
	got := processIncludes(input, "/tmp")

	if !strings.Contains(got, "@./nonexistent/file.md") {
		t.Errorf("missing-file line should be preserved, got:\n%s", got)
	}
}
