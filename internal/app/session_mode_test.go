package app

import (
	"testing"

	"gokin/internal/config"
	"gokin/internal/permission"
)

// TestSessionMode_String pins the lowercase display names so status bar
// and toast rendering stay consistent — the TUI switches on these.
func TestSessionMode_String(t *testing.T) {
	cases := map[SessionMode]string{
		SessionModeNormal: "normal",
		SessionModePlan:   "plan",
		SessionModeYOLO:   "yolo",
	}
	for mode, want := range cases {
		if got := mode.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", mode, got, want)
		}
	}
}

// TestCurrentSessionMode_DerivedFromFlags verifies the mapping:
// planMode takes priority, then permissions-off → YOLO, else Normal.
// These are the canonical states the cycle advances between.
func TestCurrentSessionMode_DerivedFromFlags(t *testing.T) {
	cases := []struct {
		name        string
		planEnabled bool
		permsOn     bool
		want        SessionMode
	}{
		{"plan_overrides_perms", true, true, SessionModePlan},
		{"plan_overrides_yolo", true, false, SessionModePlan}, // plan wins even if perms off
		{"perms_off_is_yolo", false, false, SessionModeYOLO},
		{"perms_on_no_plan_is_normal", false, true, SessionModeNormal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newSessionModeTestApp(tc.planEnabled, tc.permsOn)
			if got := app.currentSessionMode(); got != tc.want {
				t.Errorf("currentSessionMode() = %v (%s), want %v (%s)",
					got, got.String(), tc.want, tc.want.String())
			}
		})
	}
}

// TestCycleSessionMode_WalksThroughAllThreeStates proves Shift+Tab
// cycles Normal → Plan → YOLO → Normal. This is the contract the
// user-facing documentation promises.
func TestCycleSessionMode_WalksThroughAllThreeStates(t *testing.T) {
	app := newSessionModeTestApp(false, true) // start Normal

	if m := app.currentSessionMode(); m != SessionModeNormal {
		t.Fatalf("setup: expected Normal, got %s", m.String())
	}

	// Tap 1: Normal → Plan
	if m := app.CycleSessionMode(); m != SessionModePlan {
		t.Errorf("cycle 1: expected Plan, got %s", m.String())
	}
	if !app.planningModeEnabled {
		t.Error("cycle 1 should enable plan mode")
	}

	// Tap 2: Plan → YOLO
	if m := app.CycleSessionMode(); m != SessionModeYOLO {
		t.Errorf("cycle 2: expected YOLO, got %s", m.String())
	}
	if app.planningModeEnabled {
		t.Error("cycle 2 should disable plan mode")
	}
	if app.permManager.IsEnabled() {
		t.Error("cycle 2 (YOLO) should disable permissions")
	}
	if app.config.Tools.Bash.Sandbox {
		t.Error("cycle 2 (YOLO) should disable sandbox")
	}

	// Tap 3: YOLO → Normal
	if m := app.CycleSessionMode(); m != SessionModeNormal {
		t.Errorf("cycle 3: expected Normal, got %s", m.String())
	}
	if !app.permManager.IsEnabled() {
		t.Error("cycle 3 (Normal) should re-enable permissions")
	}
	if !app.config.Tools.Bash.Sandbox {
		t.Error("cycle 3 (Normal) should re-enable sandbox")
	}
}

// TestApplySessionMode_Idempotent — calling with the current mode must
// be a no-op. Important for the startup sync path and for repeated
// Shift+Tab taps that race with UI state updates.
func TestApplySessionMode_Idempotent(t *testing.T) {
	app := newSessionModeTestApp(false, true) // Normal

	// First apply to Normal — already there, should be no-op
	app.applySessionMode(SessionModeNormal)
	if m := app.currentSessionMode(); m != SessionModeNormal {
		t.Errorf("after Normal→Normal: expected Normal, got %s", m.String())
	}

	// To Plan and back to Plan — idempotent
	app.applySessionMode(SessionModePlan)
	app.applySessionMode(SessionModePlan)
	if m := app.currentSessionMode(); m != SessionModePlan {
		t.Errorf("after Plan→Plan: expected Plan, got %s", m.String())
	}

	// To YOLO twice — idempotent
	app.applySessionMode(SessionModeYOLO)
	app.applySessionMode(SessionModeYOLO)
	if m := app.currentSessionMode(); m != SessionModeYOLO {
		t.Errorf("after YOLO→YOLO: expected YOLO, got %s", m.String())
	}
}

// TestCycleSessionMode_ReturnsActualNotIntended exposes the race-window
// bug the second pass caught: CycleSessionMode previously returned the
// "next in cycle" value it computed from the pre-apply snapshot, even
// if the actual resulting state diverged (e.g. because permManager was
// nil and couldn't be toggled off for YOLO). Now it re-reads after
// apply and returns ground truth, so callers don't render a toast
// that lies about what happened.
func TestCycleSessionMode_ReturnsActualNotIntended(t *testing.T) {
	// Setup: App with NO perm manager. YOLO would require perms-off,
	// but TogglePermissions is a no-op without permManager, so attempting
	// to enter YOLO leaves perms in its "virtual on" state. The return
	// value must reflect what actually happened, not the intended YOLO.
	cfg := &config.Config{}
	cfg.Tools.Bash.Sandbox = true
	app := &App{
		config:              cfg,
		planningModeEnabled: false, // start Normal
		permManager:         nil,   // no permissions manager
	}

	// Tap 1: Normal → Plan. Plan doesn't need permManager, so this works.
	if got := app.CycleSessionMode(); got != SessionModePlan {
		t.Errorf("cycle 1: expected Plan, got %s", got.String())
	}

	// Tap 2: Plan → YOLO (intended). But without permManager, perms can't
	// flip off → YOLO won't actually be entered. With the nil-perm-manager
	// path, currentSessionMode treats "no perm manager" as "perms on",
	// so we're stuck in Normal-shape (plan off + perms-effectively-on +
	// sandbox off from cycle code? actually sandbox-off is the YOLO
	// signal we can actually flip). Let's just assert: the RETURN value
	// matches the ACTUAL post-apply state, whatever that is.
	got := app.CycleSessionMode()
	actual := app.currentSessionMode()
	if got != actual {
		t.Errorf("CycleSessionMode returned %s but actual state is %s — return must be ground truth",
			got.String(), actual.String())
	}
}

// TestApplySessionMode_DirectTransitions verifies any-to-any jumps work,
// not just the cycle order. Useful for /permissions-style commands that
// might set state directly without going through the cycle.
func TestApplySessionMode_DirectTransitions(t *testing.T) {
	transitions := []struct {
		from, to SessionMode
	}{
		{SessionModeNormal, SessionModeYOLO},   // skip Plan
		{SessionModeYOLO, SessionModePlan},     // reverse + step
		{SessionModePlan, SessionModeNormal},   // exit plan back to Normal
	}
	for _, tc := range transitions {
		t.Run(tc.from.String()+"_to_"+tc.to.String(), func(t *testing.T) {
			app := newSessionModeTestApp(false, true) // always start Normal
			app.applySessionMode(tc.from)
			app.applySessionMode(tc.to)
			if got := app.currentSessionMode(); got != tc.to {
				t.Errorf("expected %s, got %s", tc.to.String(), got.String())
			}
		})
	}
}

// newSessionModeTestApp builds a minimal App with the three flags the
// cycle touches. No TUI, no executor, no client — CycleSessionMode
// only reads/writes app state, so we can avoid the full builder.
func newSessionModeTestApp(planEnabled, permsOn bool) *App {
	cfg := &config.Config{}
	cfg.Tools.Bash.Sandbox = permsOn // Normal has sandbox on; YOLO has it off

	permMgr := permission.NewManager(nil, permsOn)

	return &App{
		config:              cfg,
		planningModeEnabled: planEnabled,
		permManager:         permMgr,
	}
}
