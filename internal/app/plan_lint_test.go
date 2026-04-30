package app

import (
	"context"
	"testing"

	"gokin/internal/plan"
)

func TestValidateVerifyCommandSafety_SafeCommands(t *testing.T) {
	a := &App{}
	profile := doneGateProfile{}
	ctx := context.Background()

	safe := []string{
		"go test ./...",
		"go build ./...",
		"go vet ./...",
		"npm test",
		"make check",
		"cargo test --workspace",
	}
	for _, cmd := range safe {
		ok, reason := a.validateVerifyCommandSafety(ctx, cmd, profile)
		if !ok {
			t.Errorf("validateVerifyCommandSafety(%q) = blocked (%s), want allowed", cmd, reason)
		}
	}
}

func TestValidateVerifyCommandSafety_UnsafeCommands(t *testing.T) {
	a := &App{}
	profile := doneGateProfile{}
	ctx := context.Background()

	cases := []struct {
		cmd    string
		reason string
	}{
		{"git commit -am 'wip'", "mutating git operation"},
		{"rm -rf ./dist", "destructive rm"},
		{"echo foo > out.txt", "file-writing redirect"},
		{"cat log >> archive.log", "append redirect"},
		{"", "empty command"},
	}

	for _, tc := range cases {
		ok, _ := a.validateVerifyCommandSafety(ctx, tc.cmd, profile)
		if ok {
			t.Errorf("validateVerifyCommandSafety(%q) = allowed, want blocked (%s)", tc.cmd, tc.reason)
		}
	}
}

// TestValidateVerifyCommandSafety_CancelledCtx confirms the ctx parameter is
// plumbed through without panicking. The static validator does not block on
// I/O so a cancelled context still returns a result rather than hanging.
func TestValidateVerifyCommandSafety_CancelledCtx(t *testing.T) {
	a := &App{}
	profile := doneGateProfile{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	// Must not panic; result is don't-care for the cancelled-ctx case.
	_, _ = a.validateVerifyCommandSafety(ctx, "go test ./...", profile)
}

func TestLintPlanBeforeApproval_NilPlan(t *testing.T) {
	a := &App{}
	if err := a.lintPlanBeforeApproval(context.Background(), nil); err == nil {
		t.Fatal("lintPlanBeforeApproval(nil plan) = nil, want error")
	}
}

func TestLintPlanBeforeApproval_EmptySteps(t *testing.T) {
	a := &App{}
	p := plan.NewPlan("empty", "no steps")
	if err := a.lintPlanBeforeApproval(context.Background(), p); err == nil {
		t.Fatal("lintPlanBeforeApproval(plan with no steps) = nil, want error")
	}
}

func TestLintPlanBeforeApproval_MissingVerifyCommands(t *testing.T) {
	a := &App{}
	p := plan.NewPlan("task", "description")
	step := p.AddStep("compile", "build the project")
	// Wipe the defaults EnsureContractDefaults injected so the lint fires.
	step.VerifyCommands = nil

	err := a.lintPlanBeforeApproval(context.Background(), p)
	if err == nil {
		t.Fatal("lintPlanBeforeApproval(step with no verify_commands) = nil, want error")
	}
}

func TestLintPlanBeforeApproval_PassesWithSafeCommands(t *testing.T) {
	a := &App{}
	p := plan.NewPlan("task", "description")
	step := p.AddStep("compile", "build the project")
	step.VerifyCommands = []string{"go build ./..."}

	if err := a.lintPlanBeforeApproval(context.Background(), p); err != nil {
		t.Fatalf("lintPlanBeforeApproval(valid plan) = %v, want nil", err)
	}
}

func TestLintPlanBeforeApproval_BlocksUnsafeVerifyCommand(t *testing.T) {
	a := &App{}
	p := plan.NewPlan("task", "description")
	step := p.AddStep("deploy", "deploy to production")
	step.VerifyCommands = []string{"git push origin main"}

	if err := a.lintPlanBeforeApproval(context.Background(), p); err == nil {
		t.Fatal("lintPlanBeforeApproval(plan with unsafe verify command) = nil, want error")
	}
}
