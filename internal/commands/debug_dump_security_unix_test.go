//go:build !windows

package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebugDumpRejectsSymlinkedConfigDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	external := t.TempDir()
	configDir := filepath.Join(root, "gokin")
	if err := os.Symlink(external, configDir); err != nil {
		t.Fatalf("create config symlink: %v", err)
	}

	app := &debugDumpApp{state: map[string]any{"state": "input"}}
	_, err := (&DebugDumpCommand{}).Execute(context.Background(), nil, app)
	if err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("Execute error = %v, want symlink rejection", err)
	}

	entries, readErr := os.ReadDir(external)
	if readErr != nil {
		t.Fatalf("read external target: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("debug dump escaped through config symlink: %v", entries)
	}
}
