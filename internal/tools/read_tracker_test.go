package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecentlyReadFiles_OrderByTurn(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	c := filepath.Join(dir, "c.txt")
	for _, p := range []string{a, b, c} {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tr := NewFileReadTracker()
	tr.CheckAndRecord(a, 0, 0, 1)
	tr.IncrementTurn()
	tr.CheckAndRecord(b, 0, 0, 1)
	tr.IncrementTurn()
	tr.CheckAndRecord(c, 0, 0, 1)
	// re-read a on the freshest turn — should promote to top.
	tr.CheckAndRecord(a, 100, 0, 1) // different range, same file

	got := tr.RecentlyReadFiles(10)
	if len(got) != 3 {
		t.Fatalf("expected 3 files, got %d: %v", len(got), got)
	}
	// Re-read of `a` on turn 2 should make it first.
	if got[0] != a {
		t.Errorf("first = %q, want %q (most recent)", got[0], a)
	}
}

func TestRecentlyReadFiles_LimitCap(t *testing.T) {
	dir := t.TempDir()
	tr := NewFileReadTracker()
	for i := 0; i < 20; i++ {
		p := filepath.Join(dir, "f.txt")
		os.WriteFile(p+string(rune('0'+i%10)), []byte("x"), 0644)
		tr.CheckAndRecord(p+string(rune('0'+i%10)), 0, i, 1)
	}
	got := tr.RecentlyReadFiles(5)
	if len(got) > 5 {
		t.Errorf("limit ignored, got %d", len(got))
	}
}

func TestRecentlyReadFiles_DeduplicatesByPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "dup.txt")
	os.WriteFile(p, []byte("x"), 0644)

	tr := NewFileReadTracker()
	tr.CheckAndRecord(p, 0, 100, 1)
	tr.CheckAndRecord(p, 100, 200, 1) // different range
	tr.CheckAndRecord(p, 200, 300, 1)

	got := tr.RecentlyReadFiles(10)
	if len(got) != 1 {
		t.Errorf("expected 1 unique file, got %d: %v", len(got), got)
	}
}

func TestRecentlyReadFiles_EmptyTracker(t *testing.T) {
	tr := NewFileReadTracker()
	got := tr.RecentlyReadFiles(10)
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

// CheckAndRecord reports an escalating dup count for repeated reads of the same
// unchanged range, computed under the lock so the executor never has to read the
// racy DupCount field off the shared record pointer.
func TestCheckAndRecord_DupCountEscalation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.go")
	if err := os.WriteFile(p, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tr := NewFileReadTracker()
	type step struct {
		wantDup   bool
		wantCount int
	}
	for i, s := range []step{
		{false, 0}, // first read: fresh record
		{true, 1},  // first re-read
		{true, 2},  // second re-read
		{true, 3},  // third re-read
	} {
		isDup, _, _, dupCount := tr.CheckAndRecord(p, 58, 20, 25)
		if isDup != s.wantDup || dupCount != s.wantCount {
			t.Fatalf("read #%d: isDup=%v count=%d, want isDup=%v count=%d", i+1, isDup, dupCount, s.wantDup, s.wantCount)
		}
	}

	// A different range is tracked independently — its dup count starts fresh.
	if isDup, _, _, count := tr.CheckAndRecord(p, 100, 20, 25); isDup || count != 0 {
		t.Fatalf("new range: isDup=%v count=%d, want isDup=false count=0", isDup, count)
	}

	// File content change resets the dup count for the original range.
	if err := os.WriteFile(p, []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if isDup, _, changed, count := tr.CheckAndRecord(p, 58, 20, 40); isDup || !changed || count != 0 {
		t.Fatalf("after change: isDup=%v changed=%v count=%d, want isDup=false changed=true count=0", isDup, changed, count)
	}
}

// dedupReadStub self-heals: stub on the 1st re-read (token savings), full content
// on the 2nd (amnesia recovery), stub again on the 3rd+ (genuine loop → the
// executor's stagnation recovery is the backstop).
func TestDedupReadStub_SelfHeals(t *testing.T) {
	rec := &FileReadRecord{TurnIndex: 4, ContentLen: 7794}

	stub1, ok1 := dedupReadStub(rec, "models.go", 1)
	if !ok1 {
		t.Fatal("dupCount 1: want stub (ok=true)")
	}
	if !strings.Contains(stub1, "Unchanged since turn 4") || !strings.Contains(stub1, "already in context") {
		t.Errorf("dupCount 1 stub missing actionable guidance: %q", stub1)
	}

	if _, ok2 := dedupReadStub(rec, "models.go", 2); ok2 {
		t.Error("dupCount 2: want full content (ok=false) to break the amnesia loop")
	}

	if _, ok3 := dedupReadStub(rec, "models.go", 3); !ok3 {
		t.Error("dupCount 3: want stub again (ok=true) — genuine loop, recovery is the backstop")
	}

	if _, ok := dedupReadStub(nil, "models.go", 1); ok {
		t.Error("nil record: want ok=false")
	}
}
