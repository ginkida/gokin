package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubagentStartRunsOnResumeAndKeepsChildContextOnce(t *testing.T) {
	a, _, mock, root := agentHookFixture(t, "PreToolUse", "exit 0", false)
	user := t.TempDir()
	a.hooks.ConfigureClaudeSettings(root, user, true)
	settings := `{"hooks":{"SubagentStart":[{"matcher":"general-purpose","hooks":[{"type":"command","command":"cat > child-start.json; printf x >> child-start-runs; printf '%s' '{\"continue\":false,\"decision\":\"block\",\"hookSpecificOutput\":{\"additionalContext\":\"CHILD_START_EVIDENCE\"}}'"}]}]}}`
	if err := os.WriteFile(filepath.Join(user, "settings.json"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		mock.EnqueueText("Done")
		result, err := a.Run(t.Context(), "inspect proof")
		if err != nil || result.Status != AgentStatusCompleted {
			t.Fatalf("start hook blocked run: %+v %v", result, err)
		}
	}
	count := 0
	for _, content := range a.history {
		for _, part := range content.Parts {
			if strings.Contains(part.Text, "CHILD_START_EVIDENCE") {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("resume duplicated or lost start context: %d", count)
	}
	data, _ := os.ReadFile(filepath.Join(root, "child-start-runs"))
	if string(data) != "xx" {
		t.Fatalf("start did not run per invocation: %q", data)
	}
	data, _ = os.ReadFile(filepath.Join(root, "child-start.json"))
	var input map[string]any
	_ = json.Unmarshal(data, &input)
	if input["agent_id"] != a.ID || input["agent_type"] != "general-purpose" || input["tool_name"] != nil {
		t.Fatalf("wrong child payload: %s", data)
	}
}
