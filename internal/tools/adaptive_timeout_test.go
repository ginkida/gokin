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

func TestAdaptiveToolTimeout_ZeroBaseIsIdempotent(t *testing.T) {
	// Edge: zero baseline. Any p95 still passes through since base×2 = 0,
	// cap would be 0. Result: 5×p95 (bounded by cap 0 which is ≤ anything).
	// This is a degenerate config, but must not panic.
	got := adaptiveToolTimeout(0, 1*time.Second, true)
	// cap = 0, so timeout gets clamped back to 0.
	if got != 0 {
		t.Errorf("got %v, want 0 (degenerate base=0 clamps to cap=0)", got)
	}
}
