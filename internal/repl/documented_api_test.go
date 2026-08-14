package repl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every signature the repl_exec description advertises is executed here.
//
// Documentation that lies is worse than none: a caller who follows it and gets
// a TypeError learns to avoid the tool entirely. This test is the reason the
// description can state exact return shapes — if the runtime changes, the doc
// stops being a guess and starts being a failing test.
func TestDocumentedContextAPIExecutesAsAdvertised(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	if err := os.WriteFile(filepath.Join(workDir, "sample.go"),
		[]byte("package sample\n\n// TODO: improve\nfunc Target() int { return 1 } // FIXME: demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"internal/alpha/a.go": "// TODO one\n// FIXME two\n",
		"internal/beta/b.go":  "// TODO three\n",
	} {
		fullPath := filepath.Join(workDir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workDir, "bulk.txt"),
		[]byte(strings.Repeat("TODO\n", 600)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "asset.bin"),
		[]byte("TODO in binary\x00TODO\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "mystery.dat"),
		[]byte("TODO in unknown binary\x00TODO\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for index := 0; index <= 1024; index++ {
		path := filepath.Join(workDir, "many-groups", fmt.Sprintf("group-%04d", index), "file")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := testManager(t, workDir, func(o *Options) { o.CellTimeout = 30 * time.Second })

	cases := []struct {
		name string
		code string
		want string // substring the documented shape must produce
	}{
		{"workspace is a property", `context.workspace`, workDir},
		{"search_code returns the documented dict", `
r = context.search_code("Target", path=".", limit=50, case_sensitive=False, regex=False)
[sorted(r.keys()), sorted(r["matches"][0].keys())]`,
			`['matches', 'scanned_files', 'searched_files', 'skipped_files', 'truncated'], ['line', 'path', 'text']`},
		{"search_code supports one-pass regular expressions", `
r = context.search_code(r"TODO|FIXME", path="sample.go", limit=50, regex=True)
[len(r["matches"]), r["truncated"]]`, `[2, False]`},
		{"search_code remains literal by default", `
len(context.search_code(r"TODO|FIXME", path="sample.go", limit=50)["matches"])`, `0`},
		{"search_code rejects invalid regular expressions", `
try:
    context.search_code("(", regex=True)
    outcome = "no-error"
except ValueError as exc:
    outcome = str(exc)
outcome`, `invalid regular expression`},
		{"count_code aggregates without materializing matches", `
r = context.count_code(r"TODO|FIXME", path="sample.go", regex=True, group_by="extension")
[sorted(r.keys()), r["matching_lines"], r["matching_files"], r["groups"], r["truncated"]]`,
			`['groups', 'matching_files', 'matching_lines', 'scanned_files', 'searched_files', 'skipped_files', 'truncated'], 2, 1, {'.go': 2}, False`},
		{"count_code returns bounded evidence without losing the exact count", `
r = context.count_code(r"TODO|FIXME", path="sample.go", regex=True, sample_limit=1)
[r["matching_lines"], len(r["samples"]), r["samples_truncated"], sorted(r["samples"][0].keys())]`,
			`[2, 1, True, ['line', 'path', 'text']]`},
		{"count_code_many compares patterns in one scan", `
r = context.count_code_many(["TODO", "FIXME"], path="sample.go", group_by="file")
[[c["query"], c["matching_lines"], c["matching_files"], c["groups"]] for c in r["counts"]]`,
			`[['TODO', 1, 1, {'sample.go': 1}], ['FIXME', 1, 1, {'sample.go': 1}]]`},
		{"count_code_many enforces its query bound", `
try:
    context.count_code_many(["x"] * 33)
    outcome = "no-error"
except ValueError as exc:
    outcome = str(exc)
outcome`, `queries exceed count limit`},
		{"search and count skip known and detected binaries", `
s = context.search_code("TODO", path="asset.bin")
c = context.count_code("TODO", path="mystery.dat")
[len(s["matches"]), s["searched_files"], s["skipped_files"], s["truncated"], c["matching_lines"], c["searched_files"], c["skipped_files"], c["truncated"]]`,
			`[0, 0, {'binary': 1, 'oversized': 0, 'unreadable': 0}, False, 0, 0, {'binary': 1, 'oversized': 0, 'unreadable': 0}, False]`},
		{"count_code groups relative to the requested subtree", `
r = context.count_code(r"TODO|FIXME", path="internal", regex=True, group_by="top_dir")
[r["matching_lines"], r["matching_files"], r["groups"], r["truncated"]]`,
			`[3, 2, {'alpha': 2, 'beta': 1}, False]`},
		{"count_code stays exact beyond the evidence result cap", `
s = context.search_code("TODO", path="bulk.txt", limit=500)
c = context.count_code("TODO", path="bulk.txt")
[len(s["matches"]), s["truncated"], c["matching_lines"], c["truncated"]]`,
			`[500, True, 600, False]`},
		{"count evidence cap is independent from scan completeness", `
c = context.count_code("TODO", path="bulk.txt", sample_limit=100)
[c["matching_lines"], len(c["samples"]), c["samples_truncated"], c["truncated"]]`,
			`[600, 20, True, False]`},
		{"list_files returns the documented dict", `
r = context.list_files(path=".", pattern="*.go")
[sorted(r.keys()), sorted(r["files"][0].keys())]`,
			`['files', 'scanned_files', 'truncated'], ['path', 'size']`},
		{"list_files pattern filters by relative path", `
[len(context.list_files(pattern="*.go")["files"]), len(context.list_files(pattern="*.rs")["files"])]`,
			`[3, 0]`},
		{"file_stats aggregates inventory without materializing paths", `
r = context.file_stats(pattern="*.go", exclude_pattern="*_test.go", group_by="top_dir")
[sorted(r.keys()), r["matching_files"], r["total_bytes"] > 0, r["groups"]["."]["files"], r["groups"]["internal"]["files"]]`,
			`[['groups', 'matching_files', 'scanned_files', 'total_bytes', 'truncated'], 3, True, 1, 2]`},
		{"file_stats groups by extension", `
r = context.file_stats(pattern="*.go", group_by="extension")
[r["matching_files"], r["groups"][".go"]["files"], r["groups"][".go"]["bytes"] == r["total_bytes"], r["truncated"]]`,
			`[3, 3, True, False]`},
		{"file_stats validates grouping", `
try:
    context.file_stats(group_by="file")
    outcome = "no-error"
except ValueError as exc:
    outcome = str(exc)
outcome`, `group_by must be extension, top_dir, or None`},
		{"file_stats bounds grouping cardinality", `
try:
    context.file_stats(path="many-groups", group_by="top_dir")
    outcome = "no-error"
except ValueError as exc:
    outcome = str(exc)
outcome`, `file_stats groups exceed 1024-entry limit`},
		{"read_slice returns the documented dict", `
r = context.read_slice("sample.go", start_line=1, end_line=3)
[r["path"], r["start_line"], sorted(r["lines"][0].keys())]`,
			`sample.go`},
		{"Git remains on structured tools", `
[hasattr(context, "git_status"), hasattr(context, "git_diff")]`,
			`[False, False]`},
		{"runtime_limits documents context API bounds", `
r = context.runtime_limits()
[type(r).__name__, r["max_search_files"], r["max_search_matches"], r["max_count_queries"], r["max_count_samples"], r["max_count_sample_text_chars"], r["max_file_stat_groups"], r["max_read_lines"], r["max_file_index_cache_bytes"], r["max_inline_bytes"], r["max_artifact_chunk_bytes"], r["max_artifact_bytes"], r["max_artifact_total_bytes"], r["max_memory_bytes"]]`,
			`['dict', 20000, 500, 32, 20, 300, 1024, 2000, 4194304, 8192, 4096, 4194304, 16777216, 268435456]`},
		{"artifact_get reports unknown ids honestly", `
try:
    context.artifact_get("nope")
    outcome = "no-error"
except KeyError:
    outcome = "KeyError"
outcome`, "KeyError"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := manager.Execute(t.Context(), tc.code)
			if err != nil {
				t.Fatal(err)
			}
			if res.Error != nil {
				t.Fatalf("documented call failed at runtime: %s: %s", res.Error.Type, res.Error.Message)
			}
			if !strings.Contains(res.Value, tc.want) {
				t.Fatalf("documented shape not produced.\n got: %s\nwant substring: %s", res.Value, tc.want)
			}
		})
	}
}

// The two mistakes the old description invited, pinned as the wrong usage so a
// future change that makes them "work" has to update this deliberately.
func TestUndocumentedGuessesStillFail(t *testing.T) {
	manager := testManager(t, resolvedReplTempDir(t), nil)
	for name, code := range map[string]string{
		"workspace as a method": `context.workspace()`,
		"search_code as a list": `context.search_code("x")[0]`,
	} {
		t.Run(name, func(t *testing.T) {
			res, err := manager.Execute(t.Context(), code)
			if err != nil {
				t.Fatal(err)
			}
			if res.Error == nil {
				t.Fatalf("guessed usage unexpectedly succeeded: %+v", res)
			}
		})
	}
}

func TestSearchAndCountReportOversizedFiles(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	path := filepath.Join(workDir, "oversized.txt")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(2*1024*1024 + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t, workDir, nil)
	res, err := manager.Execute(t.Context(), `
s = context.search_code("needle", path="oversized.txt")
c = context.count_code("needle", path="oversized.txt")
[s["searched_files"], s["skipped_files"], s["truncated"], c["searched_files"], c["skipped_files"], c["truncated"]]`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != nil {
		t.Fatalf("oversized-file accounting failed: %s: %s", res.Error.Type, res.Error.Message)
	}
	want := `[0, {'binary': 0, 'oversized': 1, 'unreadable': 0}, True, 0, {'binary': 0, 'oversized': 1, 'unreadable': 0}, True]`
	if !strings.Contains(res.Value, want) {
		t.Fatalf("oversized accounting = %s, want %s", res.Value, want)
	}
}

func resolvedReplTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
