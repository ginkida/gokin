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
	// Seed at least one example so the path isn't short-circuited by emptiness.
	_ = es.LearnFromSuccess("build", "compile the project", "general", "ok", 0, 0)

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
