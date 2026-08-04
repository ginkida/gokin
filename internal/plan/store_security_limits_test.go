package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlanStoreRejectsInvalidIDsWithoutEscapingDirectory(t *testing.T) {
	configDir := t.TempDir()
	store, err := NewPlanStore(configDir)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(configDir, "escape.json")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"", "../escape", "nested/escape", ".hidden", "план", strings.Repeat("a", maxPlanIDBytes+1)} {
		plan := NewPlan("invalid", "")
		plan.ID = id
		if err := store.Save(plan); err == nil {
			t.Errorf("Save accepted invalid ID %q", id)
		}
		if _, err := store.Load(id); err == nil {
			t.Errorf("Load accepted invalid ID %q", id)
		}
		if err := store.Delete(id); err == nil {
			t.Errorf("Delete accepted invalid ID %q", id)
		}
		if store.Exists(id) {
			t.Errorf("Exists accepted invalid ID %q", id)
		}
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "keep" {
		t.Fatalf("outside file changed: %q, %v", data, err)
	}
}

func TestPlanStoreRejectsOversizedFilesAndWrites(t *testing.T) {
	configDir := t.TempDir()
	dir := filepath.Join(configDir, "plans")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "oversized.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxPlanFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()

	// An oversized leftover must not disable plan persistence for the whole
	// install: the store still constructs, the file is excluded from listings,
	// and it is left on disk untouched for the user to inspect.
	store, err := NewPlanStore(configDir)
	if err != nil {
		t.Fatalf("one oversized file disabled the plan store: %v", err)
	}
	listed, err := store.List()
	if err != nil {
		t.Fatalf("List aborted over one oversized file: %v", err)
	}
	for _, plan := range listed {
		if plan.ID == "oversized" {
			t.Fatal("List returned the oversized plan")
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Size(); got != maxPlanFileBytes+1 {
		t.Fatalf("oversized plan was modified: size = %d", got)
	}

	freshRoot := t.TempDir()
	err = writePrivatePlanFile(filepath.Join(freshRoot, "plans", "new.json"), make([]byte, maxPlanFileBytes+1))
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized plan write error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(freshRoot, "plans")); !os.IsNotExist(err) {
		t.Fatalf("oversized write created plan directory: %v", err)
	}
}

func TestInspectStoredPlanFilesEnforcesCardinalityLimit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plans")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		path := filepath.Join(dir, fmt.Sprintf("plan-%d.json", i))
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := inspectStoredPlanFilesWithLimit(dir, false, 2); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("cardinality error = %v", err)
	}
}

func TestPlanStoreSanitizesNullDurableStateAndRejectsMismatchedID(t *testing.T) {
	store, err := NewPlanStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	valid := []byte("{\"id\":\"safe\",\"status\":5,\"steps\":[null,{\"id\":1,\"title\":\" step \",\"children\":[null]}],\"run_ledger\":{\"1\":null}}")
	if err := os.WriteFile(filepath.Join(store.dir, "safe.json"), valid, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("safe")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Steps) != 1 || len(loaded.Steps[0].Children) != 0 {
		t.Fatalf("null steps were not removed: %#v", loaded.Steps)
	}
	if loaded.Steps[0].Title != "step" || len(loaded.Steps[0].VerifyCommands) == 0 {
		t.Fatalf("step defaults were not repaired: %#v", loaded.Steps[0])
	}
	if len(loaded.RunLedger) != 0 {
		t.Fatalf("null run ledger entries were not removed: %#v", loaded.RunLedger)
	}

	mismatch := mustMarshalPlanForSecurityTest(t, &Plan{ID: "other", Status: StatusPaused})
	if err := os.WriteFile(filepath.Join(store.dir, "mismatch.json"), mismatch, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("mismatch"); err == nil {
		t.Fatal("Load accepted a plan whose ID did not match its filename")
	}
	plans, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range plans {
		if info.ID == "other" {
			t.Fatal("List exposed a plan whose ID did not match its filename")
		}
	}
}

func TestPlanStoreRejectsExcessiveStepNestingAndNegativeCleanupAge(t *testing.T) {
	store, err := NewPlanStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plan := NewPlan("deep", "")
	root := &Step{ID: 1}
	plan.Steps = []*Step{root}
	current := root
	for i := 0; i <= maxPlanStepDepth; i++ {
		child := &Step{ID: i + 2}
		current.Children = []*Step{child}
		current = child
	}
	if err := store.Save(plan); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("deep plan save error = %v", err)
	}
	if store.Exists(plan.ID) {
		t.Fatal("deep plan was persisted")
	}
	if removed, err := store.Cleanup(-time.Nanosecond); err == nil || removed != 0 {
		t.Fatalf("negative cleanup = (%d, %v)", removed, err)
	}
}

func mustMarshalPlanForSecurityTest(t *testing.T, plan *Plan) []byte {
	t.Helper()
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
