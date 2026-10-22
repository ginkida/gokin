package hooks

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGoalEvaluatorHonorsWorkspaceTrustAndManagedHookPolicy(t *testing.T) {
	for _, scenario := range []string{"untrusted", "disabled", "managed-only", "managed-disabled", "managed-override"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			project, user, managed := filepath.Join(root, "project"), filepath.Join(root, "user"), filepath.Join(root, "managed")
			manager := NewManager(true, project)
			manager.ConfigureClaudeManagedSettings(managed)
			manager.ConfigureClaudeSettings(project, user, scenario != "untrusted")
			switch scenario {
			case "disabled":
				writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"disableAllHooks": true})
			case "managed-only":
				writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{"allowManagedHooksOnly": true})
			case "managed-disabled":
				writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{"disableAllHooks": true})
			case "managed-override":
				writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"disableAllHooks": true})
				writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{"disableAllHooks": false})
			}
			err := manager.GoalPolicyError()
			if scenario == "managed-override" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("blocked goal evaluator was allowed")
			}
			if scenario == "untrusted" && !strings.Contains(err.Error(), "trusted") {
				t.Fatal("missing trust reason", err)
			}
		})
	}
}
