package evals

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gokin/internal/hybrid"
)

func TestHybridEvalManifestAndFixturesValid(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "evals", "hybrid", "manifest.json")
	fixturesRoot := filepath.Join("..", "..", "evals", "hybrid", "fixtures")
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest(%q): %v", manifestPath, err)
	}
	if len(manifest.Scenarios) != 8 {
		t.Fatalf("hybrid scenarios = %d, want 8", len(manifest.Scenarios))
	}
	positive, negative, operationGated := 0, 0, 0
	for _, scenario := range manifest.Scenarios {
		if scenario.HybridCandidate == nil {
			t.Fatalf("scenario %q does not declare hybrid_candidate", scenario.ID)
		}
		if *scenario.HybridCandidate {
			positive++
		} else {
			negative++
		}
		if len(scenario.HybridRequiredOperations) > 0 || len(scenario.HybridRequiredAnyOperations) > 0 {
			operationGated++
			if scenario.HybridMaxScanOperations != 1 || scenario.HybridMinFileIndexRefreshes != 1 ||
				scenario.HybridMaxReplCalls != 1 {
				t.Errorf("scenario %q efficiency contract = max scans %d/min index refreshes %d/max repl %d, want 1/1/1",
					scenario.ID, scenario.HybridMaxScanOperations,
					scenario.HybridMinFileIndexRefreshes, scenario.HybridMaxReplCalls)
			}
		}
		if scenario.ID == "repo_comment_count_rank" &&
			(len(scenario.HybridRequiredAnyOperations) != 2 ||
				scenario.HybridRequiredAnyOperations[0] != "count_code" ||
				scenario.HybridRequiredAnyOperations[1] != "count_code_many") {
			t.Errorf("comment rank alternatives=%v, want count_code/count_code_many",
				scenario.HybridRequiredAnyOperations)
		}
		if scenario.ID == "repo_source_extension_distribution" &&
			(len(scenario.HybridRequiredOperations) != 1 || scenario.HybridRequiredOperations[0] != "file_stats") {
			t.Errorf("source distribution operation=%v, want streaming file_stats", scenario.HybridRequiredOperations)
		}
		decision := hybrid.Decide("auto", scenario.Prompt)
		if decision.Enabled != *scenario.HybridCandidate {
			t.Errorf("scenario %q classification=%t, auto policy=%t (%s)",
				scenario.ID, *scenario.HybridCandidate, decision.Enabled, decision.Reason)
		}
	}
	if positive != 6 || negative != 2 || operationGated != 6 {
		t.Fatalf("hybrid policy controls = %d positive/%d negative/%d operation-gated, want 6/2/6",
			positive, negative, operationGated)
	}

	checks, err := ValidateFixtures(context.Background(), ValidateOptions{
		ManifestPath: manifestPath,
		FixturesRoot: fixturesRoot,
		Timeout:      30 * time.Second,
	})
	if err != nil {
		t.Fatalf("ValidateFixtures: %v", err)
	}
	for _, check := range checks {
		if !check.OK {
			t.Errorf("fixture %q contract failed: %s", check.ScenarioID, check.Detail)
		}
	}
}
