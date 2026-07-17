package app

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/agent"
)

// loopFailureIsTransient must treat the iteration's OWN timeout (iterationCtx
// deadline) as transient (→ loop backoff, not the task-failure auto-pause), keep
// provider overloads transient, and — the Fix #1 boundary — treat the "reached
// maximum turn limit" error as a genuine TASK failure (a task that can't fit the
// turn budget IS a task problem and should count toward auto-pause).
func TestLoopFailureIsTransient_Classification(t *testing.T) {
	cases := []struct {
		name              string
		result            *agent.AgentResult
		spawnErr, waitErr error
		want              bool
	}{
		{"deadline in result.Error", &agent.AgentResult{Error: context.DeadlineExceeded.Error()}, nil, nil, true},
		{"deadline in spawnErr", nil, context.DeadlineExceeded, nil, true},
		{"deadline in waitErr", nil, nil, context.DeadlineExceeded, true},
		{"overload stays transient", &agent.AgentResult{Error: "model response error: GLM server overloaded"}, nil, nil, true},
		{"max-turn-limit is a TASK failure (Fix #1 boundary)", &agent.AgentResult{Error: "reached maximum turn limit (25 turns)"}, nil, nil, false},
		{"plain task failure", &agent.AgentResult{Error: "compile error: undefined symbol Foo"}, nil, nil, false},
		{"no error", &agent.AgentResult{}, nil, nil, false},
		{"nil result", nil, nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := loopFailureIsTransient(tc.result, tc.spawnErr, tc.waitErr); got != tc.want {
				t.Fatalf("loopFailureIsTransient = %v, want %v", got, tc.want)
			}
		})
	}
}

// v0.100.102: a reasoning-heavy iteration clipped by the deadline with NO
// final text must still surface its partial work in the summary instead of a
// bare spawn error.
func TestLoopPartialWorkSummary(t *testing.T) {
	if got := loopPartialWorkSummary(nil); got != "" {
		t.Fatalf("nil result should synthesize nothing, got %q", got)
	}
	if got := loopPartialWorkSummary(&agent.AgentResult{}); got != "" {
		t.Fatalf("empty result should synthesize nothing, got %q", got)
	}
	res := &agent.AgentResult{
		MutatingToolCalls: 4,
		TouchedPaths:      []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go"},
		InputTokens:       612345,
	}
	got := loopPartialWorkSummary(res)
	for _, want := range []string{"cut off before a final answer", "4 mutating tool call(s)", "a.go", "(+2 more)", "~612k tokens in"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q missing %q", got, want)
		}
	}
}
