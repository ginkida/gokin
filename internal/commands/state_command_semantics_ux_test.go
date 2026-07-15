package commands

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/config"
)

func TestThinkingCopyDescribesBehaviorAndCostNotDisplayOnly(t *testing.T) {
	command := &ThinkingCommand{}
	description := strings.ToLower(command.Description())
	if !strings.Contains(description, "reasoning") || !strings.Contains(description, "budget") || strings.Contains(description, "display") {
		t.Fatalf("thinking description hides behavioral/cost effect: %q", command.Description())
	}
	examples := strings.ToLower(getCommandExample("thinking"))
	for _, want := range []string{"force reasoning", "never reason", "budget"} {
		if !strings.Contains(examples, want) {
			t.Fatalf("thinking examples missing %q:\n%s", want, examples)
		}
	}
	for _, misleading := range []string{"show provider", "hide reasoning"} {
		if strings.Contains(examples, misleading) {
			t.Fatalf("thinking examples still imply display-only behavior %q:\n%s", misleading, examples)
		}
	}

	cfg := config.DefaultConfig()
	app := &fakeSetApp{cfg: cfg}
	out, err := command.Execute(context.Background(), []string{"off"}, app)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if config.ResolveThinkingMode(cfg.Model.ThinkingMode) != config.ThinkingModeOff || !strings.Contains(out, "never reasons") {
		t.Fatalf("thinking off state=%q output=%q", cfg.Model.ThinkingMode, out)
	}
}

func TestSafetyCommandFeedbackExplainsIndependentControls(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Permission.Enabled = true
	cfg.Tools.Bash.Sandbox = true
	app := &fakeSetApp{cfg: cfg}

	permissions, err := (&PermissionsCommand{}).Execute(context.Background(), []string{"off"}, app)
	if err != nil {
		t.Fatalf("permissions Execute: %v", err)
	}
	for _, want := range []string{"auto-approve", "sandbox setting is unchanged"} {
		if !strings.Contains(permissions, want) {
			t.Fatalf("permissions feedback hides %q: %q", want, permissions)
		}
	}
	if !cfg.Tools.Bash.Sandbox {
		t.Fatal("permissions command unexpectedly changed sandbox")
	}

	// Restore prompts so the sandbox-off explanation must explicitly preserve
	// them instead of implying full YOLO mode.
	cfg.Permission.Enabled = true
	sandbox, err := (&SandboxCommand{}).Execute(context.Background(), []string{"off"}, app)
	if err != nil {
		t.Fatalf("sandbox Execute: %v", err)
	}
	for _, want := range []string{"unrestricted", "permission prompts are unchanged"} {
		if !strings.Contains(sandbox, want) {
			t.Fatalf("sandbox feedback hides %q: %q", want, sandbox)
		}
	}
	if !cfg.Permission.Enabled {
		t.Fatal("sandbox command unexpectedly changed permission prompts")
	}
}
