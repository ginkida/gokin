package evals

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

	// Machine-checked behavioral assertions, scored ONLY when declared (so
	// scenarios that omit them keep their existing metric set and baselines).
	// They close the "green/trap scenario rewards a no-op" hole: when
	// verification passes in the delivered state, doing nothing scores well
	// unless a positive assertion proves the agent actually did the work.
	//
	//   AnswerMustContain — substrings the final answer MUST include
	//     (case-insensitive). Positive proof the agent reached the required
	//     conclusion, e.g. naming a caller in an investigation scenario.
	//   FileMustChange    — workspace-relative paths that MUST be modified.
	//     Catches the no-op on refactor/feature scenarios (verification still
	//     green because nothing was touched).
	//   FileMustNotChange — workspace-relative paths that must NOT be
	//     modified. Catches the trap where the correct action is to leave a
	//     file alone (e.g. a deprecated-but-still-used symbol).
	//   WorkspaceMustRemainUnchanged — no workspace path may be added, removed,
	//     or modified. Intended for read-only investigation/analytics scenarios.
	//
	// Paths match exactly or as a trailing path segment, so a scenario may
	// name "internal/x/y.go" or a deeper-rooted equivalent.
	AnswerMustContain []string `json:"answer_must_contain,omitempty"`
	FileMustChange    []string `json:"file_must_change,omitempty"`
	FileMustNotChange []string `json:"file_must_not_change,omitempty"`
	// WorkspaceMustRemainUnchanged is stronger than enumerating protected
	// files: any edit, deletion, or newly-created workspace file fails.
	WorkspaceMustRemainUnchanged bool `json:"workspace_must_remain_unchanged,omitempty"`

	// HybridCandidate classifies whether auto engine policy should expose the
	// computation plane for this prompt. Nil means the scenario does not test
	// engine selection; an explicit false is a negative-control scenario.
	HybridCandidate *bool `json:"hybrid_candidate,omitempty"`
	// HybridRequiredOperations names runtime context primitives whose actual
	// execution is required on auto/hybrid candidate runs. This measures
	// efficient implementation adoption, not merely REPL exposure or a
	// repl_exec call containing arbitrary Python.
	HybridRequiredOperations []string `json:"hybrid_required_operations,omitempty"`
	// HybridRequiredAnyOperations names interchangeable efficient primitives;
	// at least one must execute. Keep this separate from the all-of field so a
	// scenario can accept equivalent one-pass implementations without weakening
	// unrelated mandatory operations.
	HybridRequiredAnyOperations []string `json:"hybrid_required_any_operations,omitempty"`
	// HybridMaxScanOperations caps collection-scan primitives (count, search,
	// list) across the whole scenario. HybridMinFileIndexRefreshes requires
	// parent-observed inventory callbacks, so worker metadata alone cannot prove
	// a repository scan. HybridMaxReplCalls separately prevents a one-pass task
	// from being split across avoidable model/tool rounds.
	HybridMaxScanOperations     int `json:"hybrid_max_scan_operations,omitempty"`
	HybridMinFileIndexRefreshes int `json:"hybrid_min_file_index_refreshes,omitempty"`
	HybridMaxReplCalls          int `json:"hybrid_max_repl_calls,omitempty"`
}

// HasBehavioralAssertion reports whether the scenario declares at least one
// machine-checked behavioral assertion (positive OR negative).
func (s Scenario) HasBehavioralAssertion() bool {
	return len(s.AnswerMustContain) > 0 || len(s.FileMustChange) > 0 ||
		len(s.FileMustNotChange) > 0 || s.WorkspaceMustRemainUnchanged || s.HybridCandidate != nil ||
		s.HasHybridEfficiencyAssertion()
}

// HasHybridEfficiencyAssertion reports whether the scenario constrains the
// implementation path taken by an eligible computation-plane run.
func (s Scenario) HasHybridEfficiencyAssertion() bool {
	return len(s.HybridRequiredOperations) > 0 || len(s.HybridRequiredAnyOperations) > 0 ||
		s.HybridMaxScanOperations > 0 ||
		s.HybridMinFileIndexRefreshes > 0 || s.HybridMaxReplCalls > 0
}

// HasPositiveBehavioralAssertion reports whether the scenario declares an
// assertion that a no-op CANNOT satisfy — the answer must contain something, or
// a file must change. FileMustNotChange and WorkspaceMustRemainUnchanged are
// negative and trivially satisfied by doing nothing, so they do NOT count here.
// Green (trap) scenarios need a positive assertion or they silently reward a
// no-op.
func (s Scenario) HasPositiveBehavioralAssertion() bool {
	return len(s.AnswerMustContain) > 0 || len(s.FileMustChange) > 0
}

// LoadManifest reads and validates a coding eval manifest.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var manifest Manifest
	if err := decodeStrictJSON(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func decodeStrictJSON(data []byte, destination any) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

// rejectDuplicateJSONKeys walks the token stream before decoding into structs.
// encoding/json otherwise accepts repeated object members and silently keeps
// the last value, which can weaken an eval contract without an obvious error.
func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanUniqueJSONValue(decoder, "$"); err != nil {
		return err
	}
	if _, err := decoder.Token(); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("multiple JSON values")
}

func scanUniqueJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key at %s is not a string", path)
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON key %q at %s", key, path)
			}
			seen[key] = struct{}{}
			if err := scanUniqueJSONValue(decoder, path+"."+key); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for index := 0; decoder.More(); index++ {
			if err := scanUniqueJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q at %s", delimiter, path)
	}
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
		if len(scenario.HybridRequiredOperations) > 0 || len(scenario.HybridRequiredAnyOperations) > 0 {
			if scenario.HybridCandidate == nil || !*scenario.HybridCandidate {
				return fmt.Errorf("scenario %q hybrid required operations require hybrid_candidate=true", scenario.ID)
			}
			seenOperations := make(map[string]bool,
				len(scenario.HybridRequiredOperations)+len(scenario.HybridRequiredAnyOperations))
			operationFields := []struct {
				name       string
				operations []string
			}{
				{"hybrid_required_operations", scenario.HybridRequiredOperations},
				{"hybrid_required_any_operations", scenario.HybridRequiredAnyOperations},
			}
			for _, field := range operationFields {
				for _, operation := range field.operations {
					if !validHybridOperationName(operation) {
						return fmt.Errorf("scenario %q has invalid %s operation %q", scenario.ID, field.name, operation)
					}
					if seenOperations[operation] {
						return fmt.Errorf("scenario %q repeats hybrid operation %q", scenario.ID, operation)
					}
					seenOperations[operation] = true
				}
			}
		}
		if scenario.HybridMaxScanOperations < 0 {
			return fmt.Errorf("scenario %q hybrid_max_scan_operations must not be negative", scenario.ID)
		}
		if scenario.HybridMinFileIndexRefreshes < 0 {
			return fmt.Errorf("scenario %q hybrid_min_file_index_refreshes must not be negative", scenario.ID)
		}
		if scenario.HybridMaxReplCalls < 0 {
			return fmt.Errorf("scenario %q hybrid_max_repl_calls must not be negative", scenario.ID)
		}
		if scenario.HybridMaxScanOperations > 0 &&
			scenario.HybridMinFileIndexRefreshes > scenario.HybridMaxScanOperations {
			return fmt.Errorf("scenario %q hybrid_min_file_index_refreshes must not exceed hybrid_max_scan_operations", scenario.ID)
		}
		if (scenario.HybridMaxScanOperations > 0 || scenario.HybridMinFileIndexRefreshes > 0 ||
			scenario.HybridMaxReplCalls > 0) &&
			len(scenario.HybridRequiredOperations) == 0 && len(scenario.HybridRequiredAnyOperations) == 0 {
			return fmt.Errorf("scenario %q hybrid efficiency limits require a hybrid operation contract", scenario.ID)
		}
		switch scenario.DeliveredState {
		case "", "red", "green":
		default:
			return fmt.Errorf("scenario %q delivered_state must be \"red\" or \"green\", got %q", scenario.ID, scenario.DeliveredState)
		}
		// A "green" (trap) scenario passes verification in the delivered
		// state, so a no-op also passes — it MUST carry a positive assertion
		// that the agent actually did the right thing, or it silently rewards
		// doing nothing. Red scenarios are gated by verification flipping
		// red->green, so the assertion is optional for them.
		if scenario.EffectiveDeliveredState() == "green" && !scenario.HasPositiveBehavioralAssertion() {
			return fmt.Errorf("scenario %q is delivered_state=green but declares no POSITIVE behavioral assertion "+
				"(answer_must_contain / file_must_change) — file_must_not_change alone is trivially satisfied by a no-op, "+
				"which would score as success", scenario.ID)
		}
	}
	return nil
}

func validHybridOperationName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// EffectiveDeliveredState resolves the default: fixtures ship red unless
// declared otherwise.
func (s Scenario) EffectiveDeliveredState() string {
	if s.DeliveredState == "green" {
		return "green"
	}
	return "red"
}
