package tools

import (
	"testing"
	"time"
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
