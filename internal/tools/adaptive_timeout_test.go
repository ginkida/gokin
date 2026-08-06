package tools

import (
	"testing"
	"time"

	"gokin/internal/config"
)

func TestAdaptiveToolTimeout_NoStatsReturnsBase(t *testing.T) {
	base := 30 * time.Second
	got := adaptiveToolTimeout(base, 0, false)
	if got != base {
		t.Errorf("got %v, want %v (no stats → base)", got, base)
	}
}

func TestAdaptiveToolTimeout_ZeroP95ReturnsBase(t *testing.T) {
	base := 30 * time.Second
	// ok=true but p95 is zero — still return base.
	got := adaptiveToolTimeout(base, 0, true)
	if got != base {
		t.Errorf("got %v, want %v (zero p95 → base)", got, base)
	}
}

func TestAdaptiveToolTimeout_NegativeP95ReturnsBase(t *testing.T) {
	base := 30 * time.Second
	got := adaptiveToolTimeout(base, -10*time.Millisecond, true)
	if got != base {
		t.Errorf("got %v, want %v (negative p95 → base)", got, base)
	}
}

func TestAdaptiveToolTimeout_SmallP95NoChange(t *testing.T) {
	// p95 = 1s → 5×p95 = 5s < 30s base. Should stay at base.
	base := 30 * time.Second
	got := adaptiveToolTimeout(base, 1*time.Second, true)
	if got != base {
		t.Errorf("got %v, want %v (small p95 keeps base)", got, base)
	}
}

func TestAdaptiveToolTimeout_MediumP95StretchesUp(t *testing.T) {
	// p95 = 10s → 5×p95 = 50s > 30s base. Cap = 60s (base×2). 50 < 60 → use 50.
	base := 30 * time.Second
	got := adaptiveToolTimeout(base, 10*time.Second, true)
	want := 50 * time.Second
	if got != want {
		t.Errorf("got %v, want %v (5×p95 under cap)", got, want)
	}
}

func TestAdaptiveToolTimeout_LargeP95HitsCap(t *testing.T) {
	// p95 = 60s → 5×p95 = 300s. Cap = 2×base = 60s. Result should cap at 60s.
	base := 30 * time.Second
	got := adaptiveToolTimeout(base, 60*time.Second, true)
	want := 60 * time.Second
	if got != want {
		t.Errorf("got %v, want %v (cap at 2×base)", got, want)
	}
}

func TestAdaptiveToolTimeout_ExactlyAtCapStays(t *testing.T) {
	// Boundary: adaptive == cap. Should keep cap.
	base := 30 * time.Second
	// 5×p95 = 2×base → p95 = 2×base/5 = 12s.
	got := adaptiveToolTimeout(base, 12*time.Second, true)
	want := 60 * time.Second
	if got != want {
		t.Errorf("got %v, want %v (boundary)", got, want)
	}
}

func TestAdaptiveToolTimeout_PastCapRollsBackToCap(t *testing.T) {
	// Even extreme p95 (1 hour) caps at 2×base.
	base := 30 * time.Second
	got := adaptiveToolTimeout(base, 1*time.Hour, true)
	if got != 60*time.Second {
		t.Errorf("got %v, want 60s (cap)", got)
	}
}

func TestAdaptiveToolTimeout_NonPositiveBaseFallsBackToDefault(t *testing.T) {
	// A non-positive base (e.g. a config with tools.timeout: 0s) must NEVER
	// yield a 0 timeout — that produced an already-expired context that made
	// every tool call fail instantly with "context deadline exceeded". The
	// helper falls back to defaultToolExecTimeout instead.
	for _, base := range []time.Duration{0, -5 * time.Second} {
		if got := adaptiveToolTimeout(base, 1*time.Second, true); got != defaultToolExecTimeout {
			t.Errorf("base=%v: got %v, want %v (safe default)", base, got, defaultToolExecTimeout)
		}
		if got := adaptiveToolTimeout(base, 0, false); got != defaultToolExecTimeout {
			t.Errorf("base=%v no-stats: got %v, want %v (safe default)", base, got, defaultToolExecTimeout)
		}
	}
}

func TestToolExecutionTimeoutHonorsLongOperationBudget(t *testing.T) {
	base := 30 * time.Second
	if got, want := toolExecutionTimeout(base, 0, false, "run_tests", nil),
		DefaultRunTestsTimeout+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("run_tests outer timeout = %v, want %v", got, want)
	}
	if got, want := toolExecutionTimeout(base, 0, false, "run_tests", map[string]any{"timeout_seconds": 900}),
		15*time.Minute+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("requested run_tests outer timeout = %v, want %v", got, want)
	}
	if got, want := toolExecutionTimeout(base, 0, false, "bash", map[string]any{
		"command": "cargo test --workspace 2>&1 | tail -20",
	}), DefaultRunTestsTimeout+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("direct verification bash outer timeout = %v, want %v", got, want)
	}
	if got, want := toolExecutionTimeout(base, 0, false, "verify_code", nil),
		DefaultVerifyCodeTimeout+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("verify_code outer timeout = %v, want %v", got, want)
	}
	if got, want := toolExecutionTimeout(base, 0, false, "coordinate", nil),
		DefaultCoordinateTimeout+coordinateCleanupTimeout+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("coordinate outer timeout = %v, want %v", got, want)
	}
	if got, want := toolExecutionTimeout(base, 0, false, "coordinate", map[string]any{"timeout_minutes": 30}),
		30*time.Minute+coordinateCleanupTimeout+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("requested coordinate outer timeout = %v, want %v", got, want)
	}
	if got, want := toolExecutionTimeout(base, 0, false, "task", map[string]any{
		"subagent_type": "bash",
	}), config.DefaultAgentTimeout+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("normal bash task outer timeout = %v, want %v", got, want)
	}
	if got, want := toolExecutionTimeout(base, 0, false, "task", map[string]any{
		"subagent_type": "general",
	}), config.DefaultAgentTimeout+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("normal general task outer timeout = %v, want %v", got, want)
	}
	if got, want := toolExecutionTimeout(base, 0, false, "task", map[string]any{
		"subagent_type": "general",
		"thoroughness":  "quick",
	}), 2*time.Minute+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("quick general task outer timeout = %v, want %v", got, want)
	}
	if got, want := toolExecutionTimeout(base, 0, false, "task", map[string]any{
		"subagent_type": "general",
		"thoroughness":  "thorough",
	}), config.DefaultThoroughAgentTimeout+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("thorough general task outer timeout = %v, want %v", got, want)
	}
	if got, want := toolExecutionTimeout(base, 0, false, "task", map[string]any{
		"subagent_type": "bash",
		"thoroughness":  "thorough",
	}), config.DefaultThoroughAgentTimeout+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("thorough bash task outer timeout = %v, want %v", got, want)
	}
	if got, want := toolExecutionTimeout(base, 0, false, "task", map[string]any{
		"subagent_type": "dynamic-reviewer",
		"thoroughness":  "thorough",
	}), config.DefaultThoroughAgentTimeout+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("thorough dynamic task outer timeout = %v, want %v", got, want)
	}
	if got := toolExecutionTimeout(base, 0, false, "task", map[string]any{
		"subagent_type":     "bash",
		"run_in_background": true,
	}); got != base {
		t.Fatalf("background task outer timeout = %v, want base %v", got, base)
	}
	if got, want := toolExecutionTimeout(base, 0, false, "task_output", map[string]any{
		"block": true,
	}), DefaultTaskOutputWaitTimeout+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("blocking task_output outer timeout = %v, want %v", got, want)
	}
	if got, want := toolExecutionTimeout(base, 0, false, "task_output", map[string]any{
		"block":      true,
		"timeout_ms": 600000,
	}), MaxTaskOutputWaitTimeout+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("long blocking task_output outer timeout = %v, want %v", got, want)
	}
	if got := toolExecutionTimeout(base, 0, false, "task_output", nil); got != base {
		t.Fatalf("non-blocking task_output outer timeout = %v, want base %v", got, base)
	}
	if got, want := toolExecutionTimeout(base, 0, false, "ssh", map[string]any{
		"timeout": 600,
	}), 10*time.Minute+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("foreground ssh outer timeout = %v, want %v", got, want)
	}
	if got := toolExecutionTimeout(base, 0, false, "ssh", map[string]any{
		"timeout":           600,
		"run_in_background": true,
	}); got != base {
		t.Fatalf("background ssh outer timeout = %v, want base %v", got, base)
	}
	for _, test := range []struct {
		tool string
		wait time.Duration
	}{
		{tool: "ask_user", wait: 10 * time.Minute},
		{tool: "enter_plan_mode", wait: 10 * time.Minute},
		{tool: "write", wait: 5 * time.Minute},
		{tool: "edit", wait: 5 * time.Minute},
	} {
		if got, want := toolExecutionTimeout(base, 0, false, test.tool, nil),
			test.wait+toolTimeoutCompletionGrace; got != want {
			t.Fatalf("%s outer timeout = %v, want %v", test.tool, got, want)
		}
	}
	if got, want := toolExecutionTimeout(base, 0, false, "bash", map[string]any{"timeout_seconds": 600}),
		10*time.Minute+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("requested bash outer timeout = %v, want %v", got, want)
	}
	if got, want := toolExecutionTimeout(base, 0, false, "bash", nil),
		base+toolTimeoutCompletionGrace; got != want {
		t.Fatalf("ordinary bash outer timeout = %v, want %v", got, want)
	}
	if got, want := toolExecutionTimeout(base, 10*time.Second, true, "bash", nil),
		50*time.Second; got != want {
		t.Fatalf("adaptive bash outer timeout = %v, want %v", got, want)
	}
}

func TestExecutorDynamicInteractiveWaitBudget(t *testing.T) {
	executor := NewExecutor(NewRegistry(), nil, 30*time.Second)
	executor.SetToolWaitBudgetResolver(func(tool string, _ map[string]any) (time.Duration, bool) {
		switch tool {
		case "ask_user":
			return 30 * time.Minute, true
		case "indefinite":
			return 0, true
		default:
			return 0, false
		}
	})

	if got, deadline := executor.resolveToolExecutionTimeout(
		30*time.Second, 0, false, "ask_user", nil,
	); !deadline || got != 30*time.Minute+toolTimeoutCompletionGrace {
		t.Fatalf("dynamic ask_user timeout = %v/%t", got, deadline)
	}
	if got, deadline := executor.resolveToolExecutionTimeout(
		30*time.Second, 0, false, "indefinite", nil,
	); deadline || got != 0 {
		t.Fatalf("indefinite timeout = %v/%t, want 0/false", got, deadline)
	}
	if got, deadline := executor.resolveToolExecutionTimeout(
		30*time.Second, 0, false, "read", nil,
	); !deadline || got != 30*time.Second {
		t.Fatalf("ordinary timeout = %v/%t, want 30s/true", got, deadline)
	}
}
