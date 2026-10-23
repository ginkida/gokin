package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gokin/internal/agent"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestHeadlessFilesChangedUsesMutationsNotTargets(t *testing.T) {
	for _, name := range []string{"copy", "batch"} {
		t.Run(name, func(t *testing.T) {
			a := &App{workDir: testkit.ResolvedTempDir(t), headlessRunActive: true}
			source := filepath.Join(a.workDir, "a.txt")
			if err := os.WriteFile(source, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			var tool tools.Tool = tools.NewCopyTool(a.workDir)
			args := map[string]any{"source": source, "destination": filepath.Join(a.workDir, "b.txt")}
			want := []string{"b.txt"}
			if name == "batch" {
				tool = tools.NewBatchTool(a.workDir)
				args = map[string]any{"operation": "replace", "files": []any{source}, "search": "before", "replacement": "after", "dry_run": true}
				want = nil
			}
			result, err := tool.Execute(t.Context(), args)
			if err != nil || !result.Success {
				t.Fatalf("fixture: %+v %v", result, err)
			}
			a.recordResponseTouchedPaths(name, args, result)
			if !reflect.DeepEqual(a.headlessFilesChanged, want) {
				t.Fatalf("files_changed=%v want=%v", a.headlessFilesChanged, want)
			}
			data, _ := os.ReadFile(source)
			if string(data) != "before" {
				t.Fatal("source changed")
			}
		})
	}
}

func TestHeadlessIsolatedAgentFilesRequireApplyBack(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata map[string]any
		want     []string
	}{
		{"failed", map[string]any{"isolated_workspace": true}, nil},
		{"applied subset", map[string]any{"isolated_workspace": true, "isolated_workspace_applied_files": []string{"applied.go"}}, []string{"applied.go"}},
		{"serialized subset", map[string]any{"isolated_workspace": true, "isolated_workspace_applied_files": []any{"applied.go"}}, []string{"applied.go"}},
		{"shared workspace", nil, []string{"applied.go", "discarded.go"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, scope := activeUsageScopeApp()
			a.workDir = testkit.ResolvedTempDir(t)
			a.accumulateScopedAgentResultUsage(&agent.AgentResult{InvocationScope: scope, TouchedPaths: []string{"applied.go", "discarded.go"}, Metadata: test.metadata})
			if !reflect.DeepEqual(a.headlessFilesChanged, test.want) {
				t.Fatalf("files_changed=%v want=%v", a.headlessFilesChanged, test.want)
			}
		})
	}
}

func TestDelegatedMutationRetainsCompletionReview(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText("Reviewed both files.")
	a, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})
	a.workDir = testkit.ResolvedTempDir(t)
	a.beginHeadlessPolicyTracking()
	defer a.endHeadlessPolicyTracking()
	scope := agent.InvocationScopeFromContext(a.withHeadlessInvocationScope(context.Background()))
	a.recordResponseTouchedPaths("edit", map[string]any{"file_path": "first.go"}, tools.NewSuccessResultWithData("edited", map[string]any{"diff_added": 1, "diff_removed": 1}))
	a.accumulateScopedAgentResultUsage(&agent.AgentResult{InvocationScope: scope, TouchedPaths: []string{"second.go"}})
	answer := "Fixed both files."
	if !a.runCompletionReviewIfNeeded(t.Context(), "Fix both files.", &answer, nil) || len(mock.Calls()) != 1 {
		t.Fatalf("multi-file review skipped: calls=%d", len(mock.Calls()))
	}
}
