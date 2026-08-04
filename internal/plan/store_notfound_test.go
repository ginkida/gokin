package plan

import (
	"strings"
	"testing"
)

// The private-storage migration wrapped read errors, and os.IsNotExist cannot
// unwrap — so this branch was dead and a deleted plan surfaced an absolute
// filesystem path instead of the message the caller is designed to show.
func TestLoadMissingPlanReportsPlanNotFound(t *testing.T) {
	store, err := NewPlanStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Load("plan_does_not_exist")
	if err == nil {
		t.Fatal("Load(missing) returned no error")
	}
	if !strings.Contains(err.Error(), "plan not found: plan_does_not_exist") {
		t.Fatalf("Load(missing) = %v, want the plan-not-found message", err)
	}
	if strings.Contains(err.Error(), "inspect private file") {
		t.Fatalf("internal storage error leaked to the caller: %v", err)
	}
}
