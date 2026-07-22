package ui

import (
	"strings"
	"testing"
	"time"
)

// TestStatusBar_RecoveryStateHasNoDuplication pins the declutter: a provider
// recovery storm (retrying + degraded mode + half-open breaker) used to surface
// the SAME state in up to four status-bar cells (mode:recovering / ⚠ recovering
// / ⚠ RETRY 3/3 / ↻ 3/3) plus a "heartbeat" cell. It must now show exactly ONE
// retry badge with the cooldown folded in, no heartbeat/mode noise, and the
// model name instead of a duplicated standalone provider cell.
func TestStatusBar_RecoveryStateHasNoDuplication(t *testing.T) {
	m := NewModel()
	m.width = 160 // full layout (>= 120)
	m.workDir = "/tmp/proj"
	m.permissionsEnabled = false
	m.sandboxEnabled = false
	m.showTokens = false
	m.currentModel = "glm-5.2"
	m.state = StateProcessing
	m.retryAttempt = 3
	m.retryMax = 3
	m.runtimeStatus.Provider = "glm"
	m.runtimeStatus.Mode = "degraded"
	m.runtimeStatus.RequestBreaker = "half_open"
	m.runtimeStatus.HasHeartbeat = true
	m.runtimeStatus.DegradedRemaining = 4*time.Minute + 34*time.Second

	out := stripAnsi(m.renderStatusBar())

	if n := strings.Count(out, "3/3"); n != 1 {
		t.Fatalf("retry indicator must appear exactly once, found %d in %q", n, out)
	}
	if !strings.Contains(out, "retry 3/3") {
		t.Fatalf("expected consolidated 'retry 3/3' badge, got %q", out)
	}
	if !strings.Contains(out, "4m34s") {
		t.Fatalf("degraded cooldown must be folded into the retry badge, got %q", out)
	}
	for _, dup := range []string{"mode:", "heartbeat", "RETRY"} {
		if strings.Contains(out, dup) {
			t.Fatalf("recovery state must not show %q (duplicate surface), got %q", dup, out)
		}
	}
	if !strings.Contains(out, "glm-5.2") {
		t.Fatalf("expected model identity glm-5.2, got %q", out)
	}
	if n := strings.Count(out, "glm"); n != 1 {
		t.Fatalf("provider must not be a separate cell — 'glm' should appear once (inside glm-5.2), found %d in %q", n, out)
	}
	if !strings.Contains(out, "YOLO") || !strings.Contains(out, "!SANDBOX") {
		t.Fatalf("safety badges should remain, got %q", out)
	}
}

// TestStatusBar_HealthyStateIsQuiet: nothing recovery-related at rest.
func TestStatusBar_HealthyStateIsQuiet(t *testing.T) {
	m := NewModel()
	m.width = 160
	m.workDir = "/tmp/proj"
	m.permissionsEnabled = true
	m.sandboxEnabled = true
	m.showTokens = false
	m.currentModel = "glm-5.2"
	m.runtimeStatus.Provider = "glm"
	m.runtimeStatus.Mode = "normal"

	out := stripAnsi(m.renderStatusBar())

	for _, noise := range []string{"retry", "recovering", "heartbeat", "mode:", "IDLE", "circuit"} {
		if strings.Contains(out, noise) {
			t.Fatalf("healthy bar must be quiet, but contains %q: %q", noise, out)
		}
	}
	if !strings.Contains(out, "glm-5.2") {
		t.Fatalf("expected model identity, got %q", out)
	}
}

// TestStatusBar_FailoverShowsBothProviderAndModel: when the active backend
// diverges from the model family (failover), the provider IS shown alongside.
func TestStatusBar_FailoverShowsBothProviderAndModel(t *testing.T) {
	m := NewModel()
	m.width = 160
	m.workDir = "/tmp/proj"
	m.showTokens = false
	m.currentModel = "glm-5.2"
	m.runtimeStatus.Provider = "deepseek"
	m.runtimeStatus.Mode = "normal"

	out := stripAnsi(m.renderStatusBar())
	if !strings.Contains(out, "deepseek→glm-5.2") {
		t.Fatalf("failover should show provider→model, got %q", out)
	}
}

// v0.100.108: the context label is ONE number — the full live context
// (prompt + completion). The old "+N" output tail was systematically
// under-read and could be dropped under width pressure.
func TestFormatAbsoluteTokens_SumsCompletion(t *testing.T) {
	if got := formatAbsoluteTokens(49_000, 1_000_000, 12_000); got != "61.0K/1.0M" {
		t.Fatalf("label = %q, want the 61.0K/1.0M sum", got)
	}
	if got := formatAbsoluteTokens(49_000, 1_000_000, 0); got != "49.0K/1.0M" {
		t.Fatalf("no-output label = %q", got)
	}
}

// v0.100.109: the identity segment shows the bare model when it belongs to
// the active provider — the old prefix heuristic branded kimi's k3 as a
// permanent mismatch and rendered a redundant "kimi→k3" on every frame. The
// arrow form remains for a REAL provider/model mismatch (failover).
func TestIdentitySegment_NoArrowForOwnModel(t *testing.T) {
	m := NewModel()
	m.currentModel = "k3"
	m.runtimeStatus.Provider = "kimi"
	if got := stripAnsi(m.identitySegment()); got != "k3" {
		t.Fatalf("identity = %q, want bare k3 (model belongs to the provider)", got)
	}

	m.currentModel = "glm-5.2"
	m.runtimeStatus.Provider = "kimi" // genuine mismatch → arrow stays
	if got := stripAnsi(m.identitySegment()); got != "kimi→glm-5.2" {
		t.Fatalf("identity = %q, want the failover arrow for a real mismatch", got)
	}

	m.currentModel = "my-custom-tag"
	m.runtimeStatus.Provider = "ollama" // unknown model → conservative prefix fallback
	if got := stripAnsi(m.identitySegment()); got != "ollama→my-custom-tag" {
		t.Fatalf("identity = %q (unknown model keeps the explicit provider)", got)
	}
}
