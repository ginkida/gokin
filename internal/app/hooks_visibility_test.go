package app

import (
	"context"
	"testing"
	"time"

	"gokin/internal/hooks"
)

// The anti-spam state machine: a post_tool hook on `write` fires on EVERY
// write, so a broken one must toast once per failing streak — not per call —
// and a recovery must re-arm the notification.
func TestHookFailureTracker_OncePerStreak(t *testing.T) {
	var tr hookFailureTracker

	if !tr.shouldNotify("post_tool:fmt", true) {
		t.Fatal("first failure of a streak must notify")
	}
	if tr.shouldNotify("post_tool:fmt", true) {
		t.Fatal("second consecutive failure must NOT re-notify")
	}
	if tr.shouldNotify("post_tool:fmt", false) {
		t.Fatal("a success must not notify")
	}
	if !tr.shouldNotify("post_tool:fmt", true) {
		t.Fatal("a NEW streak after recovery must notify again")
	}
	// Independent hooks have independent streaks.
	if !tr.shouldNotify("on_error:lint", true) {
		t.Fatal("a different hook's first failure must notify")
	}
}

// onHookResult must stay quiet for failures that ARE the designed control
// flow (surfaced by their own mechanism), and surface everything else.
func TestOnHookResult_DesignedControlFlowStaysQuiet(t *testing.T) {
	a := &App{}

	blockingPre := &hooks.Hook{Name: "gate", Type: hooks.PreTool, FailOnError: true}
	a.onHookResult(blockingPre, "", context.DeadlineExceeded)
	if a.hookFailures.shouldNotify("pre_tool:gate", true) == false {
		t.Fatal("a FailOnError pre_tool failure must not have consumed the streak (it was skipped before tracking)")
	}

	// An ADVISORY pre_tool hook (FailOnError=false) is invisible elsewhere —
	// its failure must go through the notify path (streak consumed).
	advisoryPre := &hooks.Hook{Name: "advice", Type: hooks.PreTool}
	a.onHookResult(advisoryPre, "", context.DeadlineExceeded)
	if a.hookFailures.shouldNotify("pre_tool:advice", true) {
		t.Fatal("advisory pre_tool failure should have consumed the healthy->failing transition (i.e. it was surfaced)")
	}
}

// End-to-end through the REAL hooks.Manager: a failing post_tool hook must
// reach the wired handler; a passing one must reset the streak.
func TestHooksManagerHandler_EndToEnd(t *testing.T) {
	a := &App{}
	mgr := hooks.NewManager(true, t.TempDir())
	mgr.SetTimeout(5 * time.Second)
	mgr.SetHandler(a.onHookResult)
	mgr.AddHook(&hooks.Hook{
		Name:    "always-fails",
		Type:    hooks.PostTool,
		Command: "exit 1",
		Enabled: true,
	})

	mgr.RunPostTool(context.Background(), "write", map[string]any{}, "ok")
	// The failing run must have consumed the healthy->failing transition
	// (proving onHookResult was invoked and took the notify path; the toast
	// itself is a safeSendToProgram no-op with a nil program).
	if a.hookFailures.shouldNotify("post_tool:always-fails", true) {
		t.Fatal("failing hook never reached onHookResult through the manager")
	}
}
