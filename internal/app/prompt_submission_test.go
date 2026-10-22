package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/commands"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func installSubmissionSettings(t *testing.T, a *App, handler map[string]any) {
	t.Helper()
	a.hooksManager.ConfigureClaudeSettings(a.workDir, t.TempDir(), true)
	path := filepath.Join(a.workDir, ".claude/settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"hooks": map[string]any{"UserPromptSubmit": []any{map[string]any{"hooks": []any{handler}}}}})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.closeAsyncHooks)
}

func TestUserPromptSubmitRunsBeforeModelAndRetainsContextAndTitle(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("Done")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	installSubmissionSettings(t, a, map[string]any{"type": "command", "command": `cat > prompt-input.json; printf x >> prompt-runs; printf '%s' '{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"SUBMITTED_EVIDENCE","sessionTitle":"Review title"}}'`})
	result, err := a.RunHeadlessWithOptions(t.Context(), "inspect '$HOME'", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Usage.ModelRounds != 1 || len(worker.Calls()) != 1 || !strings.Contains(worker.Calls()[0].Message, "SUBMITTED_EVIDENCE") {
		t.Fatalf("submit context lost: %+v %v", result, err)
	}
	data, _ := os.ReadFile(filepath.Join(a.workDir, "prompt-input.json"))
	var input map[string]any
	_ = json.Unmarshal(data, &input)
	if input["prompt"] != "inspect '$HOME'" || input["tool_name"] != nil {
		t.Fatalf("wrong submit payload: %s", data)
	}
	if a.session.GetTitle() != "Review title" || !strings.Contains(a.session.GetHistory()[0].Parts[0].Text, "SUBMITTED_EVIDENCE") {
		t.Fatal("submission metadata/context was not retained")
	}
}

func TestUserPromptSubmitDenialDoesNotReachModelOrCommand(t *testing.T) {
	for _, entry := range []string{"headless", "command", "accepted"} {
		t.Run(entry, func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueText("must not run")
			a := newGoalTestApp(t, worker, testkit.NewMockClient())
			installSubmissionSettings(t, a, map[string]any{"type": "command", "command": `cat > prompt-input.json; printf '%s' '{"decision":"block","reason":"SUBMISSION_DENIED","hookSpecificOutput":{"suppressOriginalPrompt":true}}'`})
			switch entry {
			case "headless":
				result, err := a.RunHeadlessWithOptions(t.Context(), "/goal forbidden secret", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
				if err == nil || result.Error.Kind != "hook_stopped" || strings.Contains(result.Error.Message, "forbidden secret") || a.session.GoalState().Current != nil {
					t.Fatalf("blocked goal ran or exposed suppressed prompt: %+v %v", result, err)
				}
			case "command":
				root := filepath.Join(a.workDir, "skills")
				path := filepath.Join(root, "guard", "SKILL.md")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("---\ndescription: Guarded workflow\n---\n!`touch "+filepath.Join(a.workDir, "rendered")+"`"), 0600); err != nil {
					t.Fatal(err)
				}
				a.registry.MustRegister(tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}), a.workDir))
				a.commandHandler = commands.NewHandler()
				a.executeCommandCtx(commands.WithRawInvocation(t.Context(), "/skill guard '$HOME'"), "skill", []string{"guard", "$HOME"})
				if _, err := os.Stat(filepath.Join(a.workDir, "rendered")); !os.IsNotExist(err) {
					t.Fatal("blocked workflow rendered shell content")
				}
			case "accepted":
				a.runAcceptedSubmitWithRecoveryIdentity(t.Context(), "inspect @missing '$HOME'", "", nil, false, "", "", "", 0, nil)
			}
			if len(worker.Calls()) != 0 || len(a.session.GetHistory()) != 0 {
				t.Fatal("blocked submission reached provider/history")
			}
		})
	}
}

func TestUserPromptSubmitModelChecksShareLimitsAndGoalUsage(t *testing.T) {
	for _, test := range []struct {
		name, answer, kind string
		cap                int
		budget             float64
	}{
		{"allow", `{"ok":true}`, "", 4, 0},
		{"deny", `{"ok":false,"reason":"policy denied"}`, "hook_stopped", 4, 0},
		{"turn-cap", `{"ok":true}`, "max_turns", 1, 0},
		{"cost-cap", `{"ok":true}`, "budget_exceeded", 4, 1e-12},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueText("Completed proof")
			verifier := testkit.NewMockClient().EnqueueText(test.answer).EnqueueText(`{"verdict":"met","reason":"Complete proof"}`)
			a := newGoalTestApp(t, worker, verifier)
			installSubmissionSettings(t, a, map[string]any{"type": "prompt", "prompt": "Verify input: $ARGUMENTS"})
			result, err := a.RunHeadlessWithOptions(t.Context(), "/goal inspect proof", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: test.cap, MaxBudgetUSD: test.budget})
			if test.kind == "" {
				if err != nil || result.Goal == nil || result.Goal.InputTokens != 30 {
					t.Fatalf("lost goal prefix usage: %+v %v", result, err)
				}
			} else if err == nil || result.Error.Kind != test.kind {
				t.Fatalf("wrong submission failure: %+v %v", result, err)
			}
			calls := len(worker.Calls()) + len(verifier.Calls())
			if result.Usage.InputTokens != calls*10 || result.Usage.ModelRounds != calls {
				t.Fatalf("lost/duplicated hook usage: %+v calls=%d", result.Usage, calls)
			}
		})
	}
}

func TestUserPromptSubmitCommandTimeoutDiscardsOutputUnlessBlocking(t *testing.T) {
	for _, policy := range []string{"continue", "block"} {
		t.Run(policy, func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueText("Done")
			a := newGoalTestApp(t, worker, testkit.NewMockClient())
			installSubmissionSettings(t, a, map[string]any{"type": "command", "timeout": .02, "onFailure": policy, "command": `printf 'discard this'; sleep 2`})
			result, err := a.RunHeadlessWithOptions(context.Background(), "read proof", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
			if policy == "continue" {
				if err != nil || len(worker.Calls()) != 1 || strings.Contains(worker.Calls()[0].Message, "discard this") {
					t.Fatalf("failed-open timeout lost behavior: %+v %v", result, err)
				}
			} else if err == nil || len(worker.Calls()) != 0 {
				t.Fatal("onFailure block reached provider")
			}
		})
	}
}

func TestUserPromptSubmitRunsForEachAutomaticGoalTurnAndCountsUsageOnce(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("First attempt").EnqueueText("Completed proof")
	verifier := testkit.NewMockClient().EnqueueText(`{"ok":true}`).EnqueueText(`{"verdict":"not_met","reason":"Check again"}`).EnqueueText(`{"ok":true}`).EnqueueText(`{"verdict":"met","reason":"Proof complete"}`)
	a := newGoalTestApp(t, worker, verifier)
	installSubmissionSettings(t, a, map[string]any{"type": "prompt", "prompt": "Check submitted turn $ARGUMENTS"})
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal complete proof", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 8})
	if err != nil || result.Usage.ModelRounds != 6 || result.Usage.InputTokens != 60 || result.Goal.InputTokens != 60 || len(verifier.Calls()) != 4 {
		t.Fatalf("automatic turn gate skipped/doubled usage: %+v %v", result, err)
	}
	if !strings.Contains(verifier.Calls()[0].Message, "/goal complete proof") || !strings.Contains(verifier.Calls()[2].Message, "Check again") {
		t.Fatal("submission did not use the current turn's text")
	}
}

func TestUserPromptSubmitBlockedAcceptedTurnSuspendsExistingGoal(t *testing.T) {
	worker := testkit.NewMockClient()
	verifier := testkit.NewMockClient().EnqueueText(`{"ok":false,"reason":"policy denied"}`)
	a := newGoalTestApp(t, worker, verifier)
	if err := a.SetGoal(t.Context(), "inspect proof"); err != nil {
		t.Fatal(err)
	}
	installSubmissionSettings(t, a, map[string]any{"type": "prompt", "prompt": "Check submitted turn $ARGUMENTS"})
	a.runAcceptedSubmitWithRecoveryIdentity(t.Context(), "inspect proof", "", nil, false, "", "", "", 0, nil)
	goal := a.session.GoalState().Current
	if goal == nil || !goal.Suspended || goal.InputTokens != 10 || len(worker.Calls()) != 0 {
		t.Fatalf("blocked pre-expansion gate left goal/accounting active: %+v", goal)
	}
}

func TestUserPromptSubmitAgentCannotActAfterConversationReplacement(t *testing.T) {
	worker := testkit.NewMockClient()
	verifier := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "proof"}).EnqueueText(`{"ok":true}`)
	a := newGoalTestApp(t, worker, verifier)
	installSubmissionSettings(t, a, map[string]any{"type": "agent", "prompt": "Inspect proof before approving input"})
	verifier.OnSend = func(context.Context) {
		a.sessionLeaseMu.Lock()
		a.advanceConversationEpoch()
		a.sessionLeaseMu.Unlock()
	}
	result, err := a.RunHeadlessWithOptions(t.Context(), "inspect proof", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	read, _ := a.registry.Get("read")
	if err == nil || result.Error.Kind != "hook_stopped" || len(worker.Calls()) != 0 || len(verifier.Calls()) != 1 || read.(*appHeadlessScriptedTool).CallCount() != 0 {
		t.Fatalf("stale prompt verifier acted in replaced conversation: %+v %v", result, err)
	}
}
