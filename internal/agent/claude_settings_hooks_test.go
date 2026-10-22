package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/hooks"
	"google.golang.org/genai"
)

func TestAgentClaudeSettingsRewriteRunsOnceBeforePermissionAndTracksActualTarget(t *testing.T) {
	for _, source := range []string{"user", "managed", "plugin"} {
		t.Run(source, func(t *testing.T) {
			agent, _, _, root := agentHookFixture(t, "PreToolUse", "exit 0", false)
			user := t.TempDir()
			output := map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "updatedInput": map[string]any{"file_path": filepath.Join(root, "actual.txt"), "content": "actual"}}}
			encoded, err := json.Marshal(output)
			if err != nil {
				t.Fatal(err)
			}
			command := "cat > child-settings-input.json; printf x >> settings-runs; printf '%s' '" + string(encoded) + "'"
			settings := map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Write", "hooks": []any{map[string]any{"type": "command", "command": command}}}}}}
			data, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(user, "settings.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if source == "managed" {
				if err := os.Rename(filepath.Join(user, "settings.json"), filepath.Join(user, "managed-settings.json")); err != nil {
					t.Fatal(err)
				}
				agent.hooks.ConfigureClaudeManagedSettings(user)
				agent.hooks.SetEnabled(false)
			} else if source == "plugin" {
				hookPath := filepath.Join(user, "hooks", "hooks.json")
				if err := os.MkdirAll(filepath.Dir(hookPath), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(user, "settings.json"), hookPath); err != nil {
					t.Fatal(err)
				}
				agent.hooks.ConfigureClaudeSettings(root, user, false)
				agent.hooks.ConfigureClaudePlugins([]string{user})
				agent.hooks.SetEnabled(false)
			} else {
				agent.hooks.ConfigureClaudeSettings(root, user, false)
			}
			if source == "plugin" {
				all := agent.hooks.GetHooks()
				if len(all) != 1 || !strings.HasPrefix(all[0].Source, "claude:plugin:") {
					t.Fatalf("plugin variant loaded another source: %+v", all)
				}
			}
			call := &genai.FunctionCall{ID: "settings-write", Name: "write", Args: map[string]any{"file_path": "original.txt", "content": "original"}}
			result := agent.executeToolWithReflection(t.Context(), call)
			if success, _ := result.Response.Response["success"].(bool); !success {
				t.Fatalf("result=%+v", result.Response.Response)
			}
			if data, err := os.ReadFile(filepath.Join(root, "settings-runs")); err != nil || string(data) != "x" {
				t.Fatalf("configured handler ran twice: %q %v", data, err)
			}
			if call.Args["file_path"] != "original.txt" {
				t.Fatal("provider tool input was changed")
			}
			if data, err := os.ReadFile(filepath.Join(root, "actual.txt")); err != nil || string(data) != "actual" {
				t.Fatalf("actual target not used: %q %v", data, err)
			}
			if paths := agent.GetTouchedPaths(); len(paths) != 1 || paths[0] != "actual.txt" {
				t.Fatalf("mutation tracking=%v", paths)
			}
			data, err = os.ReadFile(filepath.Join(root, "child-settings-input.json"))
			if err != nil {
				t.Fatal(err)
			}
			var input map[string]any
			if err := json.Unmarshal(data, &input); err != nil || input["agent_id"] != agent.ID || input["agent_type"] != hooks.ClaudeAgentType(string(agent.Type)) {
				t.Fatalf("child identity=%v %v", input, err)
			}
		})
	}
}
