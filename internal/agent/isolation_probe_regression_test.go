//go:build unix

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestIsolationProbeFailureFailsClosed(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "git"), []byte("#!/bin/sh\necho probe-failed >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	client := testkit.NewMockClient().EnqueueText("must not run")
	runner := &Runner{}
	agent := runner.newConfiguredAgent(t.Context(), runnerAgentDeps{client: client, baseRegistry: tools.NewRegistry(), workDir: root, workspaceIsolation: true}, "bash", 1, "", nil)
	if agent.definitionError == nil || !strings.Contains(agent.definitionError.Error(), "isolation") {
		t.Fatalf("isolation failure silently used base workspace: error=%v isolated=%v", agent.definitionError, agent.isolatedWorkspace)
	}
	result, err := agent.Run(t.Context(), "must stay isolated")
	if err == nil || result.Status != AgentStatusFailed {
		t.Fatalf("failed isolation still ran: %+v %v", result, err)
	}
	if len(client.Calls()) != 0 {
		t.Fatal("isolation probe contacted model")
	}
}
