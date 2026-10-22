package chat

import (
	"fmt"

	"gokin/internal/goals"
	"gokin/internal/skills"
)

// Validate every persisted hook reference before any owner is replaced.
// Corruption in a branch/checkpoint must not leave a half-restored session.
func validateSessionHookGraph(state *SessionState, depth int, visiting map[*SessionState]bool) error {
	if state == nil || depth > maxJSONDepth || visiting[state] {
		return fmt.Errorf("invalid session hook state graph")
	}
	visiting[state] = true
	defer delete(visiting, state)
	if err := goals.Validate(state.Goal); err != nil {
		return fmt.Errorf("invalid saved goal: %w", err)
	}
	if err := ValidateHistoryWindows(state.HistoryWindows); err != nil {
		return fmt.Errorf("invalid saved history windows: %w", err)
	}
	if err := skills.ValidateHookActivations(state.SkillHooks); err != nil {
		return fmt.Errorf("invalid saved skill hooks: %w", err)
	}
	for name, activations := range state.CheckpointSkillHooks {
		if err := skills.ValidateHookActivations(activations); err != nil {
			return fmt.Errorf("invalid checkpoint %q skill hooks: %w", name, err)
		}
	}
	for name, branch := range state.Branches {
		if err := validateSessionHookGraph(branch, depth+1, visiting); err != nil {
			return fmt.Errorf("invalid branch %q skill hooks: %w", name, err)
		}
	}
	return nil
}
