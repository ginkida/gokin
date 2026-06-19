package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/security"
	"gokin/internal/testkit"
)

// newDirGateExecutor builds an executor over a real registry plus an external
// dir holding one file. The checker tracks a mutable allowed-set; the grant
// handler (when used) appends to it AND re-propagates to the registry so the
// live read tool's validator is rebuilt — exactly the production flow.
func newDirGateExecutor(t *testing.T) (*Executor, *Registry, string, string) {
	t.Helper()
	workDir := testkit.ResolvedTempDir(t)
	external := testkit.ResolvedTempDir(t)
	extFile := filepath.Join(external, "f.txt")
	if err := os.WriteFile(extFile, []byte("external content"), 0644); err != nil {
		t.Fatal(err)
	}
	reg := DefaultRegistry(workDir)
	exec := NewExecutor(reg, nil, time.Second)
	return exec, reg, external, extFile
}

func TestDirGate_GrantAllowsAccess(t *testing.T) {
	exec, reg, external, extFile := newDirGateExecutor(t)

	allowed := []string{filepath.Dir(external) + "/__none__"} // workDir only is implied by each tool; start with no grants
	exec.SetDirAccessChecker(func(p string) bool {
		ok, _ := security.IsPathWithinAny(p, allowed)
		return ok
	})
	granted := false
	exec.SetDirGrantHandler(func(ctx context.Context, tool, p string) (bool, error) {
		granted = true
		dir := filepath.Dir(p)
		allowed = append(allowed, dir)
		SetAllowedDirsOnRegistry(reg, []string{dir})
		return true, nil
	})

	res := exec.doExecuteTool(context.Background(), testFunctionCall("r1", "read", map[string]any{"file_path": extFile}))
	if !granted {
		t.Fatal("grant handler should have been invoked for an out-of-workspace path")
	}
	if !res.Success {
		t.Fatalf("read should succeed after grant, got error: %s", res.Error)
	}
}

func TestDirGate_DenyReturnsActionableError(t *testing.T) {
	exec, _, _, extFile := newDirGateExecutor(t)

	exec.SetDirAccessChecker(func(p string) bool { return false }) // everything is out-of-scope
	exec.SetDirGrantHandler(func(ctx context.Context, tool, p string) (bool, error) {
		return false, nil // user denied
	})

	res := exec.doExecuteTool(context.Background(), testFunctionCall("r2", "read", map[string]any{"file_path": extFile}))
	if res.Success {
		t.Fatal("read of a denied out-of-workspace path must not succeed")
	}
	if !strings.Contains(res.Error+res.Content, "/add-dir") {
		t.Errorf("deny result should be actionable (mention /add-dir), got: %s / %s", res.Error, res.Content)
	}
}

func TestDirGate_InWorkspaceNoPrompt(t *testing.T) {
	workDir := testkit.ResolvedTempDir(t)
	inFile := filepath.Join(workDir, "in.txt")
	if err := os.WriteFile(inFile, []byte("inside"), 0644); err != nil {
		t.Fatal(err)
	}
	reg := DefaultRegistry(workDir)
	exec := NewExecutor(reg, nil, time.Second)

	exec.SetDirAccessChecker(func(p string) bool {
		ok, _ := security.IsPathWithinAny(p, []string{workDir})
		return ok
	})
	prompted := false
	exec.SetDirGrantHandler(func(ctx context.Context, tool, p string) (bool, error) {
		prompted = true
		return true, nil
	})

	res := exec.doExecuteTool(context.Background(), testFunctionCall("r3", "read", map[string]any{"file_path": inFile}))
	if prompted {
		t.Error("an in-workspace path must NOT trigger the grant prompt")
	}
	if !res.Success {
		t.Fatalf("in-workspace read should succeed: %s", res.Error)
	}
}

func TestDirGate_RelativePathSkipsGate(t *testing.T) {
	workDir := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(workDir, "rel.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	reg := DefaultRegistry(workDir)
	exec := NewExecutor(reg, nil, time.Second)

	prompted := false
	exec.SetDirAccessChecker(func(p string) bool { return false })
	exec.SetDirGrantHandler(func(ctx context.Context, tool, p string) (bool, error) {
		prompted = true
		return false, nil
	})

	// A relative path is always workspace-relative — the gate must skip it (it
	// only guards absolute paths). We assert the GATE skipped (no prompt); the
	// read tool's own relative-path resolution is a separate concern and depends
	// on process cwd vs workDir, which differ under t.TempDir().
	_ = exec.doExecuteTool(context.Background(), testFunctionCall("r4", "read", map[string]any{"file_path": "rel.txt"}))
	if prompted {
		t.Error("a relative path must skip the gate (no prompt)")
	}
}

func TestDirGate_NilHandlerStillDefaultDenies(t *testing.T) {
	workDir := testkit.ResolvedTempDir(t)
	external := testkit.ResolvedTempDir(t)
	extFile := filepath.Join(external, "f.txt")
	if err := os.WriteFile(extFile, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	reg := DefaultRegistry(workDir)
	exec := NewExecutor(reg, nil, time.Second)
	// No gate callbacks wired — the tool's own PathValidator must still reject
	// the out-of-workspace path (default-deny holds without the gate).
	res := exec.doExecuteTool(context.Background(), testFunctionCall("r5", "read", map[string]any{"file_path": extFile}))
	if res.Success {
		t.Error("with no gate, an out-of-workspace read must still be rejected by the validator")
	}
}
