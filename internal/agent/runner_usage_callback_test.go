package agent

import (
	"strings"
	"testing"
	"time"

	"gokin/internal/client"
)

func TestRunnerAgentUsageCallbackReportsEveryInvocation(t *testing.T) {
	r := &Runner{}
	var got []int
	r.SetOnAgentUsage(func(id string, result *AgentResult) {
		if id != "same-agent" {
			t.Fatalf("callback id = %q", id)
		}
		got = append(got, result.InputTokens)
	})

	// Resume reuses an ID, but represents another billable invocation. The
	// accounting callback must not deduplicate by agent ID.
	r.reportAgentUsage("same-agent", &AgentResult{InputTokens: 100})
	r.reportAgentUsage("same-agent", &AgentResult{InputTokens: 200})
	r.reportAgentUsage("same-agent", nil)

	if len(got) != 2 || got[0] != 100 || got[1] != 200 {
		t.Fatalf("reported usage = %v, want [100 200]", got)
	}
}

func TestRunnerAgentUsageCallbackPanicIsContained(t *testing.T) {
	r := &Runner{}
	r.SetOnAgentUsage(func(string, *AgentResult) {
		panic("broken accounting integration")
	})

	// If the panic escapes, synchronous Spawn paths fail and async paths enter
	// their outer panic recovery after the agent had actually succeeded.
	r.reportAgentUsage("agent-ok", &AgentResult{Completed: true, InputTokens: 10})
}

func TestRunnerLifecycleCallbackPanicsAreContained(t *testing.T) {
	result := &AgentResult{Status: AgentStatusCompleted, Completed: true}
	progress := &AgentProgress{CurrentStep: 1}

	invokeAgentStart(func(string, string, string) { panic("start") }, "a1", "general", "task")
	invokeAgentProgress(func(string, *AgentProgress) { panic("progress") }, "a1", progress)
	invokeAgentComplete(func(string, *AgentResult) { panic("complete") }, "a1", result)
	invokeSubAgentActivity(
		func(string, string, string, string, map[string]any, string, bool, string) { panic("activity") },
		"a1", "general", "task", "read", map[string]any{"path": "x"}, "tool_start", true, "reading",
	)

	if result.Status != AgentStatusCompleted || !result.Completed {
		t.Fatalf("callback panic mutated successful result: %+v", result)
	}
	if progress.CurrentStep != 1 {
		t.Fatalf("callback panic mutated progress: %+v", progress)
	}
}

func TestInvokeSubAgentActivityForwardsPayload(t *testing.T) {
	called := false
	invokeSubAgentActivity(func(id, typ, prompt, tool string, args map[string]any, status string, success bool, summary string) {
		called = id == "a1" && typ == "explore" && prompt == "find bug" && tool == "grep" &&
			args["pattern"] == "TODO" && status == "tool_end" && success && summary == "1 match"
	}, "a1", "explore", "find bug", "grep", map[string]any{"pattern": "TODO"}, "tool_end", true, "1 match")
	if !called {
		t.Fatal("activity callback payload was not forwarded intact")
	}
}

func TestAgentStreamingAndToolCallbackPanicsAreContained(t *testing.T) {
	a := &Agent{ID: "a1"}
	a.SetOnText(func(string) { panic("text") })
	a.SetOnThinking(func(string) { panic("thinking") })
	a.SetOnRateLimit(func(*client.RateLimitMetadata) { panic("rate") })

	a.safeOnText("hello")
	a.safeOnThinking("reasoning")
	a.safeOnRateLimit(&client.RateLimitMetadata{})
	invokeAgentToolActivity(
		func(string, string, map[string]any, string, bool, string) { panic("tool") },
		"a1", "read", map[string]any{"path": "x"}, "start", false, "",
	)

	if a.Thought != "reasoning" {
		t.Fatalf("thinking accumulation was lost after callback panic: %q", a.Thought)
	}
}

func TestAgentInputCallbackPanicBecomesError(t *testing.T) {
	response, err := invokeAgentInput(func(string) (string, error) {
		panic("input UI failed")
	}, "a1", "approve?")
	if err == nil || !strings.Contains(err.Error(), "input UI failed") {
		t.Fatalf("input panic result = (%q, %v), want descriptive error", response, err)
	}
}

func TestAgentProgressPlanAndMetaCallbackPanicsAreContained(t *testing.T) {
	a := &Agent{ID: "a1"}
	a.SetProgressCallback(func(*AgentProgress) { panic("progress") })
	a.SetProgress(1, 2, "working")

	invokeAgentPlanApproved(func(string) { panic("plan") }, "a1", "summary")
	invokeMetaStuckCallback(func(string, time.Duration) { panic("stuck") }, "a1", time.Minute)
	invokeMetaInterventionCallback(
		func(string, string, string, string) { panic("intervention") },
		"a1", "idle", "steer", "continue",
	)

	progress := a.GetProgress()
	if progress.CurrentStep != 1 || progress.TotalSteps != 2 {
		t.Fatalf("progress state lost after callback panic: %+v", progress)
	}
}
