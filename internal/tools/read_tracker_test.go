package tools

import (
	"os"
	"path/filepath"
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
