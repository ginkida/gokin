package evals

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeValidateFixture(t *testing.T, root, name, script string) {
	t.Helper()
	dir := filepath.Join(root, "synthetic", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "check.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeValidateManifest(t *testing.T, dir string, scenarios []Scenario) string {
	t.Helper()
	m := Manifest{Version: 1, Name: "validate-test", Metrics: []string{"task_completed"}, Scenarios: scenarios}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func validateScenario(id, fixture, state string) Scenario {
	return Scenario{
		ID: id, Category: "bugfix", Difficulty: "small", Prompt: "p",
		Fixture:              "synthetic/" + fixture,
		ExpectedBehaviors:    []string{"b"},
		VerificationCommands: []string{"sh check.sh"},
		SuccessCriteria:      []string{"s"},
		FailureSignals:       []string{"f"},
		MaxToolCalls:         5,
		DeliveredState:       state,
	}
}

func TestValidateFixtures_ContractEnforcement(t *testing.T) {
	root := t.TempDir()
	fixtures := filepath.Join(root, "fixtures")
	writeValidateFixture(t, fixtures, "red-ok", "exit 1\n")     // red fixture correctly failing
	writeValidateFixture(t, fixtures, "red-rotten", "exit 0\n") // red fixture that already passes = rot
	writeValidateFixture(t, fixtures, "green-ok", "exit 0\n")   // green trap correctly passing

	manifest := writeValidateManifest(t, root, []Scenario{
		validateScenario("red_ok", "red-ok", ""),
		validateScenario("red_rotten", "red-rotten", ""),
		validateScenario("green_ok", "green-ok", "green"),
	})

	checks, err := ValidateFixtures(context.Background(), ValidateOptions{
		ManifestPath: manifest,
		FixturesRoot: fixtures,
		Timeout:      30 * time.Second,
	})
	if err != nil {
		t.Fatalf("ValidateFixtures() error = %v", err)
	}
	got := map[string]bool{}
	for _, c := range checks {
		got[c.ScenarioID] = c.OK
	}
	if !got["red_ok"] {
		t.Error("red fixture that fails verification must validate OK")
	}
	if got["red_rotten"] {
		t.Error("red fixture that already passes must be flagged as rot")
	}
	if !got["green_ok"] {
		t.Error("green trap fixture that passes must validate OK")
	}
}

func TestManifestValidate_RejectsBadDeliveredState(t *testing.T) {
	m := Manifest{Version: 1, Name: "x", Metrics: []string{"m"},
		Scenarios: []Scenario{validateScenario("a", "f", "purple")}}
	if err := m.Validate(); err == nil {
		t.Fatal("Validate() must reject delivered_state other than red/green")
	}
}
