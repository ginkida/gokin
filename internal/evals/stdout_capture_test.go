package evals

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRunShellCommand_StdoutOnlyExcludesStderr pins the scoring fix: the agent's
// "answer" must be its STDOUT only. gokin and the toolchain write logs/warnings
// to stderr (e.g. the config-perms WARN that fires during config load); with
// CombinedOutput that noise was scored as the answer and made `falseFileClaims`
// flag the config path on every scenario (no_false_file_claims 0/26).
func TestRunShellCommand_StdoutOnlyExcludesStderr(t *testing.T) {
	// Agent capture (stdoutOnly=true): stderr is excluded from OutputPreview.
	agent := runShellCommand(context.Background(), t.TempDir(),
		"echo MODEL_ANSWER; echo LOG_NOISE 1>&2", time.Second, nil, true)
	if !agent.Success {
		t.Fatalf("command should succeed, err=%q", agent.Error)
	}
	if !strings.Contains(agent.OutputPreview, "MODEL_ANSWER") {
		t.Errorf("stdout-only must keep stdout, got %q", agent.OutputPreview)
	}
	if strings.Contains(agent.OutputPreview, "LOG_NOISE") {
		t.Errorf("stdout-only must EXCLUDE stderr, got %q", agent.OutputPreview)
	}

	// Verification capture (stdoutOnly=false): combined output keeps stderr so
	// build/test failures stay visible.
	verify := runShellCommand(context.Background(), t.TempDir(),
		"echo OUT; echo ERR_DETAIL 1>&2", time.Second, nil, false)
	if !strings.Contains(verify.OutputPreview, "ERR_DETAIL") {
		t.Errorf("combined output should include stderr, got %q", verify.OutputPreview)
	}
}
