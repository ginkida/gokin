package app

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestSchemaClaimCorrectionCannotMutateAfterFinalization(t *testing.T) {
	schema, err := CompileStructuredOutputSchema(structuredOutputTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	root := testkit.ResolvedTempDir(t)
	path := filepath.Join(root, "m.go")
	answer := `{"answer":"Added a comment in m.go."}`
	mock := testkit.NewMockClient().EnqueueText(answer).
		EnqueueToolCall("write", map[string]any{"file_path": path, "content": "broken Go source"}).
		EnqueueText(answer)
	a, executor := newHeadlessPolicyTestApp(t, mock, tools.NewWriteTool(root))
	a.workDir = root
	executor.EnablePreFlightChecks(false)
	result, err := a.RunHeadlessWithOptions(t.Context(), "Add a comment in m.go", HeadlessOptions{JSONSchema: schema, NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("schema-stage advisory wrote after finalization: %v", statErr)
	}
	if err != nil || result.Status != "success" || !result.ClaimMismatch || !result.Incomplete || result.Result != answer {
		t.Fatalf("schema advisory changed outcome or lost final verdict: %+v %v", result, err)
	}
}

func TestConfirmedExternalWritesCountAsMutationEvidence(t *testing.T) {
	for _, name := range []string{"edit", "ssh", "memorize"} {
		t.Run(name, func(t *testing.T) {
			a := &App{workDir: testkit.ResolvedTempDir(t)}
			path := filepath.Join(testkit.ResolvedTempDir(t), "m.go")
			a.recordResponseTouchedPaths(name, map[string]any{"file_path": path}, tools.NewSuccessResultWithData("changed", map[string]any{"written_paths": []string{path}}))
			if a.responseMutationCount != 1 || len(a.responseTouchedPaths) != 0 || len(a.absentMutationClaim(t.Context(), "Updated "+path)) != 0 {
				t.Fatalf("external evidence was filtered with gate targets: mutations=%d targets=%v", a.responseMutationCount, a.responseTouchedPaths)
			}
		})
	}
}

func TestGrantedExternalWriteDoesNotTriggerClaimCorrection(t *testing.T) {
	root, external := testkit.ResolvedTempDir(t), testkit.ResolvedTempDir(t)
	path := filepath.Join(external, "m.go")
	mock := testkit.NewMockClient().EnqueueToolCall("write", map[string]any{"file_path": path, "content": "package m\n"}).EnqueueText("Updated " + path).EnqueueText("must not continue")
	a, executor := newHeadlessPolicyTestApp(t, mock, tools.NewWriteTool(root))
	a.workDir = root
	tools.SetAllowedDirsOnRegistry(a.registry, []string{external})
	executor.EnablePreFlightChecks(false)
	result, err := a.RunHeadlessWithOptions(t.Context(), "Update "+path, HeadlessOptions{NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Status != "success" || result.ClaimMismatch || result.Incomplete || len(mock.Calls()) != 2 {
		t.Fatalf("granted external write falsely flagged: %+v %v calls=%d", result, err, len(mock.Calls()))
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "package m\n" {
		t.Fatalf("external write did not happen: %q %v", content, err)
	}
}
