// Package gitexec constructs host Git commands with execution-bearing
// repository configuration disabled. Bash has its separate OS policy.
package gitexec

import (
	"context"
	"os"
	"os/exec"
)

type hooksKey struct{}
type executableKey struct{}

func WithExecutable(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, executableKey{}, path)
}

func CommandContext(ctx context.Context, args ...string) *exec.Cmd {
	hooks := os.DevNull
	if trusted, ok := ctx.Value(hooksKey{}).(string); ok {
		hooks = trusted
	}
	safe := []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + hooks}
	name := "git"
	if path, ok := ctx.Value(executableKey{}).(string); ok {
		name = path
	}
	return exec.CommandContext(ctx, name, append(safe, args...)...)
}
