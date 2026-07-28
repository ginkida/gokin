package tasks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// One manager owns every background task (foreground + sub-agent clones), so
// the per-task working directory must travel with the task instead of coming
// from the manager. An empty dir keeps the manager's own.
func TestStartInDirCarriesPerTaskWorkDir(t *testing.T) {
	base := t.TempDir()
	other := filepath.Join(base, "agent")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(base)
	t.Cleanup(m.CancelAll)

	explicitID, err := m.StartInDir(context.Background(), other, "sleep 5")
	if err != nil {
		t.Fatalf("StartInDir: %v", err)
	}
	explicit, ok := m.Get(explicitID)
	if !ok {
		t.Fatal("explicit-dir task missing from the manager")
	}
	if explicit.WorkDir != other {
		t.Fatalf("WorkDir = %q, want the explicit %q", explicit.WorkDir, other)
	}

	defaultID, err := m.StartInDir(context.Background(), "", "sleep 5")
	if err != nil {
		t.Fatalf("StartInDir(empty): %v", err)
	}
	fallback, ok := m.Get(defaultID)
	if !ok {
		t.Fatal("default-dir task missing from the manager")
	}
	if fallback.WorkDir != base {
		t.Fatalf("WorkDir = %q, want the manager default %q", fallback.WorkDir, base)
	}

	// Both live in ONE manager — the property that makes /tasks, task_output
	// and kill_shell able to see a sub-agent's task at all.
	if explicitID == defaultID {
		t.Fatal("task IDs must stay unique within the shared manager")
	}
}
