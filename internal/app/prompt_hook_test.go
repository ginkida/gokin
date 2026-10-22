package app

import (
	"io"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/hooks"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func installPromptHookFixture(t *testing.T, a *App, event string, continued bool) {
	t.Helper()
	skill := tools.NewSkillToolWithCatalog(skills.NewCatalog(nil))
	a.registry.MustRegister(skill)
	registration, err := skill.HookScope().Prepare("review-fixture", []hooks.ClaudeCommand{{Event: event, Handler: "prompt", Prompt: "HOOK_ACCEPTANCE_CHECK: approve only verified evidence. $ARGUMENTS", Timeout: time.Second, ContinueOnBlock: continued}})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
}

func TestPromptHookSharesHeadlessLimitsAndChargesUsage(t *testing.T) {
	for _, test := range []struct {
		name, answer, kind string
		cap                int
		budget             float64
		calls, writes      int
	}{
		{name: "allow", answer: `{"ok":true}`, cap: 4, calls: 1, writes: 1},
		{name: "deny", answer: `{"ok":false,"reason":"acceptance proof missing"}`, kind: "hook_stopped", cap: 4, calls: 1},
		{name: "turn-cap", answer: `{"ok":true}`, kind: "max_turns", cap: 1},
		{name: "cost-cap", answer: `{"ok":true}`, kind: "budget_exceeded", cap: 4, budget: 1e-12, calls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "proof.txt"}).EnqueueText("The proof was inspected")
			verifier := testkit.NewMockClient().EnqueueText(test.answer)
			a := newGoalTestApp(t, worker, verifier)
			if test.budget > 0 {
				a.executor.SetCostCalculator(func(string, string, int, int, int) (float64, bool) { return 0, true })
			}
			installPromptHookFixture(t, a, "PreToolUse", false)
			result, err := a.RunHeadlessWithOptions(t.Context(), "inspect proof", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: test.cap, MaxBudgetUSD: test.budget})
			if test.kind == "" {
				if err != nil {
					t.Fatalf("valid prompt hook failed: %+v %v", result, err)
				}
			} else if err == nil || result.Error == nil || result.Error.Kind != test.kind {
				t.Fatalf("wrong typed hook failure: %+v %v", result, err)
			}
			if len(verifier.Calls()) != test.calls {
				t.Fatalf("verifier escaped request cap: %d", len(verifier.Calls()))
			}
			wantInput := (len(worker.Calls()) + len(verifier.Calls())) * 10
			if result.Usage.InputTokens != wantInput || result.Usage.ModelRounds != wantInput/10 {
				t.Fatalf("prompt hook spend was lost or duplicated: %+v", result.Usage)
			}
			tool, _ := a.registry.Get("read")
			if tool.(*appHeadlessScriptedTool).CallCount() != test.writes {
				t.Fatal("prompt denial crossed the tool boundary")
			}
		})
	}
}

func TestPromptStopHookContinuesThenAllowsTurn(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("Initial result").EnqueueText("The requested acceptance proof is now complete")
	verifier := testkit.NewMockClient().EnqueueText(`{"ok":false,"reason":"finish acceptance proof"}`).EnqueueText(`{"ok":true}`)
	a := newGoalTestApp(t, worker, verifier)
	installPromptHookFixture(t, a, "Stop", false)
	result, err := a.RunHeadlessWithOptions(t.Context(), "finish acceptance proof", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 5})
	if err != nil || len(worker.Calls()) != 2 || len(verifier.Calls()) != 2 || result.Usage.InputTokens != 40 {
		t.Fatalf("prompt stop continuation was lost: %+v %v", result, err)
	}
}

func TestPromptHookProviderFailureKeepsGoalErrorClassification(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "proof.txt"})
	verifier := testkit.NewMockClient().EnqueueStartupError(&client.TerminalProviderError{Code: "1113", Status: 400, Message: "insufficient balance"})
	a := newGoalTestApp(t, worker, verifier)
	installPromptHookFixture(t, a, "PreToolUse", false)
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal inspect the proof", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 4})
	if err == nil || result.Goal == nil || result.Goal.Status != "cleared" || len(worker.Calls()) != 1 {
		t.Fatalf("hook lost fatal provider cause: %+v %v", result, err)
	}
}
