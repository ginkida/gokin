package toolusage

import (
	"os"
	"path/filepath"
	"testing"
)

// The point of this ledger is retention: an in-memory counter already existed
// and could not answer "has anyone ever reached for this tool".
func TestCountsSurviveReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool_usage.json")
	first := NewLedger(path)
	for range 3 {
		first.Record("repl_exec")
	}
	first.Record("grep")
	if err := first.Flush(); err != nil {
		t.Fatal(err)
	}

	second := NewLedger(path)
	counts := second.Snapshot()
	if counts["repl_exec"] != 3 || counts["grep"] != 1 {
		t.Fatalf("counts did not survive reload: %v", counts)
	}

	// A second session must accumulate rather than restart the count.
	second.Record("repl_exec")
	if err := second.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := NewLedger(path).Snapshot()["repl_exec"]; got != 4 {
		t.Fatalf("second session count = %d, want 4 (accumulated)", got)
	}
}

// The periodic flush must actually fire on its own, or every count between
// startup and shutdown is riding on the shutdown path alone.
func TestPeriodicFlushPersistsWithoutExplicitFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool_usage.json")
	ledger := NewLedger(path)
	for range flushEveryNRecords {
		ledger.Record("read")
	}
	if got := NewLedger(path).Snapshot()["read"]; got != int64(flushEveryNRecords) {
		t.Fatalf("periodic flush did not persist: got %d, want %d", got, flushEveryNRecords)
	}
}

// NeverUsed is the actionable half: it must name tools the model was offered
// and never chose, and must not invent names it has no counts for.
func TestNeverUsedReportsOfferedButUnchosen(t *testing.T) {
	ledger := NewLedger(filepath.Join(t.TempDir(), "u.json"))
	ledger.Record("grep")
	got := ledger.NeverUsed([]string{"grep", "repl_exec", "ssh"})
	if len(got) != 2 || got[0] != "repl_exec" || got[1] != "ssh" {
		t.Fatalf("never-used = %v, want [repl_exec ssh]", got)
	}
	if len(ledger.NeverUsed(nil)) != 0 {
		t.Fatal("an empty known set must yield no verdict")
	}
}

// Losing usage history must never be able to stop the app from starting.
func TestCorruptAndMissingFilesDegradeQuietly(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	ledger := NewLedger(corrupt)
	if len(ledger.Snapshot()) != 0 {
		t.Fatal("corrupt file must load as empty")
	}
	ledger.Record("read")
	if err := ledger.Flush(); err != nil {
		t.Fatalf("a corrupt file must be overwritten, not fatal: %v", err)
	}
	if NewLedger(corrupt).Snapshot()["read"] != 1 {
		t.Fatal("recovery write did not take")
	}

	if len(NewLedger(filepath.Join(dir, "missing.json")).Snapshot()) != 0 {
		t.Fatal("missing file must load as empty")
	}
}

// A nil ledger is the shape every caller sees before wiring exists; it must be
// inert rather than a panic on the tool hot path.
func TestNilLedgerIsInert(t *testing.T) {
	var ledger *Ledger
	ledger.Record("read")
	if ledger.Snapshot() != nil || ledger.NeverUsed([]string{"read"}) == nil {
		t.Fatal("nil ledger must snapshot nil and still answer NeverUsed")
	}
	if err := ledger.Flush(); err != nil {
		t.Fatal(err)
	}
}

// A failed write must not drop the counts it was carrying.
func TestFailedWriteRetriesOnNextFlush(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "sub", "u.json")
	if err := os.WriteFile(filepath.Join(dir, "sub"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ledger := NewLedger(blocked)
	ledger.Record("read")
	if err := ledger.Flush(); err == nil {
		t.Skip("environment allowed the write; retry path not exercised")
	}
	if err := os.Remove(filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Flush(); err != nil {
		t.Fatalf("retry flush failed: %v", err)
	}
	if NewLedger(blocked).Snapshot()["read"] != 1 {
		t.Fatal("count carried by the failed write was dropped")
	}
}
