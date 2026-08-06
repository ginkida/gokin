package repl

import (
	"os"
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

	manager := testManager(t, workDir, func(o *Options) { o.CellTimeout = 60 * time.Second })
	res, err := manager.Execute(t.Context(), `
r = context.search_code("TODO", path=".", limit=500)
sorted({m["path"] for m in r["matches"]})`)
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

// The resolver must not over-reach: a directory that is NOT ignored stays
// searchable, or the fix would hide source the user wanted analysed.
func TestIgnoredTopLevelDirsOnlyReportsIgnoredOnes(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	for _, name := range []string{"internal", "build", "keepme"} {
		if err := os.MkdirAll(filepath.Join(workDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workDir, ".gitignore"), []byte("build/\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := ignoredTopLevelDirs(workDir)
	if len(got) != 1 || got[0] != "build" {
		t.Fatalf("ignored dirs = %v, want exactly [build]", got)
	}
}

// No .gitignore at all must not break traversal.
func TestIgnoredTopLevelDirsWithoutGitignore(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	if err := os.MkdirAll(filepath.Join(workDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ignoredTopLevelDirs(workDir); len(got) != 0 {
		t.Fatalf("ignored dirs = %v, want none", got)
	}
}
