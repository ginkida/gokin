package app

import (
	"io"
	"strings"
	"testing"
	"time"

	"gokin/internal/plan"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestPlanVerificationHookStopIsTerminal(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		for _, event := range []string{"PreToolUse", "PostToolUse"} {
			name := "direct/" + event
			if delegated {
				name = "delegated/" + event
			}
			t.Run(name, func(t *testing.T) {
				worker := testkit.NewMockClient().EnqueueText("Approved plan ready")
				evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"This must never be evaluated after a hook stop."}`)
				a := newGoalTestApp(t, worker, evaluator)
				p, _ := configureGoalPlan(t, a, worker)
				first := p.Steps[0]
				first.VerifyCommands = []string{"go test ./first", "go test ./second"}
				later := p.AddStepWithOptions("later step", "inspect and verify later work", false, []int{first.ID})
				later.VerifyCommands = []string{"go test ./later"}
				child := testkit.NewMockClient().EnqueueText("First step inspected").EnqueueText("Later step must not run")
				if delegated {
					runner := configureGoalPlanRunner(t, a, child)
					t.Cleanup(func() { runner.CancelAll() })
					a.config.Plan.DelegateSteps = true
				} else {
					worker.EnqueueText("First step inspected").EnqueueText("Later step must not run")
				}
				installCwdSettings(t, a, map[string]any{event: []any{fileHookGroup("Bash", `printf '%s' '{"continue":false,"stopReason":"HTTP 429 timeout is the hook explanation"}'`)}})
				shell := &verifyHookTool{appHeadlessScriptedTool: appHeadlessScriptedTool{name: "bash", results: []tools.ToolResult{
					tools.NewSuccessResult("verification passed"), tools.NewSuccessResult("must not run"), tools.NewSuccessResult("must not run"),
				}}, commands: make(chan string, 4)}
				a.registry.Unregister("bash")
				a.registry.MustRegister(shell)
				result, err := a.RunHeadlessWithOptions(t.Context(), "/goal execute and verify the plan", HeadlessOptions{
					OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 12, Timeout: 3 * time.Second,
				})
				if err == nil || result.Error == nil || result.Error.Kind != "hook_stopped" {
					t.Errorf("hook stop lost: result=%+v err=%v", result, err)
				}
				wantCommands, wantWorker := 0, 2
				if event == "PostToolUse" {
					wantCommands = 1
				}
				if delegated {
					wantWorker = 1
				}
				if shell.CallCount() != wantCommands || len(worker.Calls()) != wantWorker || len(evaluator.Calls()) != 0 || delegated && len(child.Calls()) != 1 {
					t.Errorf("work continued after stop: commands=%d worker=%d evaluator=%d child=%d", shell.CallCount(), len(worker.Calls()), len(evaluator.Calls()), len(child.Calls()))
				}
				if p.GetStatus() != plan.StatusPaused || p.GetStep(first.ID).Status == plan.StatusCompleted || p.GetStep(later.ID).Status != plan.StatusPending {
					t.Errorf("plan progressed after stop: status=%s first=%s later=%s", p.GetStatus(), p.GetStep(first.ID).Status, p.GetStep(later.ID).Status)
				}
				if strings.Contains(result.Result, "verify_commands_passed") || a.pendingCount() != 0 {
					t.Errorf("stopped verification was reported as PASS or queued more work: %+v", result)
				}
			})
		}
	}
}
