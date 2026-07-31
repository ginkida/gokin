package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/plan"
)

func TestPlanPersistenceFailureBecomesHeadlessTerminalOutcome(t *testing.T) {
	base := t.TempDir()
	store, err := plan.NewPlanStore(base)
	if err != nil {
		t.Fatal(err)
	}
	manager := plan.NewManager(true, false)
	manager.SetPlanStore(store)
	manager.CreatePlan("persist me", "description", "request")

	plansDir := filepath.Join(base, "plans")
	if err := os.Rename(plansDir, plansDir+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plansDir, []byte("block directory recreation"), 0o600); err != nil {
		t.Fatal(err)
	}

	application := &App{planManager: manager}
	if err := application.beginHeadlessPolicyTracking(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		application.mu.Lock()
		application.endHeadlessPolicyTrackingLocked()
		application.mu.Unlock()
	}()

	if err := application.saveCurrentPlanWithVisibility("test mutation"); err == nil {
		t.Fatal("plan persistence failure was hidden")
	}
	terminal := application.headlessTerminalOutcomeSnapshot()
	if terminal == nil || terminal.Kind != "persistence_failed" {
		t.Fatalf("headless terminal outcome = %+v, want persistence_failed", terminal)
	}
}

func TestPlanPersistenceFailureNotificationIsOncePerStreak(t *testing.T) {
	application := &App{}
	const key = "current_plan"

	if !application.persistenceFailures.shouldNotify(key, true) {
		t.Fatal("first failure in streak was not reportable")
	}
	if application.persistenceFailures.shouldNotify(key, true) {
		t.Fatal("repeated failure in streak would spam the user")
	}
	if application.persistenceFailures.shouldNotify(key, false) {
		t.Fatal("healthy reset unexpectedly reported a failure")
	}
	if !application.persistenceFailures.shouldNotify(key, true) {
		t.Fatal("new failure after recovery was not reportable")
	}
}

func TestRuntimeHealthReportIncludesPersistenceState(t *testing.T) {
	base := t.TempDir()
	store, err := plan.NewPlanStore(base)
	if err != nil {
		t.Fatal(err)
	}
	manager := plan.NewManager(true, false)
	manager.SetPlanStore(store)
	application := &App{
		journal:     &ExecutionJournal{},
		planManager: manager,
	}
	application.persistenceFailures.shouldNotify("recovery_snapshot", true)
	application.persistenceFailures.shouldNotify("current_plan", true)

	report := application.GetRuntimeHealthReport()
	for _, want := range []string{
		"execution_journal: healthy",
		"recovery_snapshot: failing",
		"plan_store: failing",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("runtime health report missing %q:\n%s", want, report)
		}
	}
}
