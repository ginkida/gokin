package agent

import (
	"strings"
	"testing"
	"time"

	"gokin/internal/tools"
)

// Sub-agents build their own system prompt and never see the foreground base
// rules — the verify-before-proposing rule must be present for substantive
// agents (a /loop 'improve the app' iteration is the canonical producer of
// unverified suggestion lists) and stay out of lightweight explorers.
func TestSubAgentPromptCarriesProposalHonesty(t *testing.T) {
	reg := tools.NewRegistry()
	general := &Agent{Type: AgentTypeGeneral, registry: reg}
	if !strings.Contains(general.buildSystemPrompt(), "Proposal honesty") {
		t.Error("general agent prompt must carry the proposal-honesty rule")
	}
	explore := &Agent{Type: AgentTypeExplore, registry: reg}
	if strings.Contains(explore.buildSystemPrompt(), "Proposal honesty") {
		t.Error("lightweight explore agent should keep its minimal prompt")
	}
}

func TestIsolatedBashPromptUsesLongVerificationFallbackWithoutBackgroundLoop(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry()
	bash := tools.NewBashTool(dir)
	bash.EnableManagedWorkspaceApplyBackMode(dir)
	reg.MustRegister(bash)
	reg.MustRegister(tools.NewRunTestsTool(dir))
	reg.MustRegister(tools.NewVerifyCodeTool(dir))

	a := &Agent{Type: AgentTypeBash, registry: reg, workDir: dir}
	prompt := a.buildSystemPrompt()
	for _, want := range []string{"prefer run_tests", "timeout_seconds", "Do not request or retry run_in_background"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("isolated bash prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "use run_in_background=true") {
		t.Fatalf("isolated bash prompt contains contradictory background guidance:\n%s", prompt)
	}
}

func TestBashAgentAllowsVerificationAndTaskOutputTools(t *testing.T) {
	allowed := make(map[string]bool)
	for _, name := range AgentTypeBash.AllowedTools() {
		allowed[name] = true
	}
	for _, name := range []string{"run_tests", "verify_code", "task_output"} {
		if !allowed[name] {
			t.Errorf("bash agent missing required long-verification tool %q", name)
		}
	}
}

func TestBashAgentTimeoutLeavesRoomForLongVerificationSummary(t *testing.T) {
	a := &Agent{Type: AgentTypeBash, maxTurns: 15}
	a.applyAgentTypeDefaults()
	if a.timeout != 15*time.Minute {
		t.Fatalf("normal bash timeout = %v, want 15m", a.timeout)
	}
	a.ApplyThoroughness(tools.ThoroughnessThorough, 15)
	if a.timeout != 35*time.Minute {
		t.Fatalf("thorough bash timeout = %v, want 35m", a.timeout)
	}
	a.ApplyThoroughness(tools.ThoroughnessQuick, 15)
	if a.timeout != 3*time.Minute {
		t.Fatalf("quick bash timeout = %v, want 3m", a.timeout)
	}
}
