package donegate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestRunChecksStopsAtPreAndPostToolHookStops(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(map[bool]string{false: "pre", true: "post"}[success], func(t *testing.T) {
			later, evidence := 0, 0
			results := RunChecks(t.Context(), []Check{
				{Name: "verify_code", Run: func(context.Context) (tools.ToolResult, error) {
					return tools.ToolResult{Success: success, HookStopReason: "stop checks", HookStopKind: "max_turns"}, nil
				}},
				{Name: "later", Run: func(context.Context) (tools.ToolResult, error) {
					later++
					return tools.NewSuccessResult("must not run"), nil
				}},
			}, 0, nil, func(string) { evidence++ })
			if len(results) != 1 || later != 0 || evidence != 0 || Passed(results) || !tools.IsHookStopError(StoppedError(results)) || !errors.Is(StoppedError(results), tools.ErrMaxTurnsExceeded) {
				t.Fatalf("hook control lost: results=%+v later=%d evidence=%d", results, later, evidence)
			}
		})
	}
}

func TestRunChecksDoesNotVerifyHookRewrittenShell(t *testing.T) {
	ctx, state := tools.ContextWithHeadlessPermissions(t.Context(), false, "default")
	evidence := 0
	result := tools.NewSuccessResult("skipped")
	result.HookUpdatedInput = map[string]any{"command": "echo skipped"}
	results := RunChecks(ctx, []Check{{Name: "go_vet@.", Run: func(context.Context) (tools.ToolResult, error) {
		return result, nil
	}}}, 0, nil, func(string) { evidence++ })
	if !Passed(results) || !results[0].Skipped || evidence != 0 || state.Verification().Status != "not_run" {
		t.Fatalf("rewritten check claimed verification: %+v receipt=%+v evidence=%d", results, state.Verification(), evidence)
	}
}

func TestMakeDryRunDoesNotClaimCompletedVerification(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("requires local make")
	}
	work := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(work, "Makefile"), []byte("all:\n\tfalse\n"), 0600); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewBashTool(work))
	checks := BuildChecks(registry, work, "Fix main.c", []string{"edit"}, Profile{MakeProjects: []string{work}, TouchedPaths: []string{"main.c"}}, Policy{Mode: "normal"})
	ctx, state := tools.ContextWithHeadlessPermissions(t.Context(), false, "default")
	results := RunChecks(ctx, checks, 0, nil, nil)
	if !Passed(results) || state.Verification().Status != "not_run" {
		t.Fatalf("make -n was mistaken for running the failing recipe: results=%+v receipt=%+v", results, state.Verification())
	}
}
