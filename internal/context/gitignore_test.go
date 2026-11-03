package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- EnsureGokinGitignore ----------

func readExclude(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("read info/exclude: %v", err)
	}
	return string(data)
}

func assertNoGitignore(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf("starting gokin must not create .gitignore in the user's repository (err=%v)", err)
	}
}

// A read-only run used to create or append .gitignore, leaving a change in
// the user's repository. The entries now go to the local exclude file.
func TestGitignore_WritesLocalExcludeNotTrackedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	EnsureGokinGitignore(dir)

	assertNoGitignore(t, dir)
	content := readExclude(t, dir)
	for _, entry := range gokinGitignoreEntries {
		if !strings.Contains(content, entry+"\n") {
			t.Errorf("missing entry %q in info/exclude", entry)
		}
	}
}

func TestGitignore_LeavesExistingGitignoreUntouched(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	original := "node_modules/\n" + gokinGitignoreEntries[0] + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	EnsureGokinGitignore(dir)

	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(data) != original {
		t.Fatalf(".gitignore modified:\n%s", data)
	}
	content := readExclude(t, dir)
	if strings.Contains(content, gokinGitignoreEntries[0]) {
		t.Errorf("entry already in .gitignore duplicated into info/exclude")
	}
	if !strings.Contains(content, gokinGitignoreEntries[1]) {
		t.Errorf("missing entry %q", gokinGitignoreEntries[1])
	}
}

func TestGitignore_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git", "info"), 0755); err != nil {
		t.Fatal(err)
	}
	userExclude := "# user rule\n*.swp"
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte(userExclude), 0644); err != nil {
		t.Fatal(err)
	}

	EnsureGokinGitignore(dir)
	first := readExclude(t, dir)
	EnsureGokinGitignore(dir)
	second := readExclude(t, dir)

	if first != second {
		t.Errorf("second call changed info/exclude.\nFirst:\n%s\nSecond:\n%s", first, second)
	}
	if !strings.HasPrefix(first, userExclude+"\n") {
		t.Errorf("user's exclude rules not preserved:\n%s", first)
	}
}

func TestGitignore_LinkedWorktreeUsesCommonDir(t *testing.T) {
	main := t.TempDir()
	wtGitDir := filepath.Join(main, ".git", "worktrees", "wt")
	if err := os.MkdirAll(wtGitDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtGitDir, "commondir"), []byte("../..\n"), 0644); err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+wtGitDir+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	EnsureGokinGitignore(wt)

	assertNoGitignore(t, wt)
	if !strings.Contains(readExclude(t, main), gokinGitignoreEntries[0]) {
		t.Error("worktree entries must go to the common dir's info/exclude")
	}
}

func TestGitignore_NoGitDir_NoOp(t *testing.T) {
	dir := t.TempDir()

	EnsureGokinGitignore(dir)

	assertNoGitignore(t, dir)
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Error("must not create .git when absent")
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
	home := t.TempDir()
	t.Setenv("HOME", home)

	tmpFile := filepath.Join(home, ".gokin_test_include_tmp.md")
	if err := os.WriteFile(tmpFile, []byte("home content"), 0644); err != nil {
		t.Fatalf("write home include fixture: %v", err)
	}

	input := "@~/.gokin_test_include_tmp.md"
	got := processIncludes(input, "/irrelevant")

	if !strings.Contains(got, "home content") {
		t.Errorf("expected home content, got:\n%s", got)
	}
}

// TestProcessIncludes_TildeNoSlashStaysGuarded pins that a no-slash "@~foo"
// include does NOT resolve to $HOME/foo (which would bypass the traversal
// guard). Only "@~" and "@~/..." are home-rooted; "@~foo" falls through to the
// guarded relative branch.
func TestProcessIncludes_TildeNoSlashStaysGuarded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tmpFile := filepath.Join(home, "gokin_test_noslash_tmp.md")
	if err := os.WriteFile(tmpFile, []byte("SHOULD-NOT-LEAK home content"), 0644); err != nil {
		t.Fatalf("write home include fixture: %v", err)
	}

	input := "@~gokin_test_noslash_tmp.md"
	got := processIncludes(input, t.TempDir())

	if strings.Contains(got, "SHOULD-NOT-LEAK") {
		t.Errorf("@~foo (no slash) must NOT read $HOME/foo; leaked home content:\n%s", got)
	}
	// Falls through to the guarded relative branch; no such file under baseDir,
	// so the original include line is preserved verbatim.
	if !strings.Contains(got, "@~gokin_test_noslash_tmp.md") {
		t.Errorf("unresolved no-slash tilde include should be preserved, got:\n%s", got)
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

func TestGokinGitignoreCoversRuntimeFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	EnsureGokinGitignore(root)
	data := readExclude(t, root)
	for _, entry := range []string{".gokin/execution_journal.jsonl", ".gokin/recovery_snapshot.json", ".gokin/agent-output/", ".gokin/agents/checkpoints/"} {
		if !strings.Contains(data, entry+"\n") {
			t.Errorf("info/exclude misses runtime file %s", entry)
		}
	}
}
