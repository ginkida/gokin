package app

import (
	"context"
	"path/filepath"
	"testing"

	"gokin/internal/donegate"
	"gokin/internal/tools"
)

func TestAppRecordResponseTouchedPaths_TracksSuccessfulMutationsOnly(t *testing.T) {
	dir := t.TempDir()
	app := &App{workDir: dir}

	app.recordResponseTouchedPaths("read", map[string]any{
		"file_path": filepath.Join(dir, "README.md"),
	}, tools.ToolResult{Success: true})
	app.recordResponseTouchedPaths("edit", map[string]any{
		"file_path": filepath.Join(dir, "internal", "app.go"),
	}, tools.ToolResult{Success: false})
	app.recordResponseTouchedPaths("edit", map[string]any{
		"file_path": filepath.Join(dir, "internal", "app.go"),
	}, tools.ToolResult{Success: true})
	app.recordResponseTouchedPaths("write", map[string]any{
		"file_path": filepath.Join(dir, "internal", "app.go"),
	}, tools.ToolResult{Success: true})
	app.recordResponseTouchedPaths("move", map[string]any{
		"source":      filepath.Join(dir, "internal", "old.go"),
		"destination": filepath.Join(dir, "internal", "new.go"),
	}, tools.ToolResult{Success: true})

	got := app.snapshotResponseTouchedPaths()
	want := []string{"internal/app.go", "internal/new.go", "internal/old.go"}
	if len(got) != len(want) {
		t.Fatalf("touched paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("touched paths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestAppRecordResponseTouchedPaths_MergesWrittenPaths pins the v0.100.73 #2
// fix: a batch/refactor pattern-mode edit carries its targets as a "files" LIST
// or no path args at all, so its writes are self-declared via written_paths. The
// done-gate must merge those with the arg-side paths — otherwise a batch that
// broke a module records only an unrelated README write, the git ground-truth
// fallback is suppressed, and the broken module's checks are skipped (false PASS).
func TestAppRecordResponseTouchedPaths_MergesWrittenPaths(t *testing.T) {
	dir := t.TempDir()
	app := &App{workDir: dir}

	// A batch edit that broke packages/b (declared via written_paths — no scalar
	// path arg), plus an unrelated README write in the same turn.
	app.recordResponseTouchedPaths("batch", map[string]any{
		"operation": "replace",
	}, tools.NewSuccessResultWithData("done", map[string]any{
		"written_paths": []string{
			filepath.Join(dir, "packages", "b", "index.ts"),
			filepath.Join(dir, "packages", "b", "util.ts"),
		},
	}))
	app.recordResponseTouchedPaths("write", map[string]any{
		"file_path": filepath.Join(dir, "README.md"),
	}, tools.ToolResult{Success: true})

	got := app.snapshotResponseTouchedPaths()
	want := []string{"README.md", "packages/b/index.ts", "packages/b/util.ts"}
	if len(got) != len(want) {
		t.Fatalf("touched paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("touched paths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRunDoneGateChecks_RecordsSuccessfulVerificationEvidence(t *testing.T) {
	app := &App{}

	results := donegate.RunChecks(context.Background(), []donegate.Check{
		{
			Name:     "go_vet@.",
			Evidence: "go vet .",
			Run: func(context.Context) (tools.ToolResult, error) {
				return tools.NewSuccessResult("ok"), nil
			},
		},
	}, 0, app.reportDoneGateProgress, app.recordResponseVerificationEvidence)

	if len(results) != 1 || !results[0].Success {
		t.Fatalf("results = %+v, want one successful result", results)
	}

	got := app.snapshotResponseCommands()
	if len(got) != 1 || got[0] != "go vet ." {
		t.Fatalf("response commands = %v, want [go vet .]", got)
	}
}

func TestRunDoneGateChecks_DoesNotRecordFailedVerificationEvidence(t *testing.T) {
	app := &App{}

	results := donegate.RunChecks(context.Background(), []donegate.Check{
		{
			Name:     "go_vet@.",
			Evidence: "go vet .",
			Run: func(context.Context) (tools.ToolResult, error) {
				return tools.ToolResult{Success: false, Error: "vet failed"}, nil
			},
		},
	}, 0, app.reportDoneGateProgress, app.recordResponseVerificationEvidence)

	if len(results) != 1 || results[0].Success {
		t.Fatalf("results = %+v, want one failed result", results)
	}
	if got := app.snapshotResponseCommands(); len(got) != 0 {
		t.Fatalf("failed check should not record verification evidence, got %v", got)
	}
}

func TestDoneGateResultSummaryFormatsCounts(t *testing.T) {
	cases := []struct {
		passed int
		failed int
		want   string
	}{
		{passed: 3, failed: 0, want: "3 passed"},
		{passed: 0, failed: 2, want: "2 failed"},
		{passed: 3, failed: 1, want: "3 passed, 1 failed"},
	}

	for _, c := range cases {
		if got := formatDoneGateResultSummary(c.passed, c.failed); got != c.want {
			t.Fatalf("formatDoneGateResultSummary(%d,%d) = %q, want %q", c.passed, c.failed, got, c.want)
		}
	}
}

func TestFormatDoneGateResultSummary(t *testing.T) {
	cases := []struct {
		passed, failed int
		want           string
	}{
		{3, 0, "3 passed"},
		{0, 2, "2 failed"},
		{2, 1, "2 passed, 1 failed"},
		{0, 0, "0 passed"}, // edge: no results — falls into "failed == 0" branch
	}
	for _, tc := range cases {
		got := formatDoneGateResultSummary(tc.passed, tc.failed)
		if got != tc.want {
			t.Errorf("formatDoneGateResultSummary(%d, %d) = %q, want %q",
				tc.passed, tc.failed, got, tc.want)
		}
	}
}
