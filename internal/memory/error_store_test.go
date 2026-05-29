package memory

import (
	"strings"
	"testing"
	"time"
)

func newTestErrorStore(t *testing.T) *ErrorStore {
	t.Helper()
	es, err := NewErrorStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewErrorStore: %v", err)
	}
	// Flush stops the 2s debounced save timer; without this it can fire after
	// the test and write into t.TempDir mid-cleanup ("directory not empty").
	t.Cleanup(func() { _ = es.Flush() })
	return es
}

func TestErrorStoreLearnAndGet(t *testing.T) {
	es := newTestErrorStore(t)
	if es.Count() != 0 {
		t.Fatalf("fresh store Count = %d, want 0", es.Count())
	}

	if err := es.LearnError("build", "undefined: foo", "declare foo", []string{"go"}); err != nil {
		t.Fatalf("LearnError: %v", err)
	}
	if es.Count() != 1 {
		t.Fatalf("Count after one learn = %d, want 1", es.Count())
	}

	// Case-insensitive substring match: the pattern is contained in the message.
	matches := es.GetLearnedErrors("./main.go:10: UNDEFINED: FOO in expression")
	if len(matches) != 1 {
		t.Fatalf("GetLearnedErrors matched %d, want 1", len(matches))
	}
	if matches[0].Solution != "declare foo" {
		t.Errorf("matched solution = %q, want %q", matches[0].Solution, "declare foo")
	}

	// A message that doesn't contain the pattern returns nothing.
	if got := es.GetLearnedErrors("totally unrelated error"); len(got) != 0 {
		t.Errorf("unrelated message matched %d entries, want 0", len(got))
	}
}

func TestErrorStoreLearnDedup(t *testing.T) {
	es := newTestErrorStore(t)
	_ = es.LearnError("build", "undefined: foo", "old solution", nil)
	_ = es.LearnError("build", "undefined: foo", "new solution", []string{"go"})

	if es.Count() != 1 {
		t.Fatalf("Count after duplicate learn = %d, want 1 (same type+pattern updates in place)", es.Count())
	}
	matches := es.GetLearnedErrors("undefined: foo")
	if len(matches) != 1 || matches[0].Solution != "new solution" {
		t.Fatalf("dedup did not update solution: %+v", matches)
	}
}

func TestErrorStoreRecordSuccessFailure(t *testing.T) {
	es := newTestErrorStore(t)
	_ = es.LearnError("test", "panic: nil map", "init the map", nil)
	id := es.GetLearnedErrors("panic: nil map")[0].ID

	// Fresh entry starts at the neutral 0.5.
	if got := es.GetLearnedErrors("panic: nil map")[0].SuccessRate; got != 0.5 {
		t.Fatalf("initial SuccessRate = %v, want 0.5", got)
	}

	if err := es.RecordSuccess(id); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
	if got := es.GetLearnedErrors("panic: nil map")[0].SuccessRate; got <= 0.5 {
		t.Errorf("SuccessRate after success = %v, want > 0.5", got)
	}

	es2 := newTestErrorStore(t)
	_ = es2.LearnError("test", "panic: nil map", "init the map", nil)
	id2 := es2.GetLearnedErrors("panic: nil map")[0].ID
	if err := es2.RecordFailure(id2); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if got := es2.GetLearnedErrors("panic: nil map")[0].SuccessRate; got >= 0.5 {
		t.Errorf("SuccessRate after failure = %v, want < 0.5", got)
	}

	// Unknown ID is an error, not a panic.
	if err := es.RecordSuccess("does-not-exist"); err == nil {
		t.Error("RecordSuccess(unknown) returned nil, want error")
	}
	if err := es.RecordFailure("does-not-exist"); err == nil {
		t.Error("RecordFailure(unknown) returned nil, want error")
	}
}

func TestErrorStoreGetByTypeAndClear(t *testing.T) {
	es := newTestErrorStore(t)
	_ = es.LearnError("build", "undefined: a", "fix a", nil)
	_ = es.LearnError("build", "undefined: b", "fix b", nil)
	_ = es.LearnError("lint", "unused var", "remove it", nil)

	if got := es.GetByType("build"); len(got) != 2 {
		t.Errorf("GetByType(build) = %d, want 2", len(got))
	}
	if got := es.GetByType("lint"); len(got) != 1 {
		t.Errorf("GetByType(lint) = %d, want 1", len(got))
	}
	if got := es.GetByType("missing"); got != nil {
		t.Errorf("GetByType(missing) = %v, want nil", got)
	}

	if err := es.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if es.Count() != 0 {
		t.Errorf("Count after Clear = %d, want 0", es.Count())
	}
	if got := es.GetByType("build"); got != nil {
		t.Errorf("GetByType after Clear = %v, want nil", got)
	}
}

func TestErrorStorePruneOldEntries(t *testing.T) {
	es := newTestErrorStore(t)
	_ = es.LearnError("a", "old-low", "s", nil)  // old + low success → pruned
	_ = es.LearnError("b", "old-high", "s", nil) // old + high success → kept
	_ = es.LearnError("c", "recent", "s", nil)   // recent → kept

	old := time.Now().Add(-2 * time.Hour)
	for _, e := range es.GetByType("a") {
		e.LastUsed = old
		e.SuccessRate = 0.1
	}
	for _, e := range es.GetByType("b") {
		e.LastUsed = old
		e.SuccessRate = 0.9
	}

	if err := es.PruneOldEntries(1 * time.Hour); err != nil {
		t.Fatalf("PruneOldEntries: %v", err)
	}

	if got := len(es.GetLearnedErrors("old-low")); got != 0 {
		t.Errorf("old low-success entry survived prune (%d matches)", got)
	}
	if got := len(es.GetLearnedErrors("old-high")); got != 1 {
		t.Errorf("old HIGH-success entry was pruned — prune must require BOTH old AND <0.3 (%d matches)", got)
	}
	if got := len(es.GetLearnedErrors("recent")); got != 1 {
		t.Errorf("recent entry was pruned (%d matches)", got)
	}
	// byType index must stay consistent with entries after prune.
	if got := es.GetByType("a"); len(got) != 0 {
		t.Errorf("byType[a] still lists a pruned entry: %v", got)
	}
}

func TestErrorStorePersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	es, err := NewErrorStore(dir)
	if err != nil {
		t.Fatalf("NewErrorStore: %v", err)
	}
	_ = es.LearnError("build", "undefined: foo", "declare foo", []string{"go"})
	if err := es.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// A fresh store over the same dir must load the persisted entry.
	es2, err := NewErrorStore(dir)
	if err != nil {
		t.Fatalf("reopen NewErrorStore: %v", err)
	}
	if es2.Count() != 1 {
		t.Fatalf("reloaded Count = %d, want 1", es2.Count())
	}
	matches := es2.GetLearnedErrors("undefined: foo here")
	if len(matches) != 1 || matches[0].Solution != "declare foo" {
		t.Fatalf("persisted entry not restored: %+v", matches)
	}
	// byType index must be rebuilt on load.
	if got := es2.GetByType("build"); len(got) != 1 {
		t.Errorf("byType not rebuilt on load: %d entries", len(got))
	}
}

func TestErrorStoreGetErrorContext(t *testing.T) {
	es := newTestErrorStore(t)
	if got := es.GetErrorContext("anything"); got != "" {
		t.Errorf("empty store GetErrorContext = %q, want empty", got)
	}
	_ = es.LearnError("build", "undefined: foo", "declare foo", nil)
	ctx := es.GetErrorContext("undefined: foo in main")
	if ctx == "" {
		t.Fatal("GetErrorContext returned empty for a matching error")
	}
	for _, want := range []string{"Learned from Previous Errors", "declare foo", "undefined: foo"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("GetErrorContext missing %q in:\n%s", want, ctx)
		}
	}
}
