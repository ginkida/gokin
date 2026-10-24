package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/donegate"
	"gokin/internal/permission"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestAgentDoneGateVerificationDenialSkipsAutoFix(t *testing.T) {
	work := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module example.com/denial\n\ngo 1.23\n"), 0600); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewBashTool(work))
	rules := permission.DefaultRules()
	rules.SetPolicy("bash", permission.LevelDeny)
	a := NewAgent(AgentTypeGeneral, nil, registry, work, 3, "", permission.NewManager(rules, true), nil)
	a.SetDoneGatePolicy(donegate.Policy{Enabled: true, AutoFixAttempts: 2})
	a.recordToolExecution("edit", map[string]any{"file_path": "m.go"}, tools.NewSuccessResult("edited"))
	ctx, state := tools.ContextWithHeadlessPermissions(t.Context(), false, "acceptEdits")
	var output strings.Builder
	fixes, generation := 0, -1
	if outcome, err := a.runDoneGateAtBreak(ctx, "Update m.go", &output, &fixes, &generation); err != nil || outcome != agentGateSkipped || fixes != 0 {
		t.Fatalf("denied verifier started auto-fix: outcome=%d fixes=%d output=%s", outcome, fixes, output.String())
	}
	if state.Verification().Status != "denied" || len(state.Denials()) == 0 {
		t.Fatalf("missing receipt: %+v denials=%v", state.Verification(), state.Denials())
	}
}
