package repl

import (
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
		[]byte("package sample\n\nfunc Target() int { return 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t, workDir, func(o *Options) { o.CellTimeout = 30 * time.Second })

	cases := []struct {
		name string
		code string
		want string // substring the documented shape must produce
	}{
		{"workspace is a property", `context.workspace`, workDir},
		{"search_code returns the documented dict", `
r = context.search_code("Target", path=".", limit=50, case_sensitive=False)
[sorted(r.keys()), sorted(r["matches"][0].keys())]`,
			`['matches', 'scanned_files', 'truncated'], ['line', 'path', 'text']`},
		{"read_slice returns the documented dict", `
r = context.read_slice("sample.go", start_line=1, end_line=3)
[r["path"], r["start_line"], sorted(r["lines"][0].keys())]`,
			`sample.go`},
		{"runtime_limits is a call returning a dict", `type(context.runtime_limits()).__name__`, "dict"},
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

func resolvedReplTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
