package app

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/agent"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestClaimMismatchIsAdvisoryAndInteractiveDoesNotContinue(t *testing.T) {
	t.Run("headless", func(t *testing.T) {
		mock := testkit.NewMockClient().EnqueueText(absentEditGLMAnswer).EnqueueText(absentEditGLMAnswer)
		a, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
		result, err := a.RunHeadlessWithOptions(t.Context(), "Add the comment in m.go", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
		if err != nil || result.Status != "success" || !result.ClaimMismatch || !result.Incomplete || len(mock.Calls()) != 2 {
			t.Fatalf("advisory changed execution outcome: %+v %v calls=%d", result, err, len(mock.Calls()))
		}
	})
	t.Run("interactive", func(t *testing.T) {
		mock := testkit.NewMockClient().EnqueueText("must not run")
		a, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
		response := absentEditGLMAnswer
		var usage turnUsageAccumulator
		a.runCompletionReviewIfNeeded(tools.ContextWithoutSelfReview(t.Context()), "Add the comment", &response, &usage)
		if len(mock.Calls()) != 0 || response != absentEditGLMAnswer {
			t.Fatal("interactive heuristic continued the model")
		}
	})
}

func TestMutationEvidenceIncludesDeclaredWritesAndExcludesPreviews(t *testing.T) {
	for _, name := range []string{"memorize", "ssh", "batch", "refactor"} {
		t.Run(name, func(t *testing.T) {
			a := &App{workDir: testkit.ResolvedTempDir(t)}
			a.recordResponseTouchedPaths(name, nil, tools.NewSuccessResultWithData("done", map[string]any{"written_paths": []string{"m.go"}}))
			if paths := a.absentMutationClaim(t.Context(), absentEditGLMAnswer); len(paths) != 0 {
				t.Fatalf("declared write ignored: %v", paths)
			}
		})
	}
	t.Run("preview", func(t *testing.T) {
		a := &App{workDir: testkit.ResolvedTempDir(t)}
		a.recordResponseTouchedPaths("batch", map[string]any{"file_path": "m.go", "dry_run": true}, tools.NewSuccessResultWithData("preview", map[string]any{"written_paths": []string{}}))
		if a.responseMutationCount != 0 || len(a.responseTouchedPaths) != 0 {
			t.Fatalf("preview became mutation/target: %d %v", a.responseMutationCount, a.responseTouchedPaths)
		}
	})
}

func TestSummaryRequestsDoNotTriggerClaimCorrection(t *testing.T) {
	for _, prompt := range []string{"Summarize your changes", "Что ты изменил?", "Review the changes in m.go"} {
		mock := testkit.NewMockClient().EnqueueText("Added a comment in m.go.").EnqueueText("must not run")
		a, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
		result, err := a.RunHeadlessWithOptions(t.Context(), prompt, HeadlessOptions{NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
		if err != nil || result.ClaimMismatch || len(mock.Calls()) != 1 {
			t.Errorf("summary treated as new edit: %q %+v %v calls=%d", prompt, result, err, len(mock.Calls()))
		}
	}
}

func TestClaimVerdictUsesFinalSchemaAnswer(t *testing.T) {
	schema, err := CompileStructuredOutputSchema(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, introduced := range []bool{false, true} {
		mock := testkit.NewMockClient()
		if introduced {
			mock.EnqueueText("No change was made.").EnqueueText(`{"summary":"Added a comment in m.go."}`).EnqueueText(`{"summary":"Added a comment in m.go."}`)
		} else {
			mock.EnqueueText(absentEditGLMAnswer).EnqueueText(absentEditGLMAnswer).EnqueueText(`{"summary":"No change was made."}`)
		}
		a, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
		result, err := a.RunHeadlessWithOptions(t.Context(), "Add a comment in m.go", HeadlessOptions{JSONSchema: schema, NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
		if err != nil || result.ClaimMismatch != introduced || len(mock.Calls()) != 3 || result.Usage.ModelRounds != 3 {
			t.Errorf("stale schema verdict: introduced=%v result=%+v err=%v", introduced, result, err)
		}
	}
}

func TestClaimOnlyChecksCompletedEditPathsAfterShellWrite(t *testing.T) {
	root := newClaimGitWorkspace(t)
	path := filepath.Join(root, "m.go")
	answer := "Added the comment in m.go. Read go.mod to confirm the module name."
	mock := testkit.NewMockClient().EnqueueToolCall("bash", map[string]any{"command": "echo '// comment' >> m.go"}).EnqueueText(answer)
	a, executor := newHeadlessPolicyTestApp(t, mock, tools.NewBashTool(root))
	executor.EnablePreFlightChecks(false)
	a.workDir = root
	result, err := a.RunHeadlessWithOptions(t.Context(), "Add a comment in m.go", HeadlessOptions{NoSelfReview: true, PermissionMode: "bypassPermissions", OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "// comment") {
		raw, _ := json.Marshal(mock.Calls()[1].Responses)
		t.Fatalf("shell fixture did not edit file: result=%+v tool_response=%s", result, raw)
	}
	if err != nil || result.ClaimMismatch || len(mock.Calls()) != 2 {
		t.Fatalf("unrelated read counted as edit claim: %+v %v calls=%d", result, err, len(mock.Calls()))
	}
}

func TestInteractiveDelegatedWritesAreRecorded(t *testing.T) {
	a := &App{workDir: testkit.ResolvedTempDir(t)}
	// A turn-owned scope excludes stale background completions.
	scope := agent.NewInvocationScope()
	a.responseAgentScope = scope
	a.accumulateScopedAgentResultUsage(&agent.AgentResult{InvocationScope: scope, FilesChanged: []string{"isolated/m.go"}, Metadata: map[string]any{"isolated_workspace": true, "isolated_workspace_applied_files": []string{"m.go"}}})
	if len(a.absentMutationClaim(t.Context(), absentEditGLMAnswer)) != 0 {
		t.Fatal("applied-back child mutation ignored")
	}
}

func TestClaimEvidenceExcludesPreviousScopeDuringHeadlessStartup(t *testing.T) {
	a, _ := activeUsageScopeApp()
	old := agent.NewInvocationScope()
	a.responseAgentScope = old
	a.accumulateScopedAgentResultUsage(&agent.AgentResult{InvocationScope: old, FilesChanged: []string{"m.go"}})
	if a.responseMutationCount != 0 || len(a.headlessFilesChanged) != 0 {
		t.Fatal("previous turn's completion leaked into a new headless invocation")
	}
}

func newClaimGitWorkspace(t *testing.T) string {
	t.Helper()
	root := testkit.ResolvedTempDir(t)
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.invalid"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	path := filepath.Join(root, "m.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "m.go"}, {"commit", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	return root
}

func TestClaimWorkspaceUsesTurnStartEvidence(t *testing.T) {
	for _, scenario := range []string{"clean unchanged", "preexisting dirty", "changed during turn", "other file only", "unknown probe"} {
		t.Run(scenario, func(t *testing.T) {
			root := newClaimGitWorkspace(t)
			path := filepath.Join(root, "m.go")
			if scenario == "preexisting dirty" {
				if err := os.WriteFile(path, []byte("// existing dirty change\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			a := &App{workDir: root, responseClaimBaseline: snapshotClaimWorkspace(t.Context(), root)}
			if !a.responseClaimBaseline.known || !a.responseClaimBaseline.repo {
				t.Fatal("Git baseline fixture unavailable")
			}
			switch scenario {
			case "changed during turn":
				if err := os.WriteFile(path, []byte("// new change\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "other file only":
				if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unknown probe":
				a.responseClaimBaseline.known = false
			}
			wantMismatch := scenario != "changed during turn" && scenario != "unknown probe"
			if mismatch := len(a.absentMutationClaim(t.Context(), "Added a comment in m.go. Read go.mod to confirm.")) > 0; mismatch != wantMismatch {
				t.Fatalf("claim verdict used dirty status instead of turn-start content: mismatch=%v want=%v", mismatch, wantMismatch)
			}
		})
	}
}

func TestAdvisoryCorrectionFailurePreservesSuccess(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText(absentEditGLMAnswer).EnqueueError(errors.New("local mock correction failed"))
	a, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
	result, err := a.RunHeadlessWithOptions(t.Context(), "Add the comment", HeadlessOptions{NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Status != "success" || !result.Incomplete || !result.ClaimMismatch || result.Result != absentEditGLMAnswer {
		t.Fatalf("advisory correction error became terminal: %+v %v", result, err)
	}
}

func TestAdvisoryCorrectionCannotBreakValidSchemaAnswer(t *testing.T) {
	schema, err := CompileStructuredOutputSchema(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	answer := `{"summary":"Added a comment in m.go."}`
	mock := testkit.NewMockClient().EnqueueText(answer).EnqueueText("My correction is not JSON.").EnqueueText("Still not JSON.").EnqueueText("Still not JSON.")
	a, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
	result, err := a.RunHeadlessWithOptions(t.Context(), "Add a comment in m.go", HeadlessOptions{JSONSchema: schema, NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Status != "success" || !result.ClaimMismatch || result.Result != answer || len(mock.Calls()) != 2 {
		t.Fatalf("advisory correction invalidated a valid schema answer: %+v %v calls=%d", result, err, len(mock.Calls()))
	}
}
