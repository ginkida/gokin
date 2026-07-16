package app

import (
	"testing"

	"gokin/internal/plan"
)

// TestHandleSubAgentActivity_TouchesStepHeartbeat (round 6) pins the fix for
// the plan watchdog false-kill bug: delegated plan steps (Plan.DelegateSteps,
// the DEFAULT) run their actual work through a spawned sub-agent, whose tool
// activity reaches handleSubAgentActivity — NOT the foreground executor's
// OnToolStart/OnToolEnd (buildExecutionHandler), which was the ONLY place
// touching the step heartbeat. Any delegated step whose sub-agent worked for
// longer than stepStuckTimeout (3min, well under the 5min default step
// timeout) was falsely flagged "stuck" by the plan watchdog, which pauses
// the WHOLE plan and cancels the step's context mid-work.
func TestHandleSubAgentActivity_TouchesStepHeartbeat(t *testing.T) {
	for _, status := range []string{"start", "tool_start", "tool_end"} {
		t.Run(status, func(t *testing.T) {
			a := &App{}
			// lastStepHeartbeat starts at the zero value (never touched).

			a.handleSubAgentActivity("agent-1", "general", "do work", "read", map[string]any{"file_path": "x.go"}, status, true, "ok")

			a.stepHeartbeatMu.RLock()
			touched := !a.lastStepHeartbeat.IsZero()
			a.stepHeartbeatMu.RUnlock()
			if !touched {
				t.Fatalf("handleSubAgentActivity(status=%q) did not touch the step heartbeat", status)
			}
		})
	}
}

// TestHandleSubAgentActivity_RecordsStepEffectWhenSingleDelegatedStepInFlight
// (round 6) pins the RunLedger/rollback-snapshot fix: with exactly one
// delegated step in flight (the common case — a linear plan, or the
// safe-mode/single-ready-step path), a sub-agent's tool_start must record
// the step's side effect exactly like the foreground executor's OnToolStart
// already does — otherwise the idempotency guard (HasPartialEffects /
// HasDuplicateRisk) is permanently dead for delegated execution.
func TestHandleSubAgentActivity_RecordsStepEffectWhenSingleDelegatedStepInFlight(t *testing.T) {
	mgr := plan.NewManager(true, false)
	p := mgr.CreatePlan("Test Plan", "desc", "request")
	p.Steps = []*plan.Step{{ID: 1, Title: "step 1"}}
	mgr.SetPlan(p)
	mgr.SetExecutionMode(true)
	mgr.SetCurrentStepID(1)

	a := &App{planManager: mgr}
	a.inFlightDelegatedSteps.Store(1)

	a.handleSubAgentActivity("agent-1", "general", "do work", "write", map[string]any{"file_path": "x.go"}, "tool_start", true, "")

	if !p.HasPartialEffects(1) {
		t.Fatal("expected the sub-agent's tool call to be recorded as a partial effect on step 1")
	}
}

func TestHandleSubAgentActivity_ReadOnlyToolIsNotPartialEffect(t *testing.T) {
	mgr := plan.NewManager(true, false)
	p := mgr.CreatePlan("Test Plan", "desc", "request")
	p.Steps = []*plan.Step{{ID: 1, Title: "step 1"}}
	mgr.SetPlan(p)
	mgr.SetExecutionMode(true)
	mgr.SetCurrentStepID(1)

	a := &App{planManager: mgr}
	a.inFlightDelegatedSteps.Store(1)
	a.handleSubAgentActivity("agent-1", "explore", "inspect", "read", map[string]any{"file_path": "x.go"}, "tool_start", true, "")

	if p.HasPartialEffects(1) {
		t.Fatal("read-only inspection was recorded as a retry-unsafe side effect")
	}
}

// TestHandleSubAgentActivity_SkipsStepEffectWhenMultipleDelegatedStepsInFlight
// (round 6) pins the mis-attribution guard: when 2+ delegated steps run in
// parallel (message_processor.go's non-safe-mode branch for multiple ready
// steps), planManager.GetCurrentStepID() is ambiguous — it reflects
// whichever step goroutine most recently called SetCurrentStepID, not
// necessarily the step this sub-agent's activity belongs to. Recording under
// the wrong step would corrupt RunLedger/rollback data, which is worse than
// not recording — so this must degrade gracefully instead.
func TestHandleSubAgentActivity_SkipsStepEffectWhenMultipleDelegatedStepsInFlight(t *testing.T) {
	mgr := plan.NewManager(true, false)
	p := mgr.CreatePlan("Test Plan", "desc", "request")
	p.Steps = []*plan.Step{{ID: 1, Title: "step 1"}, {ID: 2, Title: "step 2"}}
	mgr.SetPlan(p)
	mgr.SetExecutionMode(true)
	mgr.SetCurrentStepID(2) // some OTHER concurrently-running step goroutine set this last

	a := &App{planManager: mgr}
	a.inFlightDelegatedSteps.Store(2) // two delegated steps running in parallel

	a.handleSubAgentActivity("agent-1", "general", "do work", "write", map[string]any{"file_path": "x.go"}, "tool_start", true, "")

	if p.HasPartialEffects(1) || p.HasPartialEffects(2) {
		t.Fatal("expected NO step-effect recording while multiple delegated steps are in flight (mis-attribution risk)")
	}
}

// TestHandleSubAgentActivity_NoStepEffectWhenNoPlanExecuting confirms the
// existing guard (planManager.IsExecuting()) still gates recording when no
// plan is running at all — e.g. a task-tool/coordinate/loop sub-agent
// unrelated to any plan.
func TestHandleSubAgentActivity_NoStepEffectWhenNoPlanExecuting(t *testing.T) {
	mgr := plan.NewManager(true, false)
	p := mgr.CreatePlan("Test Plan", "desc", "request")
	p.Steps = []*plan.Step{{ID: 1, Title: "step 1"}}
	mgr.SetPlan(p)
	// Deliberately NOT calling SetExecutionMode(true).
	mgr.SetCurrentStepID(1)

	a := &App{planManager: mgr}
	a.inFlightDelegatedSteps.Store(1)

	a.handleSubAgentActivity("agent-1", "general", "do work", "write", map[string]any{"file_path": "x.go"}, "tool_start", true, "")

	if p.HasPartialEffects(1) {
		t.Fatal("expected no step-effect recording when no plan is executing")
	}
}
