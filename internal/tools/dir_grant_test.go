package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gokin/internal/testkit"
)

// TestSetAllowedDirsOnRegistryCoversAllScopingTools is the drift guard: every
// path-scoping tool (incl. refactor and the semantic tools, which were missing
// from the old hand-written builder loop) must learn about a granted dir.
func TestSetAllowedDirsOnRegistryCoversAllScopingTools(t *testing.T) {
	workDir := testkit.ResolvedTempDir(t)
	granted := testkit.ResolvedTempDir(t) // a distinct dir, outside workDir
	reg := DefaultRegistry(workDir)

	// refactor needs its workDir set (the builder does this during wiring).
	if rt, ok := reg.Get("refactor"); ok {
		if r, ok := rt.(*RefactorTool); ok {
			r.SetWorkDir(workDir)
		}
	}

	count := SetAllowedDirsOnRegistry(reg, []string{granted})
	if count < 13 {
		t.Fatalf("expected >=13 path-scoping tools updated, got %d", count)
	}

	// A file in the granted dir must now validate through the read tool.
	target := filepath.Join(granted, "f.txt")
	if err := os.WriteFile(target, []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	res, err := mustGet(t, reg, "read").Execute(context.Background(), map[string]any{"file_path": target})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("read of granted-dir file should succeed after grant, got: %s", res.Error)
	}

	// refactor was the previously-missing tool — confirm it now scopes the dir.
	if rt, ok := reg.Get("refactor"); ok {
		if r, ok := rt.(*RefactorTool); ok {
			if _, verr := r.pathValidator.ValidateDir(granted); verr != nil {
				t.Errorf("refactor should accept the granted dir after propagation: %v", verr)
			}
		}
	}
}

// TestSetAllowedDirsOnRegistryRevoke confirms passing an empty list resets each
// tool to workDir-only (how /remove-dir and /clear revoke a grant).
func TestSetAllowedDirsOnRegistryRevoke(t *testing.T) {
	workDir := testkit.ResolvedTempDir(t)
	granted := testkit.ResolvedTempDir(t)
	reg := DefaultRegistry(workDir)

	SetAllowedDirsOnRegistry(reg, []string{granted})
	target := filepath.Join(granted, "f.txt")
	if err := os.WriteFile(target, []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	// Revoke: now the granted dir must be rejected again.
	SetAllowedDirsOnRegistry(reg, nil)
	res, err := mustGet(t, reg, "read").Execute(context.Background(), map[string]any{"file_path": target})
	if err != nil {
		t.Fatal(err)
	}
	if res.Success {
		t.Error("after revoke, read of the (formerly granted) dir must fail")
	}
}

func mustGet(t *testing.T, reg *Registry, name string) Tool {
	t.Helper()
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("tool %q not in registry", name)
	}
	return tool
}
