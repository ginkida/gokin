package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckImpactFindsUsages(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n\nfunc Widget() int { return 1 }\nvar _ = Widget()\n"), 0644); err != nil {
		t.Fatal(err)
	}
	res, err := NewCheckImpactTool(dir).Execute(context.Background(), map[string]any{"symbol": "Widget"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "Widget") {
		t.Fatalf("expected Widget in impact report, got: %s", res.Content)
	}
}

// TestCheckImpactNoMatchIsBenign confirms a genuine no-match (grep exit 1) still
// reports the honest "no significant impact" success — the benign path must not
// regress when we started inspecting grep's error.
func TestCheckImpactNoMatchIsBenign(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n"), 0644); err != nil {
		t.Fatal(err)
	}
	res, err := NewCheckImpactTool(dir).Execute(context.Background(), map[string]any{"symbol": "DefinitelyAbsentSymbolXYZ"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("genuine no-match should be a benign success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "No significant impact") {
		t.Fatalf("expected the honest no-impact message, got: %s", res.Content)
	}
}

// TestCheckImpactCancelledCtxIsHonestError pins the regression fix: a failed
// search (here: a cancelled context) must surface an honest error, NOT a false
// "symbol is private or unused" success that could invite deleting a live symbol.
func TestCheckImpactCancelledCtxIsHonestError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n\nfunc Widget() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before running — grep never completes a real search

	res, err := NewCheckImpactTool(dir).Execute(ctx, map[string]any{"symbol": "Widget"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Success {
		t.Fatalf("a cancelled/failed search must not report success, got: %s", res.Content)
	}
	if strings.Contains(res.Content, "No significant impact") || strings.Contains(res.Error, "No significant impact") {
		t.Fatalf("must not masquerade a failed search as 'unused', got content=%q error=%q", res.Content, res.Error)
	}
	if !strings.Contains(res.Error, "check_impact") {
		t.Fatalf("expected an honest check_impact error, got: %s", res.Error)
	}
}

// TestCheckImpactPartialFailureStillReports pins the unreadable-directory
// behavior: one locked directory under workDir must NOT break the tool — the
// report over the readable files is still produced. (History: the original
// system-grep implementation exited 2 here and an early guard turned that
// into an error on EVERY call; the pure-Go engine skips unreadable entries
// like the grep tool itself does.)
func TestCheckImpactPartialFailureStillReports(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not block reads")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.go"), []byte("package p\n\nfunc TargetSym() {}\nvar _ = TargetSym\n"), 0644); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "x.go"), []byte("package p\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0755) })

	tool := NewCheckImpactTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{"symbol": "TargetSym"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("a locked subdirectory must not break the report, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "TargetSym") {
		t.Fatalf("expected TargetSym usages in report, got: %s", res.Content)
	}
}

// TestCheckImpactNoSystemGrepNeeded pins the pure-Go engine migration: the
// tool must work with an empty PATH (no grep binary reachable) — on Windows
// or minimal containers the subprocess version was permanently broken.
func TestCheckImpactNoSystemGrepNeeded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n\nfunc Widget() {}\nvar _ = Widget\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")

	tool := NewCheckImpactTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{"symbol": "Widget"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success || !strings.Contains(res.Content, "Widget") {
		t.Fatalf("engine search must not depend on PATH, got success=%v content=%s err=%s", res.Success, res.Content, res.Error)
	}
}

// TestCheckImpactDeterministicOrder — searchParallel collects from goroutines;
// the report must sort by path so identical calls render identically.
func TestCheckImpactDeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"z.go", "a.go", "m.go"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("package p\nvar _ = Sym\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	tool := NewCheckImpactTool(dir)
	first := ""
	for i := range 5 {
		res, err := tool.Execute(context.Background(), map[string]any{"symbol": "Sym"})
		if err != nil || !res.Success {
			t.Fatalf("run %d failed: %v %s", i, err, res.Error)
		}
		if first == "" {
			first = res.Content
		} else if res.Content != first {
			t.Fatalf("run %d produced different report ordering", i)
		}
	}
}
