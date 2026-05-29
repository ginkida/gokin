package memory

import "testing"

// TestGetSimilarExamplesNonPositiveLimit pins the v0.85.16 fix: a non-positive
// limit (e.g. a negative ExampleLimit from config reaching the unguarded
// GetExamplesForContext path) must return nil rather than panic on
// make([]TaskExampleSummary, limit).
func TestGetSimilarExamplesNonPositiveLimit(t *testing.T) {
	es, err := NewExampleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewExampleStore: %v", err)
	}
	// No seeding: the limit<=0 guard returns before touching examples, and
	// LearnFromSuccess spawns an async save goroutine that races t.TempDir
	// cleanup. The guard is what we're testing, so an empty store is enough.

	for _, limit := range []int{-5, -1, 0} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("GetSimilarExamples(limit=%d) panicked: %v", limit, r)
				}
			}()
			if got := es.GetSimilarExamples("compile the project", limit); got != nil {
				t.Errorf("GetSimilarExamples(limit=%d) = %v, want nil", limit, got)
			}
		}()
	}
}
