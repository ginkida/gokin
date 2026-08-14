package repl

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Analysis that silently walks ignored trees returns confidently wrong answers.
// Measured on this repository: a gitignored 266 MB Go toolchain cache made a
// "which packages have the most TODOs" ranking report the standard library —
// 2527 of 2635 hits — while the result still said truncated=False.
func TestSearchSkipsGitignoredTopLevelTrees(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	// Project source the question is actually about.
	if err := os.MkdirAll(filepath.Join(workDir, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "internal", "real.go"),
		[]byte("package internal\n// TODO: the only one that counts\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A vendored tree the user excluded, far larger than the project.
	vendored := filepath.Join(workDir, "go", "pkg", "mod")
	if err := os.MkdirAll(vendored, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		if err := os.WriteFile(filepath.Join(vendored, "dep"+string(rune('a'+i%26))+".go"),
			[]byte("package dep\n// TODO: not the user's code\n// TODO: nor this\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workDir, ".gitignore"), []byte("/go/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workDir, ".gokin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".gokin", "execution_journal.jsonl"),
		[]byte("TODO TODO TODO\nFIXME FIXME\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	manager := testManager(t, workDir, func(o *Options) { o.CellTimeout = 60 * time.Second })
	res, err := manager.Execute(t.Context(), `
r = context.search_code("TODO", path=".", limit=500)
c = context.count_code("TODO", path=".", group_by="top_dir")
[sorted({m["path"] for m in r["matches"]}), c["matching_lines"], c["groups"], c["truncated"]]`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != nil {
		t.Fatalf("search failed: %s: %s", res.Error.Type, res.Error.Message)
	}
	if !strings.Contains(res.Value, "real.go") {
		t.Fatalf("project source was not searched: %s", res.Value)
	}
	if strings.Contains(res.Value, "go/pkg/mod") {
		t.Fatalf("gitignored tree contaminated the answer: %s", res.Value)
	}
	if strings.Contains(res.Value, ".gokin") {
		t.Fatalf("agent runtime metadata contaminated the answer: %s", res.Value)
	}
	if !strings.Contains(res.Value, "1, {'internal': 1}, False") {
		t.Fatalf("aggregate count was not exact and scoped: %s", res.Value)
	}
	explicit, err := manager.Execute(t.Context(), `context.count_code("TODO", path=".gokin")`)
	if err != nil || explicit.Error != nil || !strings.Contains(explicit.Value, "'matching_lines': 1") {
		t.Fatalf("explicit metadata path should remain readable: result=%+v err=%v", explicit, err)
	}
}

// list_files exists so inventory questions have a scoped route; if it walked
// ignored trees it would just move the wrong answer from "most TODOs" to "how
// many files", where a count carries no hint that it is off by a toolchain.
func TestListFilesSkipsGitignoredTopLevelTrees(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	if err := os.MkdirAll(filepath.Join(workDir, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "internal", "real.go"),
		[]byte("package internal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vendored := filepath.Join(workDir, "go", "pkg", "mod")
	if err := os.MkdirAll(vendored, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		if err := os.WriteFile(filepath.Join(vendored, "dep"+string(rune('a'+i))+".go"),
			[]byte("package dep\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workDir, ".gitignore"), []byte("/go/\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	manager := testManager(t, workDir, func(o *Options) { o.CellTimeout = 60 * time.Second })
	res, err := manager.Execute(t.Context(), `
r = context.list_files(pattern="*.go")
[sorted(f["path"] for f in r["files"]), r["truncated"]]`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != nil {
		t.Fatalf("list failed: %s: %s", res.Error.Type, res.Error.Message)
	}
	if !strings.Contains(res.Value, "internal/real.go") {
		t.Fatalf("project source was not listed: %s", res.Value)
	}
	if strings.Contains(res.Value, "go/pkg/mod") {
		t.Fatalf("gitignored tree contaminated the inventory: %s", res.Value)
	}
	// The count is the whole point of this call; it must be exact, not capped.
	if !strings.Contains(res.Value, "False") {
		t.Fatalf("a 1-file listing must not report truncation: %s", res.Value)
	}
}

func TestFileIndexUsesNestedGitignoreNegationAndTrackedFiles(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	initFileIndexGitRepo(t, workDir)
	xdgConfig := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdgConfig, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	globalIgnore := filepath.Join(xdgConfig, "git", "ignore")
	if err := os.WriteFile(globalIgnore, []byte("*.private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	globalConfig := "[core]\n\texcludesFile = " + globalIgnore + "\n"
	if err := os.WriteFile(filepath.Join(xdgConfig, "git", "config"), []byte(globalConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	for name, content := range map[string]string{
		"web/src/live.txt":         "needle live\n",
		"web/generated/noise.txt":  "needle generated\n",
		"web/debug.log":            "needle debug\n",
		"web/important.log":        "needle important\n",
		"local-excluded.cache":     "needle local exclude\n",
		"global-excluded.private":  "needle global exclude\n",
		"tracked-then-ignored.txt": "needle tracked\n",
	} {
		path := filepath.Join(workDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	add := exec.Command("git", "-C", workDir, "add", "tracked-then-ignored.txt")
	if output, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add tracked file: %v (%s)", err, output)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".gitignore"), []byte("tracked-then-ignored.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "web", ".gitignore"), []byte("generated/\n*.log\n!important.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".git", "info", "exclude"), []byte("*.cache\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	manager := testManager(t, workDir, nil)
	res, err := manager.Execute(t.Context(), `
web = context.count_code("needle", path="web", group_by="file")
root = context.count_code("needle", path=".", group_by="file")
[web["matching_lines"], sorted(web["groups"]), web["truncated"], root["matching_lines"], sorted(root["groups"]), root["truncated"]]`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != nil {
		t.Fatalf("nested ignore scan failed: %s: %s", res.Error.Type, res.Error.Message)
	}
	for _, want := range []string{"web/src/live.txt", "web/important.log", "tracked-then-ignored.txt"} {
		if !strings.Contains(res.Value, want) {
			t.Fatalf("visible or tracked file %q missing from %s", want, res.Value)
		}
	}
	for _, unwanted := range []string{
		"web/generated/noise.txt", "web/debug.log",
		"local-excluded.cache", "global-excluded.private",
	} {
		if strings.Contains(res.Value, unwanted) {
			t.Fatalf("nested ignored file %q leaked into %s", unwanted, res.Value)
		}
	}
	if !strings.Contains(res.Value, "[2,") || !strings.Contains(res.Value, "False, 3,") {
		t.Fatalf("nested/tracked counts are not exact: %s", res.Value)
	}

	explicit, err := manager.Execute(t.Context(), `context.count_code("needle", path="web/generated")`)
	if err != nil || explicit.Error != nil || !strings.Contains(explicit.Value, "'matching_lines': 1") {
		t.Fatalf("explicit ignored subtree should remain readable: result=%+v err=%v", explicit, err)
	}
}

func TestExplicitIgnoredScopeIncludesTrackedAndUntrackedFiles(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	initFileIndexGitRepo(t, workDir)
	ignoredDir := filepath.Join(workDir, "ignored")
	if err := os.MkdirAll(ignoredDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ignoredDir, "tracked.txt"), []byte("needle tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "-C", workDir, "add", "ignored/tracked.txt")
	if output, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add tracked ignored-scope fixture: %v (%s)", err, output)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".gitignore"), []byte("ignored/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ignoredDir, "untracked.txt"), []byte("needle untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	manager := testManager(t, workDir, nil)
	root, err := manager.Execute(t.Context(), `context.count_code("needle", path=".")["matching_lines"]`)
	if err != nil || root.Error != nil || root.Value != "1" {
		t.Fatalf("implicit root inventory=%+v err=%v, want tracked file only", root, err)
	}
	explicit, err := manager.Execute(t.Context(), `context.count_code("needle", path="ignored", group_by="file")`)
	if err != nil || explicit.Error != nil ||
		!strings.Contains(explicit.Value, "'matching_lines': 2") ||
		!strings.Contains(explicit.Value, "ignored/tracked.txt") ||
		!strings.Contains(explicit.Value, "ignored/untracked.txt") ||
		explicit.FileIndexRefreshes != 1 {
		t.Fatalf("explicit ignored inventory=%+v err=%v", explicit, err)
	}
}

func TestFileIndexRefreshesBetweenCells(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	initFileIndexGitRepo(t, workDir)
	if err := os.WriteFile(filepath.Join(workDir, "first.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t, workDir, nil)
	first, err := manager.Execute(t.Context(), `context.count_code("needle")["matching_files"]`)
	if err != nil || first.Error != nil || first.Value != "1" {
		t.Fatalf("first count=%+v err=%v", first, err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "second.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := manager.Execute(t.Context(), `context.count_code("needle")["matching_files"]`)
	if err != nil || second.Error != nil || second.Value != "2" {
		t.Fatalf("refreshed count=%+v err=%v", second, err)
	}
}

func TestFileIndexScopesPathsFromWorkspaceInsideLargerRepository(t *testing.T) {
	outer := resolvedReplTempDir(t)
	initFileIndexGitRepo(t, outer)
	workDir := filepath.Join(outer, "project")
	for name, content := range map[string]string{
		"project/src/live.txt": "needle inside\n",
		"outside.txt":          "needle outside\n",
	} {
		path := filepath.Join(outer, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := testManager(t, workDir, nil)
	result, err := manager.Execute(t.Context(), `
whole = context.count_code("needle", path=".", group_by="file")
scoped = context.count_code("needle", path="src", group_by="file")
[whole["matching_lines"], sorted(whole["groups"]), scoped["matching_lines"], sorted(scoped["groups"]), whole["truncated"], scoped["truncated"]]`)
	if err != nil || result.Error != nil {
		t.Fatalf("nested workspace scan=%+v err=%v", result, err)
	}
	want := `[1, ['src/live.txt'], 1, ['src/live.txt'], False, False]`
	if result.Value != want {
		t.Fatalf("nested workspace scan=%s want=%s", result.Value, want)
	}
}

func initFileIndexGitRepo(t *testing.T, workDir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "--quiet", workDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v (%s)", err, output)
	}
}
