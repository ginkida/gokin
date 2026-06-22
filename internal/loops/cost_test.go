package loops

import (
	"strings"
	"testing"
	"time"
)

func TestRenderTokenCount(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1.0K"},
		{1234, "1.2K"},
		{999_999, "1000.0K"},
		{1_000_000, "1.0M"},
		{2_500_000, "2.5M"},
	}
	for _, tc := range cases {
		if got := renderTokenCount(tc.n); got != tc.want {
			t.Errorf("renderTokenCount(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// Lifetime token totals must accrue across iterations regardless of outcome
// (a failed iteration still spent tokens), surviving history-slice trimming.
func TestAppendIteration_AccumulatesLifetimeTokens(t *testing.T) {
	l := &Loop{ID: "x", Task: "t", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning}
	now := time.Now()
	l.AppendIteration(Iteration{N: 1, StartedAt: now, OK: true, TokensIn: 1000, TokensOut: 200})
	l.AppendIteration(Iteration{N: 2, StartedAt: now, OK: false, Transient: true, TokensIn: 500, TokensOut: 50})
	l.AppendIteration(Iteration{N: 3, StartedAt: now, OK: false, TokensIn: 300, TokensOut: 30})

	if l.TotalTokensIn != 1800 {
		t.Errorf("TotalTokensIn = %d, want 1800 (accrues on success+transient+task)", l.TotalTokensIn)
	}
	if l.TotalTokensOut != 280 {
		t.Errorf("TotalTokensOut = %d, want 280", l.TotalTokensOut)
	}
}

// Negative/zero token values must never decrement the lifetime totals.
func TestAppendIteration_TokenGuards(t *testing.T) {
	l := &Loop{ID: "x", Task: "t", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning}
	l.AppendIteration(Iteration{N: 1, StartedAt: time.Now(), OK: true, TokensIn: -5, TokensOut: 0})
	if l.TotalTokensIn != 0 || l.TotalTokensOut != 0 {
		t.Errorf("guards failed: in=%d out=%d, want 0/0", l.TotalTokensIn, l.TotalTokensOut)
	}
}

// The runner must carry SpawnResult token counts into the recorded Iteration
// and the loop's lifetime totals.
func TestRunner_PropagatesTokenUsage(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("t", ModeInterval, 3600)
	spawner := &fakeSpawner{output: "done work", ok: true, tokensIn: 4096, tokensOut: 512}
	r := NewRunner(mgr, spawner.spawn, (&fakeIdle{}).check)
	r.fireOne(t.Context(), l)

	got, _ := mgr.Get(l.ID)
	if len(got.Iterations) != 1 {
		t.Fatalf("expected 1 iteration, got %d", len(got.Iterations))
	}
	if got.Iterations[0].TokensIn != 4096 || got.Iterations[0].TokensOut != 512 {
		t.Errorf("iteration tokens = %d/%d, want 4096/512", got.Iterations[0].TokensIn, got.Iterations[0].TokensOut)
	}
	if got.TotalTokensIn != 4096 || got.TotalTokensOut != 512 {
		t.Errorf("loop lifetime tokens = %d/%d, want 4096/512", got.TotalTokensIn, got.TotalTokensOut)
	}
}

// The human-readable markdown must surface lifetime + per-iteration token spend.
func TestRenderLoopMarkdown_ShowsTokens(t *testing.T) {
	l := &Loop{
		ID: "x", Task: "t", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning,
		IterationCount: 1, SuccessCount: 1, TotalTokensIn: 12000, TotalTokensOut: 3400,
		Iterations: []Iteration{{N: 1, StartedAt: time.Now(), Duration: time.Second, OK: true, Summary: "did it", TokensIn: 12000, TokensOut: 3400}},
	}
	md := RenderLoopMarkdown(l)
	if !strings.Contains(md, "12.0K in") || !strings.Contains(md, "3.4K out") {
		t.Errorf("markdown missing lifetime token line:\n%s", md)
	}
	if !strings.Contains(md, "tok") {
		t.Errorf("markdown missing per-iteration token tag:\n%s", md)
	}
}
