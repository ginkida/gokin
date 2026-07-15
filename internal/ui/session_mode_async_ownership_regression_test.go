package ui

import "testing"

// SessionModeCycledMsg is emitted after an asynchronous Shift+Tab/palette
// worker. A newer /set, /permissions, /sandbox, /plan, or second cycle can
// commit a versioned ConfigUpdateMsg in the gap between that worker's final
// state read and its result send. Neither an older owned result nor a legacy
// unowned completion may repaint safety over the authoritative snapshot.
func TestLateSessionModeResultCannotRollBackNewerSafetyConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  SessionModeCycledMsg
	}{
		{name: "older versioned result", msg: SessionModeCycledMsg{Mode: "normal", Revision: 41}},
		{name: "legacy result", msg: SessionModeCycledMsg{Mode: "normal"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel()

			// A newer operation has authoritatively entered YOLO. A stale
			// completion still says Normal; accepting it would falsely tell the
			// user prompts and sandbox are enabled while the backend remains unrestricted.
			_ = m.handleMessageTypes(ConfigUpdateMsg{
				Revision:            42,
				PermissionsEnabled:  false,
				SandboxEnabled:      false,
				PlanningModeEnabled: false,
			})
			_ = m.handleMessageTypes(tc.msg)

			if m.permissionsEnabled || m.sandboxEnabled || m.planningModeEnabled {
				t.Fatalf("late session-mode completion rolled back revision 42 safety state: permissions=%v sandbox=%v planning=%v",
					m.permissionsEnabled, m.sandboxEnabled, m.planningModeEnabled)
			}
		})
	}
}

func TestSessionModeResultOwnershipKeepsLegacyPreResizeCompatibility(t *testing.T) {
	m := NewModel()
	_ = m.handleMessageTypes(SessionModeCycledMsg{Mode: "yolo"})
	if m.permissionsEnabled || m.sandboxEnabled || m.sessionMode != "yolo" {
		t.Fatalf("pre-version legacy result was not applied: mode=%q permissions=%v sandbox=%v",
			m.sessionMode, m.permissionsEnabled, m.sandboxEnabled)
	}
}

func TestLatePlanningModeResultCannotRollBackNewerConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  PlanningModeToggledMsg
	}{
		{name: "older versioned result", msg: PlanningModeToggledMsg{Enabled: true, Revision: 41}},
		{name: "legacy result", msg: PlanningModeToggledMsg{Enabled: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel()
			_ = m.handleMessageTypes(ConfigUpdateMsg{
				Revision:            42,
				PermissionsEnabled:  true,
				SandboxEnabled:      true,
				PlanningModeEnabled: false,
			})
			_ = m.handleMessageTypes(tc.msg)
			if m.planningModeEnabled {
				t.Fatal("late planning-mode completion repainted revision 42")
			}
		})
	}
}

func TestPlanningModeResultOwnershipKeepsLegacyPreVersionCompatibility(t *testing.T) {
	m := NewModel()
	_ = m.handleMessageTypes(PlanningModeToggledMsg{Enabled: true})
	if !m.planningModeEnabled {
		t.Fatal("pre-version legacy planning-mode result was not applied")
	}
}
