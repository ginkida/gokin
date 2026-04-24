package evals

import (
	"path/filepath"
	"testing"
)

func TestCodingEvalManifestValid(t *testing.T) {
	path := filepath.Join("..", "..", "evals", "coding", "manifest.json")
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest(%q) error = %v", path, err)
	}

	if manifest.Version <= 0 {
		t.Fatal("manifest version must be positive")
	}
	if manifest.Name == "" {
		t.Fatal("manifest name is required")
	}
	if len(manifest.Metrics) < 4 {
		t.Fatalf("metrics = %v, want at least 4", manifest.Metrics)
	}
	if len(manifest.Scenarios) < 5 {
		t.Fatalf("scenarios = %d, want at least 5", len(manifest.Scenarios))
	}

	seenIDs := make(map[string]bool, len(manifest.Scenarios))
	seenCategories := make(map[string]bool)
	for _, scenario := range manifest.Scenarios {
		if scenario.ID == "" {
			t.Fatal("scenario id is required")
		}
		if seenIDs[scenario.ID] {
			t.Fatalf("duplicate scenario id %q", scenario.ID)
		}
		seenIDs[scenario.ID] = true
		seenCategories[scenario.Category] = true

		if scenario.Category == "" || scenario.Difficulty == "" || scenario.Prompt == "" || scenario.Fixture == "" {
			t.Fatalf("scenario %q missing required metadata: %+v", scenario.ID, scenario)
		}
		if len(scenario.ExpectedBehaviors) == 0 {
			t.Fatalf("scenario %q missing expected behaviors", scenario.ID)
		}
		if len(scenario.VerificationCommands) == 0 {
			t.Fatalf("scenario %q missing verification commands", scenario.ID)
		}
		if len(scenario.SuccessCriteria) == 0 {
			t.Fatalf("scenario %q missing success criteria", scenario.ID)
		}
		if len(scenario.FailureSignals) == 0 {
			t.Fatalf("scenario %q missing failure signals", scenario.ID)
		}
		if scenario.MaxToolCalls <= 0 {
			t.Fatalf("scenario %q max_tool_calls must be positive", scenario.ID)
		}
	}

	for _, category := range []string{"bugfix", "refactor", "test_repair", "feature", "failing_build"} {
		if !seenCategories[category] {
			t.Fatalf("missing required category %q", category)
		}
	}
}
