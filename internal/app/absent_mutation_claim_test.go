package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/permission"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

const absentEditGLMAnswer = "Done — m.go line 3 now has `// Add adds two ints.` above Add, confirmed via diff review. The go build ./... verification was blocked by a bash permission denial."

func TestHeadlessAbsentMutationClaimGetsOneCorrection(t *testing.T) {
	for _, correction := range []string{"still claims", "corrects answer", "performs edit", "denied"} {
		t.Run(correction, func(t *testing.T) {
			work := testkit.ResolvedTempDir(t)
			path := filepath.Join(work, "m.go")
			original := "package main\n\nfunc Add(a, b int) int { return a + b }\n"
			if err := os.WriteFile(path, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			mock := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": path})
			if correction == "denied" {
				mock.EnqueueToolCall("edit", map[string]any{"file_path": path, "old_string": "func Add", "new_string": "// Add adds two ints.\nfunc Add"})
			}
			mock.EnqueueText(absentEditGLMAnswer)
			switch correction {
			case "still claims", "denied":
				mock.EnqueueText(absentEditGLMAnswer)
			case "corrects answer":
				mock.EnqueueText("I did not change m.go or run go build. My earlier completion claim was incorrect.")
			case "performs edit":
				mock.EnqueueToolCall("edit", map[string]any{"file_path": path, "old_string": "func Add", "new_string": "// Add adds two ints.\nfunc Add"}).EnqueueText("Added the comment above Add in m.go. Verification has not run.")
			}
			a, exec := newHeadlessPolicyTestApp(t, mock, tools.NewReadTool(work))
			a.workDir = work
			a.registry.MustRegister(tools.NewEditTool(work))
			if correction == "denied" {
				rules := permission.DefaultRules()
				rules.SetPolicy("edit", permission.LevelDeny)
				exec.SetPermissions(permission.NewManager(rules, true))
			}
			var stdout, stderr bytes.Buffer
			result, err := a.RunHeadlessWithOptions(t.Context(), "Add a one-line comment above Add in m.go saying it adds two ints. Then run go build ./... to verify.", HeadlessOptions{PermissionMode: "acceptEdits", NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: &stdout, Stderr: &stderr})
			var receipt map[string]any
			if decodeErr := json.Unmarshal(stdout.Bytes(), &receipt); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			mismatch := correction == "still claims"
			if result.Incomplete != (mismatch || correction == "denied") || (receipt["claim_mismatch"] == true) != mismatch || err != nil || result.Status != "success" {
				t.Fatalf("absent edit reported complete: result=%+v err=%v JSON=%s", result, err, stdout.String())
			}
			wantCalls := 3
			if correction == "performs edit" {
				wantCalls = 4
			}
			if len(mock.Calls()) != wantCalls {
				t.Fatalf("model calls=%d, want %d", len(mock.Calls()), wantCalls)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil || (string(data) != original) != (correction == "performs edit") {
				t.Fatalf("unexpected file: %s %v", data, readErr)
			}
		})
	}
}

func TestInteractiveAbsentMutationClaimDoesNotContinue(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText("No file was changed. My previous claim about m.go was wrong.")
	a, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
	response := absentEditGLMAnswer
	var usage turnUsageAccumulator
	if !a.runCompletionReviewIfNeeded(t.Context(), "Add the comment", &response, &usage) || len(mock.Calls()) != 0 || response != absentEditGLMAnswer {
		t.Fatalf("interactive claim unexpectedly continued: response=%q calls=%d", response, len(mock.Calls()))
	}
}

func TestHeadlessAbsentMutationClaimWarnsInText(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText(absentEditGLMAnswer).EnqueueText(absentEditGLMAnswer)
	a, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
	var stderr bytes.Buffer
	result, err := a.RunHeadlessWithOptions(t.Context(), "Add the comment", HeadlessOptions{OutputFormat: HeadlessOutputText, Stdout: io.Discard, Stderr: &stderr})
	if err != nil || result.Status != "success" || !result.Incomplete || !strings.Contains(strings.ToLower(stderr.String()), "claim") {
		t.Fatalf("missing text warning: result=%+v err=%v stderr=%q", result, err, stderr.String())
	}
}

func TestAbsentMutationClaimHonorsCancellationAndInteractiveDenial(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "interactive denial"}[denied], func(t *testing.T) {
			mock := testkit.NewMockClient()
			a, exec := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "edit"})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if denied {
				rules := permission.DefaultRules()
				rules.SetPolicy("edit", permission.LevelDeny)
				exec.SetPermissions(permission.NewManager(rules, true))
				exec.EnablePreFlightChecks(false)
				result, err := exec.InvokeTool(ctx, "edit", map[string]any{"file_path": "m.go"})
				if err != nil || result.PolicyBlock == nil || result.PolicyBlock.Kind != tools.PolicyBlockPermission || !a.responsePermissionDenied {
					t.Fatalf("denial fixture failed: %+v %v", result, err)
				}
			} else {
				cancel()
			}
			response := absentEditGLMAnswer
			var usage turnUsageAccumulator
			if !a.runCompletionReviewIfNeeded(ctx, "Add the comment", &response, &usage) || len(mock.Calls()) != 0 || a.responseClaimMismatch {
				t.Fatalf("claim correction ignored cancellation/denial: calls=%d mismatch=%v", len(mock.Calls()), a.responseClaimMismatch)
			}
		})
	}
}

func TestHeadlessAbsentMutationClaimRespectsTurnCap(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText(absentEditGLMAnswer).EnqueueText("must not run")
	a, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
	result, err := a.RunHeadlessWithOptions(t.Context(), "Add the comment", HeadlessOptions{MaxTurns: 1, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Status != "success" || !result.ClaimMismatch || !result.Incomplete || len(mock.Calls()) != 1 {
		t.Fatalf("correction bypassed cap: %+v err=%v calls=%d", result, err, len(mock.Calls()))
	}
}

func TestHeadlessAbsentMutationClaimInStreamJSON(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText(absentEditGLMAnswer).EnqueueText(absentEditGLMAnswer)
	a, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
	var stdout bytes.Buffer
	_, err := a.RunHeadlessWithOptions(t.Context(), "Add the comment", HeadlessOptions{OutputFormat: HeadlessOutputStreamJSON, Stdout: &stdout, Stderr: io.Discard})
	if err != nil {
		t.Fatalf("advisory claim changed exit success: %v", err)
	}
	records := decodeHeadlessJSONL(t, stdout.Bytes())
	var result HeadlessResult
	if err := json.Unmarshal(records[len(records)-1], &result); err != nil || !result.ClaimMismatch || !result.Incomplete || result.Status != "success" {
		t.Fatalf("missing stream claim mismatch: %+v %v", result, err)
	}
	for _, raw := range records[:len(records)-1] {
		var event HeadlessStreamEvent
		if err := json.Unmarshal(raw, &event); err != nil || event.Timestamp.IsZero() {
			t.Fatalf("missing event timestamp: %s %v", raw, err)
		}
	}
}

func TestHeadlessPathlessAbsentMutationClaim(t *testing.T) {
	answer := "Added the requested one-line comment above Add."
	mock := testkit.NewMockClient().EnqueueText(answer).EnqueueText(answer)
	a, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
	result, err := a.RunHeadlessWithOptions(t.Context(), "Add the comment in m.go", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || !result.ClaimMismatch || !result.Incomplete || len(mock.Calls()) != 2 {
		t.Fatalf("pathless claim escaped: result=%+v err=%v calls=%d", result, err, len(mock.Calls()))
	}
}
