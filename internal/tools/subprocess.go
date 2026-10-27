package tools

import (
	"context"
	"os/exec"

	"gokin/internal/gitexec"
)

// newProcessGroupCommand creates a context-bound command whose cancellation
// reaches descendants as well as the immediate process. Git and GH routinely
// spawn hooks, credential helpers, SSH, pagers, and external diff/merge
// drivers; leader-only cancellation can otherwise leave those processes alive
// after the user presses Esc or a tool deadline expires.
func newProcessGroupCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	if name == "git" {
		cmd = gitexec.CommandContext(ctx, args...)
	}
	KillProcessGroupOnCancel(cmd)
	return cmd
}
