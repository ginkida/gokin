package evals

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FixtureCheck is the validation outcome for one scenario's fixture contract.
type FixtureCheck struct {
	ScenarioID string `json:"scenario_id"`
	Fixture    string `json:"fixture"`
	Expect     string `json:"expect"` // "red" or "green"
	OK         bool   `json:"ok"`
	Detail     string `json:"detail,omitempty"`
}

// ValidateOptions configures the fixture-contract validation pass.
type ValidateOptions struct {
	ManifestPath string
	FixturesRoot string
	ScenarioIDs  []string
	Timeout      time.Duration
}

// ValidateFixtures enforces the delivered-state contract for every scenario:
// a "red" fixture's verification commands must FAIL as delivered (the agent's
// job is to make them pass), a "green" trap fixture's must PASS (the agent's
// job is to not break them). Without this check a fixture can silently rot —
// a pre-passing "red" fixture scores every agent as successful and the
// scenario measures nothing.
func ValidateFixtures(ctx context.Context, opts ValidateOptions) ([]FixtureCheck, error) {
	manifest, err := LoadManifest(opts.ManifestPath)
	if err != nil {
		return nil, err
	}
	scenarios, err := selectScenarios(manifest.Scenarios, opts.ScenarioIDs)
	if err != nil {
		return nil, err
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Minute
	}

	workRoot, err := os.MkdirTemp("", "gokin-eval-validate-")
	if err != nil {
		return nil, fmt.Errorf("create validation workspace: %w", err)
	}
	defer os.RemoveAll(workRoot)

	checks := make([]FixtureCheck, 0, len(scenarios))
	for _, scenario := range scenarios {
		if ctx.Err() != nil {
			return checks, ctx.Err()
		}
		checks = append(checks, validateScenarioFixture(ctx, manifest, scenario, opts, workRoot))
	}
	return checks, nil
}

func validateScenarioFixture(ctx context.Context, manifest *Manifest, scenario Scenario, opts ValidateOptions, workRoot string) FixtureCheck {
	check := FixtureCheck{
		ScenarioID: scenario.ID,
		Fixture:    scenario.Fixture,
		Expect:     scenario.EffectiveDeliveredState(),
	}

	src := filepath.Join(opts.FixturesRoot, filepath.FromSlash(scenario.Fixture))
	if !dirExists(src) {
		check.Detail = fmt.Sprintf("fixture directory missing: %s", src)
		return check
	}

	workspace := filepath.Join(workRoot, sanitizePathPart(scenario.ID))
	if err := resetWorkspace(src, workspace); err != nil {
		check.Detail = fmt.Sprintf("copy fixture: %v", err)
		return check
	}

	var failures []string
	allPassed := true
	for _, command := range scenario.VerificationCommands {
		expanded := expandCommandTemplate(command, manifest, scenario, workspace, matrixEntry{})
		result := runShellCommand(ctx, workspace, expanded, opts.Timeout, evalEnv(manifest, scenario, workspace, matrixEntry{}))
		if !result.Success {
			allPassed = false
			failures = append(failures, fmt.Sprintf("%s (exit %d)", command, result.ExitCode))
		}
	}

	switch check.Expect {
	case "green":
		check.OK = allPassed
		if !check.OK {
			check.Detail = "expected delivered state to PASS verification (green trap), but it failed: " + strings.Join(failures, "; ")
		}
	default: // red
		check.OK = !allPassed
		if !check.OK {
			check.Detail = "expected delivered state to FAIL verification (the agent's job is to fix it), but every command passed — the scenario measures nothing"
		}
	}
	return check
}
