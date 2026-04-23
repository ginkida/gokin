package config

import "testing"

// Pins: a fresh DefaultConfig enables proactive context with sensible
// params. If someone flips the default off or changes the numbers, this
// test forces an explicit decision rather than a silent behaviour drift
// for every user who upgrades.
func TestDefaultConfig_ProactiveContextDefaults(t *testing.T) {
	cfg := DefaultConfig()
	pc := cfg.Tools.ProactiveContext
	if !pc.Enabled {
		t.Errorf("expected ProactiveContext.Enabled=true by default, got false")
	}
	if pc.MaxFiles != 3 {
		t.Errorf("expected MaxFiles=3, got %d", pc.MaxFiles)
	}
	if pc.MaxLinesPerFile != 40 {
		t.Errorf("expected MaxLinesPerFile=40, got %d", pc.MaxLinesPerFile)
	}
}

// Pins: evidence footer default is on. Flipping this off silently would
// remove the audit-trail under every response — every user upgrading
// the binary would see a UX regression.
func TestDefaultConfig_CompletionEvidenceFooterDefault(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Completion.EvidenceFooter {
		t.Errorf("expected Completion.EvidenceFooter=true by default, got false")
	}
}

// Pins: Kimi tool budget default. Raising it weakens the runaway guard;
// dropping to 0 disables it entirely — both changes deserve an explicit
// test-update so they can't slip in silently.
func TestDefaultConfig_KimiToolBudgetDefault(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Tools.KimiToolBudget != 40 {
		t.Errorf("expected Tools.KimiToolBudget=40 by default, got %d", cfg.Tools.KimiToolBudget)
	}
}

// Pins: Read-before-Edit is on by default. This is a behaviour-changing
// invariant — turning it off silently would re-open the blind-edit class
// of bugs this feature was added to prevent.
func TestDefaultConfig_RequireReadBeforeEditDefault(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Tools.RequireReadBeforeEdit {
		t.Errorf("expected Tools.RequireReadBeforeEdit=true by default, got false")
	}
}
