package agent

import (
	"testing"

	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestConfirmedNonGitDelegationUsesPrimaryWorkspace(t *testing.T) {
	for _, kind := range []string{"bash", "writer"} {
		t.Run(kind, func(t *testing.T) {
			root := testkit.ResolvedTempDir(t)
			registry := tools.NewRegistry()
			registry.MustRegister(tools.NewReadTool(root))
			registry.MustRegister(tools.NewWriteTool(root))
			registry.MustRegister(tools.NewBashTool(root))
			types := NewAgentTypeRegistry()
			if err := types.RegisterDynamic("writer", "Writes files", []string{"read", "write"}, "Write the requested file."); err != nil {
				t.Fatal(err)
			}
			mock := testkit.NewMockClient()
			runner := &Runner{}
			agent := runner.newConfiguredAgent(t.Context(), runnerAgentDeps{client: mock, baseRegistry: registry, workDir: root, typeRegistry: types, workspaceIsolation: true}, kind, 2, "", nil)
			if agent.definitionError != nil || agent.workDir != root || agent.isolatedWorkspace != nil {
				t.Fatalf("confirmed non-repo delegation rejected: dir=%q isolation=%v error=%v", agent.workDir, agent.isolatedWorkspace, agent.definitionError)
			}
			clones := mock.Clones()
			if len(clones) != 1 {
				t.Fatalf("expected one isolated client, got %d", len(clones))
			}
			childClient := clones[0]
			childClient.EnqueueText("No changes needed.")
			result, err := agent.Run(t.Context(), "Say whether any changes are needed.")
			if err != nil || result.Status != AgentStatusCompleted || len(childClient.Calls()) != 1 {
				t.Fatalf("primary-workspace agent failed: %+v %v calls=%d", result, err, len(childClient.Calls()))
			}
		})
	}
}
