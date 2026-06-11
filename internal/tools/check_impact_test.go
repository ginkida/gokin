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

// TestCheckImpactPartialFailureStillReports pins the exit-2-with-matches fix:
// one unreadable directory under workDir makes grep exit 2 while still printing
// every real match — the matches must become a report (with a coverage warning),
// not an error, or the blast-radius tool is permanently broken in any repo with
// a single locked directory (review-confirmed empirically on macOS).
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
		t.Fatalf("matches + partial grep failure must still produce a report, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "TargetSym") {
		t.Fatalf("expected TargetSym usages in report, got: %s", res.Content)
	}
	if !strings.Contains(res.Content, "coverage may be partial") {
		t.Fatalf("expected partial-coverage warning, got: %s", res.Content)
	}
}
