package app

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/config"
	"gokin/internal/tasks"
	"gokin/internal/tools"
)

// The ssh tool documents run_in_background in its schema, its description and
// a worked example — but the builder only ever wired the task manager into
// bash, so every attempt returned "background tasks not configured" since the
// feature shipped. Probe the real contract without spawning ssh: the manager
// check runs BEFORE host validation, so an invalid host distinguishes the two
// states (wired -> "invalid hostname"; unwired -> "not configured").
//
// Also pins the seam itself: every background-capable tool is bound in ONE
// place, so a task started anywhere is visible to /tasks, task_output and
// kill_shell.
func TestInitIntegrationsWiresSSHBackgroundTasks(t *testing.T) {
	dir := t.TempDir()
	b := &Builder{
		cfg:         config.DefaultConfig(),
		ctx:         context.Background(),
		workDir:     dir,
		registry:    tools.DefaultRegistry(dir),
		taskManager: tasks.NewManager(dir),
	}
	t.Cleanup(b.taskManager.CancelAll)

	b.wireBackgroundTaskTools()

	sshTool, ok := b.registry.Get("ssh")
	if !ok {
		t.Fatal("ssh tool missing from the registry")
	}
	res, err := sshTool.Execute(context.Background(), map[string]any{
		"host":              "bad host!",
		"command":           "echo hi",
		"run_in_background": true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := res.Error + res.Content
	if strings.Contains(got, "background tasks not configured") {
		t.Fatalf("ssh run_in_background is advertised but unwired: %q", got)
	}
	if !strings.Contains(got, "invalid hostname") {
		t.Fatalf("expected the invalid-host rejection past the manager check, got %q", got)
	}
}
