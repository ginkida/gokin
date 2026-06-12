package evals

import (
	"encoding/json"
	"fmt"
	"os"
)

// Manifest describes a provider-neutral coding eval set.
type Manifest struct {
	Version     int        `json:"version"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Metrics     []string   `json:"metrics"`
	Scenarios   []Scenario `json:"scenarios"`
}

// Scenario is one coding-agent evaluation task.
type Scenario struct {
	ID                   string   `json:"id"`
	Category             string   `json:"category"`
	Difficulty           string   `json:"difficulty"`
	Prompt               string   `json:"prompt"`
	Fixture              string   `json:"fixture"`
	ExpectedBehaviors    []string `json:"expected_behaviors"`
	VerificationCommands []string `json:"verification_commands"`
	SuccessCriteria      []string `json:"success_criteria"`
	FailureSignals       []string `json:"failure_signals"`
	MaxToolCalls         int      `json:"max_tool_calls"`

	// DeliveredState declares whether the fixture's verification commands
	// pass in the delivered (pre-agent) state: "red" (default — the fixture
	// ships broken and the agent must make verification pass) or "green"
	// (trap scenarios where the correct agent action is to LEAVE things
	// working and a careless action breaks verification). `eval validate`
	// enforces this contract so fixtures can't silently rot.
	DeliveredState string `json:"delivered_state,omitempty"`
}

// LoadManifest reads and validates a coding eval manifest.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// Validate checks manifest structure without requiring fixtures to exist.
func (m *Manifest) Validate() error {
	if m == nil {
		return fmt.Errorf("manifest is nil")
	}
	if m.Version <= 0 {
		return fmt.Errorf("manifest version must be positive")
	}
	if m.Name == "" {
		return fmt.Errorf("manifest name is required")
	}
	if len(m.Metrics) == 0 {
		return fmt.Errorf("manifest metrics are required")
	}
	if len(m.Scenarios) == 0 {
		return fmt.Errorf("manifest scenarios are required")
	}

	seen := make(map[string]bool, len(m.Scenarios))
	for _, scenario := range m.Scenarios {
		if scenario.ID == "" {
			return fmt.Errorf("scenario id is required")
		}
		if seen[scenario.ID] {
			return fmt.Errorf("duplicate scenario id %q", scenario.ID)
		}
		seen[scenario.ID] = true
		if scenario.Category == "" || scenario.Difficulty == "" || scenario.Prompt == "" || scenario.Fixture == "" {
			return fmt.Errorf("scenario %q missing required metadata", scenario.ID)
		}
		if len(scenario.ExpectedBehaviors) == 0 {
			return fmt.Errorf("scenario %q missing expected behaviors", scenario.ID)
		}
		if len(scenario.VerificationCommands) == 0 {
			return fmt.Errorf("scenario %q missing verification commands", scenario.ID)
		}
		if len(scenario.SuccessCriteria) == 0 {
			return fmt.Errorf("scenario %q missing success criteria", scenario.ID)
		}
		if len(scenario.FailureSignals) == 0 {
			return fmt.Errorf("scenario %q missing failure signals", scenario.ID)
		}
		if scenario.MaxToolCalls <= 0 {
			return fmt.Errorf("scenario %q max_tool_calls must be positive", scenario.ID)
		}
		switch scenario.DeliveredState {
		case "", "red", "green":
		default:
			return fmt.Errorf("scenario %q delivered_state must be \"red\" or \"green\", got %q", scenario.ID, scenario.DeliveredState)
		}
	}
	return nil
}

// EffectiveDeliveredState resolves the default: fixtures ship red unless
// declared otherwise.
func (s Scenario) EffectiveDeliveredState() string {
	if s.DeliveredState == "green" {
		return "green"
	}
	return "red"
}
