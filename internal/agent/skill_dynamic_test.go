package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/permission"
	"gokin/internal/skills"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

func TestAgentDynamicSkillUsesOwnPermissionPipelineAndLedger(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(map[bool]string{false: "requires approval", true: "skill preapproval"}[allow], func(t *testing.T) {
			workDir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			path := filepath.Join(root, "dynamic", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			grants := ""
			if allow {
				grants = "allowed-tools: Bash\n"
			}
			if err := os.WriteFile(path, []byte("---\ndescription: Dynamic agent context\n"+grants+"---\nState: !`printf x >> agent-runs; printf agent-context`"), 0o600); err != nil {
				t.Fatal(err)
			}
			foreground := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}), workDir)
			registry := tools.NewRegistry()
			registry.MustRegister(foreground)
			registry.MustRegister(tools.NewBashTool(workDir))
			manager := permission.NewManager(permission.DefaultRules(), true)
			prompts := 0
			manager.SetPromptHandler(func(_ context.Context, _ *permission.Request) (permission.Decision, error) {
				prompts++
				return permission.DecisionAllow, nil
			})
			agent := NewAgent(AgentTypeGeneral, nil, registry, workDir, 3, "", manager, nil)
			result := agent.executeToolResultSafely(t.Context(), &genai.FunctionCall{ID: "skill-call", Name: "skill", Args: map[string]any{"name": "dynamic"}})
			if result.Success != allow || prompts != 0 {
				t.Fatalf("load=%+v prompts=%d", result, prompts)
			}
			if allow {
				if !strings.Contains(result.Content, "agent-context") {
					t.Fatalf("missing context: %+v", result)
				}
				if data, err := os.ReadFile(filepath.Join(workDir, "agent-runs")); err != nil || string(data) != "x" {
					t.Fatalf("agent shell ran incorrectly: %q err=%v", data, err)
				}
			} else if _, err := os.Stat(filepath.Join(workDir, "agent-runs")); !os.IsNotExist(err) {
				t.Fatalf("unapproved agent shell ran: %v", err)
			}
			if len(foreground.InvocationLedger().SnapshotNewestFirst()) != 0 || len(foreground.ActivePermissionGrants()) != 0 {
				t.Fatal("agent render leaked into foreground ledger or grants")
			}
			want := 0
			if allow {
				want = 1
			}
			if got := len(requireAgentSkillTool(t, agent).InvocationLedger().SnapshotNewestFirst()); got != want {
				t.Fatalf("agent render ledger=%d want=%d", got, want)
			}
		})
	}
}
