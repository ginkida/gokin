package undo

import (
	"testing"
)

// TestTrackerNewTrackerWithSize covers the constructor branch: a non-positive
// size must fall back to DefaultMaxChanges (not silently 0, which would drop
// every recorded change). A positive size must be honored.
func TestTrackerNewTrackerWithSize(t *testing.T) {
	t.Run("zero falls back to default", func(t *testing.T) {
		tr := NewTrackerWithSize(0)
		if tr.maxSize != DefaultMaxChanges {
			t.Errorf("maxSize = %d, want default %d", tr.maxSize, DefaultMaxChanges)
		}
	})
	t.Run("negative falls back to default", func(t *testing.T) {
		tr := NewTrackerWithSize(-5)
		if tr.maxSize != DefaultMaxChanges {
			t.Errorf("maxSize = %d, want default %d", tr.maxSize, DefaultMaxChanges)
		}
	})
	t.Run("positive honored", func(t *testing.T) {
		tr := NewTrackerWithSize(3)
		if tr.maxSize != 3 {
			t.Errorf("maxSize = %d, want 3", tr.maxSize)
		}
		// Capacity eviction: with maxSize==3, a 4th Record evicts the oldest.
		for i := 0; i < 4; i++ {
			tr.Record(FileChange{ID: "c" + itoa(i), FilePath: "/f" + itoa(i)})
		}
		all := tr.List()
		if len(all) != 3 {
			t.Fatalf("after overflow, len = %d, want 3", len(all))
		}
		if all[0].ID != "c1" {
			t.Errorf("oldest after eviction = %q, want c1 (c0 should have been dropped)", all[0].ID)
		}
	})
}

// TestTrackerGetLast covers the empty case (nil) and the populated case
// (returns a pointer to the most recent change without mutating the stack).
func TestTrackerGetLast(t *testing.T) {
	t.Run("empty returns nil", func(t *testing.T) {
		if got := (&Tracker{}).GetLast(); got != nil {
			t.Errorf("empty tracker GetLast = %v, want nil", got)
		}
	})
	t.Run("returns most recent without popping", func(t *testing.T) {
		tr := NewTracker()
		tr.Record(FileChange{ID: "a", FilePath: "/a"})
		tr.Record(FileChange{ID: "b", FilePath: "/b"})
		got := tr.GetLast()
		if got == nil || got.ID != "b" {
			t.Fatalf("GetLast = %+v, want b", got)
		}
		if tr.Count() != 2 {
			t.Errorf("Count after GetLast = %d, GetLast must not mutate the stack", tr.Count())
		}
	})
}

// TestTrackerGetLastUnlocked mirrors GetLast for the lock-free accessor and
// pins the empty-slice branch (returns nil rather than panicking).
func TestTrackerGetLastUnlocked(t *testing.T) {
	if got := (&Tracker{}).GetLastUnlocked(); got != nil {
		t.Errorf("empty GetLastUnlocked = %v, want nil", got)
	}
	tr := NewTracker()
	tr.Record(FileChange{ID: "x", FilePath: "/x"})
	got := tr.GetLastUnlocked()
	if got == nil || got.ID != "x" {
		t.Fatalf("GetLastUnlocked = %+v, want x", got)
	}
}

// TestTrackerRecordUnlocked covers the lock-free recorder including the
// capacity-eviction branch (len >= maxSize drops the oldest).
func TestTrackerRecordUnlocked(t *testing.T) {
	t.Run("appends when under capacity", func(t *testing.T) {
		tr := NewTrackerWithSize(2)
		tr.RecordUnlocked(FileChange{ID: "1"})
		tr.RecordUnlocked(FileChange{ID: "2"})
		if got := tr.GetLastUnlocked(); got.ID != "2" {
			t.Errorf("last = %q, want 2", got.ID)
		}
	})
	t.Run("evicts oldest at capacity", func(t *testing.T) {
		tr := NewTrackerWithSize(2)
		tr.RecordUnlocked(FileChange{ID: "1"})
		tr.RecordUnlocked(FileChange{ID: "2"})
		tr.RecordUnlocked(FileChange{ID: "3"}) // triggers eviction branch
		all := tr.List()
		if len(all) != 2 || all[0].ID != "2" || all[1].ID != "3" {
			t.Errorf("after overflow List = %v, want [2 3]", ids(all))
		}
	})
}

// TestTrackerList returns a COPY: mutating the result must not affect the
// tracker, and the slice is ordered oldest-first.
func TestTrackerList(t *testing.T) {
	t.Run("empty returns empty not nil-stable", func(t *testing.T) {
		tr := NewTracker()
		got := tr.List()
		if len(got) != 0 {
			t.Errorf("empty List len = %d", len(got))
		}
	})
	t.Run("copy semantics and order", func(t *testing.T) {
		tr := NewTracker()
		tr.Record(FileChange{ID: "a"})
		tr.Record(FileChange{ID: "b"})
		got := tr.List()
		if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
			t.Fatalf("List = %v, want [a b] oldest-first", ids(got))
		}
		got[0].ID = "mutated"
		fresh := tr.List()
		if fresh[0].ID != "a" {
			t.Error("List did not return a defensive copy; internal slice was mutated")
		}
	})
}

// TestTrackerGetByID covers the found and not-found branches. The search is
// newest-first, which matters when duplicate IDs exist (returns the latest).
func TestTrackerGetByID(t *testing.T) {
	tr := NewTracker()
	tr.Record(FileChange{ID: "a", FilePath: "/a"})
	tr.Record(FileChange{ID: "b", FilePath: "/b"})
	got := tr.GetByID("b")
	if got == nil || got.FilePath != "/b" {
		t.Fatalf("GetByID(b) = %+v, want /b", got)
	}
	if tr.GetByID("missing") != nil {
		t.Error("GetByID(missing) should be nil")
	}
}

// TestTrackerRemoveByID covers removal (returns the change, shrinks the slice)
// and the not-found case (nil, tracker unchanged).
func TestTrackerRemoveByID(t *testing.T) {
	tr := NewTracker()
	tr.Record(FileChange{ID: "a", FilePath: "/a"})
	tr.Record(FileChange{ID: "b", FilePath: "/b"})
	tr.Record(FileChange{ID: "c", FilePath: "/c"})

	removed := tr.RemoveByID("b")
	if removed == nil || removed.ID != "b" {
		t.Fatalf("RemoveByID(b) = %+v, want b", removed)
	}
	if tr.Count() != 2 {
		t.Errorf("Count after remove = %d, want 2", tr.Count())
	}
	if tr.GetByID("b") != nil {
		t.Error("b still present after removal")
	}
	// a and c intact and in order.
	all := tr.List()
	if len(all) != 2 || all[0].ID != "a" || all[1].ID != "c" {
		t.Errorf("remaining = %v, want [a c]", ids(all))
	}

	if missing := tr.RemoveByID("nope"); missing != nil {
		t.Errorf("RemoveByID(missing) = %+v, want nil", missing)
	}
	if tr.Count() != 2 {
		t.Errorf("Count changed after removing missing ID: %d", tr.Count())
	}
}

// itoa is a tiny local int→string to avoid importing strconv into a test that
// otherwise only needs IDs. fmt.Sprintf would also work but pulls a bigger dep.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

func ids(cs []FileChange) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}
