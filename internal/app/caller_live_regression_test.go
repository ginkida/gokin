package app

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"gokin/internal/permission"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

const presentEditGLMAnswer = "The comment `// Add adds two ints.` is in place above `Add` in m.go, but I couldn't run `go build ./...` — the command was denied by the current permission mode, so compilation is unverified. Please run `go build ./...` yourself to confirm (it's a one-line comment, so the risk is minimal)."

func TestHeadlessPresentEditClaimAfterDenial(t *testing.T) {
	for _, tc := range []struct {
		name, tool, path, answer string
		mismatch                 bool
	}{
		{"bash denied", "bash", "", presentEditGLMAnswer, true},
		{"Russian bash denied", "bash", "", "Комментарий `// Add adds two ints.` на месте над Add в m.go, но сборка не запускалась: команда была запрещена.", true},
		{"edit denied for claimed path", "edit", "m.go", presentEditGLMAnswer, false},
		{"edit denied for another path", "edit", "other.go", presentEditGLMAnswer, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testkit.ResolvedTempDir(t)
			original := "package m\nfunc Add(a, b int) int { return a + b }\n"
			if err := os.WriteFile(filepath.Join(root, "m.go"), []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"command": "go build ./..."}
			if tc.tool == "edit" {
				args = map[string]any{"file_path": filepath.Join(root, tc.path), "old_string": "func Add", "new_string": "// Add adds two ints.\nfunc Add"}
			}
			mock := testkit.NewMockClient().EnqueueToolCall(tc.tool, args).EnqueueText(tc.answer).EnqueueText("must not continue")
			a, executor := newHeadlessPolicyTestApp(t, mock, tools.NewBashTool(root))
			a.workDir = root
			a.registry.MustRegister(tools.NewEditTool(root))
			rules := permission.DefaultRules()
			rules.SetPolicy(tc.tool, permission.LevelDeny)
			executor.SetPermissions(permission.NewManager(rules, true))
			executor.EnablePreFlightChecks(false)
			result, err := a.RunHeadlessWithOptions(t.Context(), "Add a one-line comment above Add in m.go saying it adds two ints. Then run go build ./... to verify.", HeadlessOptions{PermissionMode: "acceptEdits", NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
			if err != nil || result.Status != "success" || result.ClaimMismatch != tc.mismatch || !result.Incomplete || len(result.FilesChanged) != 0 || len(mock.Calls()) != 2 || len(result.PermissionDenials) != 1 {
				t.Fatalf("denial/claim receipt: %+v err=%v calls=%d", result, err, len(mock.Calls()))
			}
			if data, err := os.ReadFile(filepath.Join(root, "m.go")); err != nil || string(data) != original {
				t.Fatalf("denied run changed source: %s %v", data, err)
			}
		})
	}
}

func TestHeadlessPresentStateDescriptionWithoutChangeRequest(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText(presentEditGLMAnswer).EnqueueText("must not continue")
	a, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
	result, err := a.RunHeadlessWithOptions(t.Context(), "Describe the comment above Add in m.go", HeadlessOptions{NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.ClaimMismatch || result.Incomplete || len(mock.Calls()) != 1 {
		t.Fatalf("existing-code description became a mismatch: %+v %v", result, err)
	}
}

func TestHeadlessPresentEditClaimUsesOneAdvisoryCorrection(t *testing.T) {
	for _, answer := range []string{presentEditGLMAnswer, "Комментарий в m.go на месте над Add, сборка не проверена."} {
		mock := testkit.NewMockClient().EnqueueText(answer).EnqueueText(answer).EnqueueText("must not continue again")
		a, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
		result, err := a.RunHeadlessWithOptions(t.Context(), "Add a comment above Add in m.go", HeadlessOptions{NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
		if err != nil || result.Status != "success" || !result.ClaimMismatch || !result.Incomplete || len(mock.Calls()) != 2 {
			t.Fatalf("present-state correction budget/status: %+v %v calls=%d", result, err, len(mock.Calls()))
		}
	}
}

func TestHeadlessScratchpadDoesNotExplainAbsentProjectEdit(t *testing.T) {
	var stored string
	mock := testkit.NewMockClient().EnqueueToolCall("update_scratchpad", map[string]any{"content": "remember this"}).EnqueueText(absentEditGLMAnswer).EnqueueText(absentEditGLMAnswer)
	a, executor := newHeadlessPolicyTestApp(t, mock, tools.NewUpdateScratchpadTool(func(s string) { stored = s }))
	executor.SetPermissions(permission.NewManager(permission.DefaultRules(), true))
	result, err := a.RunHeadlessWithOptions(t.Context(), "Add the comment in m.go", HeadlessOptions{NoSelfReview: true, PermissionMode: "acceptEdits", OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || stored != "remember this" || len(result.FilesChanged) != 0 || !result.ClaimMismatch || !result.Incomplete || len(result.PermissionDenials) != 0 || len(mock.Calls()) != 3 {
		t.Fatalf("scratchpad counted as project work: %+v %v stored=%q calls=%d", result, err, stored, len(mock.Calls()))
	}
}
