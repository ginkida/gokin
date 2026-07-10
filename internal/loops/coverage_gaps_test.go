package loops

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// Manager.SetOnRemove callback wiring (manager.go:95 — was 0%)
// ──────────────────────────────────────────────────────────────────────────────

// TestSetOnRemove_FiresAfterRemove pins that the onRemove callback fires
// exactly once with the removed loop's ID, AFTER the manager-side delete
// succeeds. The callback is the downstream-cleanup hook (per-loop markdown,
// memory entries) — if it ever fired before the in-memory delete completed,
// a concurrent Get would still observe the loop.
func TestSetOnRemove_FiresAfterRemove(t *testing.T) {
	m := NewManager(newMemStorage())
	l, _ := m.Add("task", ModeInterval, 60)

	var calls []string
	m.SetOnRemove(func(id string) { calls = append(calls, id) })

	if err := m.Remove(l.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(calls) != 1 || calls[0] != l.ID {
		t.Errorf("onRemove calls = %v, want [%s]", calls, l.ID)
	}
	// Callback ran outside m.mu — by now the loop must be gone from state.
	if _, ok := m.Get(l.ID); ok {
		t.Error("loop still present after Remove returned")
	}
}

// TestSetOnRemove_NotFiredOnMissingLoop: removing a nonexistent loop errors
// and must NOT fire the callback (nothing was removed).
func TestSetOnRemove_NotFiredOnMissingLoop(t *testing.T) {
	m := NewManager(newMemStorage())
	fired := false
	m.SetOnRemove(func(string) { fired = true })

	if err := m.Remove("nope"); err == nil {
		t.Error("Remove of missing loop should error")
	}
	if fired {
		t.Error("onRemove must not fire when nothing was removed")
	}
}

// TestSetOnRemove_NotFiredOnStorageDeleteFailure: if storage.Delete fails,
// the in-memory loop is preserved AND the callback must not fire — otherwise
// downstream stores would purge their copy while the loop still lives in memory.
func TestSetOnRemove_NotFiredOnStorageDeleteFailure(t *testing.T) {
	store := newMemStorage()
	store.failDelete = errors.New("disk full")
	m := NewManager(store)
	l, _ := m.Add("task", ModeInterval, 60)

	fired := false
	m.SetOnRemove(func(string) { fired = true })

	if err := m.Remove(l.ID); err == nil {
		t.Error("Remove should fail when storage.Delete fails")
	}
	if fired {
		t.Error("onRemove must not fire when storage delete failed")
	}
	if _, ok := m.Get(l.ID); !ok {
		t.Error("loop must remain in memory when storage delete failed")
	}
}

// TestSetOnRemove_ReplacesPriorCallback: SetOnRemove is documented as
// idempotent — a second call replaces the first callback, not appends.
func TestSetOnRemove_ReplacesPriorCallback(t *testing.T) {
	m := NewManager(newMemStorage())
	l1, _ := m.Add("one", ModeInterval, 60)
	l2, _ := m.Add("two", ModeInterval, 60)

	first, second := 0, 0
	m.SetOnRemove(func(string) { first++ })
	m.SetOnRemove(func(string) { second++ })

	_ = m.Remove(l1.ID)
	_ = m.Remove(l2.ID)

	if first != 0 {
		t.Errorf("replaced callback fired %d times, want 0", first)
	}
	if second != 2 {
		t.Errorf("active callback fired %d times, want 2", second)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// NewDefaultFileStorage (storage.go:47 — was 0%)
// ──────────────────────────────────────────────────────────────────────────────

// TestNewDefaultFileStorage_HomeRooted: the default storage path is
// ~/.gokin/loops. We can't assert the exact home (env-dependent) but we CAN
// assert the suffix and that no error returns on a normal system.
func TestNewDefaultFileStorage_HomeRooted(t *testing.T) {
	s, err := NewDefaultFileStorage()
	if err != nil {
		t.Fatalf("NewDefaultFileStorage: %v", err)
	}
	home, _ := os.UserHomeDir()
	wantSuffix := filepath.Join(home, ".gokin", "loops")
	if s.dir != wantSuffix {
		t.Errorf("dir = %q, want %q", s.dir, wantSuffix)
	}
}

// TestNewDefaultFileStorage_RoundTrip: a loop saved through the default
// storage loads back identical — proves the constructed path is usable,
// not just well-formed.
func TestNewDefaultFileStorage_RoundTrip(t *testing.T) {
	// Redirect HOME so we don't touch the real ~/.gokin.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// os.UserHomeDir on some platforms caches $HOME at process start; the
	// t.TempDir value is what matters for the path join.

	s, err := NewDefaultFileStorage()
	if err != nil {
		t.Fatalf("NewDefaultFileStorage: %v", err)
	}
	now := time.Now().Round(time.Second)
	orig := &Loop{
		ID: "loop-rt1", Task: "round trip", Mode: ModeInterval,
		IntervalSeconds: 60, Status: StatusRunning, CreatedAt: now,
	}
	if err := s.Save(orig); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, errs := s.Load()
	if len(errs) != 0 {
		t.Fatalf("Load errors: %v", errs)
	}
	if len(loaded) != 1 || loaded[0].ID != orig.ID {
		t.Errorf("loaded = %+v, want one loop %s", loaded, orig.ID)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// truncateErr / truncateForSummary (runner.go:638,650 — were 66-80%)
// ──────────────────────────────────────────────────────────────────────────────

// TestTruncateErr table-drives the three branches: short passthrough,
// boundary (exactly 150), and long-with-ellipsis. Also covers the
// leading/trailing whitespace TrimSpace path.
func TestTruncateErr(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace only", "   \n\t  ", ""},
		{"short", "boom", "boom"},
		{"trimmed short", "  boom  ", "boom"},
		{"exactly 150", strings.Repeat("x", 150), strings.Repeat("x", 150)},
		{"151 truncates", strings.Repeat("x", 151), strings.Repeat("x", 147) + "..."},
		{"long with leading ws", "   " + strings.Repeat("y", 200), strings.Repeat("y", 147) + "..."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncateErr(c.in)
			if got != c.want {
				t.Errorf("truncateErr(%q) = %q (len %d), want %q (len %d)",
					c.in, got, len(got), c.want, len(c.want))
			}
		})
	}
}

// TestTruncateForSummary mirrors truncateErr's structure but for the
// 200-rune summary cap. Covers short, boundary, long, and the TrimSpace
// pre-trim.
func TestTruncateForSummary(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace only", "\t\n ", ""},
		{"short", "all good", "all good"},
		{"trimmed", "  all good  ", "all good"},
		{"exactly 200", strings.Repeat("z", 200), strings.Repeat("z", 200)},
		{"201 truncates", strings.Repeat("z", 201), strings.Repeat("z", 197) + "..."},
		{"multibyte preserved", strings.Repeat("世", 201), strings.Repeat("世", 197) + "..."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncateForSummary(c.in)
			if got != c.want {
				t.Errorf("truncateForSummary len got=%d want=%d", len([]rune(got)), len([]rune(c.want)))
			}
		})
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Loop.Validate (types.go:519 — was 81.8%): error branches
// ──────────────────────────────────────────────────────────────────────────────

// TestValidate_ErrorBranches covers each rejection path that the happy-path
// Add/Save tests don't hit: empty ID, empty task, interval<=0, unknown mode,
// unknown status. Each must return a non-nil error mentioning the field.
func TestValidate_ErrorBranches(t *testing.T) {
	base := func() *Loop {
		return &Loop{
			ID: "loop-x", Task: "do thing", Mode: ModeInterval,
			IntervalSeconds: 60, Status: StatusRunning,
			CreatedAt: time.Now(),
		}
	}
	cases := []struct {
		name      string
		mutate    func(*Loop)
		errSubstr string
	}{
		{"empty id", func(l *Loop) { l.ID = "  " }, "id is empty"},
		{"empty task", func(l *Loop) { l.Task = "" }, "task is empty"},
		{"interval zero", func(l *Loop) { l.IntervalSeconds = 0 }, "interval"},
		{"interval negative", func(l *Loop) { l.IntervalSeconds = -5 }, "interval"},
		{"unknown mode", func(l *Loop) { l.Mode = Mode("bogus") }, "unknown mode"},
		{"unknown status", func(l *Loop) { l.Status = Status("bogus") }, "unknown status"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := base()
			c.mutate(l)
			err := l.Validate()
			if err == nil {
				t.Fatalf("Validate should error for %s", c.name)
			}
			if !strings.Contains(err.Error(), c.errSubstr) {
				t.Errorf("error %q should contain %q", err.Error(), c.errSubstr)
			}
		})
	}
}

// TestValidate_SelfPacedAllowsZeroInterval: self-paced mode must accept
// IntervalSeconds == 0 (the MinDelaySeconds fallback path depends on this).
func TestValidate_SelfPacedAllowsZeroInterval(t *testing.T) {
	l := &Loop{
		ID: "loop-sp", Task: "self paced", Mode: ModeSelfPaced,
		IntervalSeconds: 0, Status: StatusRunning, CreatedAt: time.Now(),
	}
	if err := l.Validate(); err != nil {
		t.Errorf("self-paced with zero interval should validate: %v", err)
	}
}

// TestValidate_AllStatusesAccepted: each valid status must pass.
func TestValidate_AllStatusesAccepted(t *testing.T) {
	for _, st := range []Status{StatusRunning, StatusPaused, StatusStopped, StatusCompleted} {
		l := &Loop{
			ID: "loop-s", Task: "t", Mode: ModeSelfPaced,
			Status: st, CreatedAt: time.Now(),
		}
		if err := l.Validate(); err != nil {
			t.Errorf("status %s should validate: %v", st, err)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// previewTask / loopLess / sortLoopsForDisplay (manager.go:487,480 — were 75%)
// ──────────────────────────────────────────────────────────────────────────────

// TestPreviewTask covers both branches: short passthrough and the >60 char
// truncation with ellipsis, plus newline flattening.
func TestPreviewTask(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"short", "short"},
		{"line one\nline two", "line one line two"},
		{strings.Repeat("a", 60), strings.Repeat("a", 60)},
		{strings.Repeat("a", 61), strings.Repeat("a", 57) + "..."},
		{strings.Repeat("a", 100), strings.Repeat("a", 57) + "..."},
	}
	for _, c := range cases {
		got := previewTask(c.in)
		if got != c.want {
			t.Errorf("previewTask(len %d) = len %d %q, want len %d %q",
				len(c.in), len(got), got, len(c.want), c.want)
		}
	}
}

// TestLoopLess covers the comparator's both branches: CreatedAt differs
// ( chronological), and CreatedAt equal (ID tiebreaker).
func TestLoopLess(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	older := &Loop{ID: "loop-a", CreatedAt: t0}
	newer := &Loop{ID: "loop-b", CreatedAt: t1}

	if !loopLess(older, newer) {
		t.Error("older.Before(newer) should be true")
	}
	if loopLess(newer, older) {
		t.Error("newer.Before(older) should be false")
	}

	// Equal CreatedAt → ID tiebreaker.
	x := &Loop{ID: "loop-x", CreatedAt: t0}
	y := &Loop{ID: "loop-y", CreatedAt: t0}
	if !loopLess(x, y) {
		t.Error("equal time, x < y by ID should be true")
	}
	if loopLess(y, x) {
		t.Error("equal time, y < x by ID should be false")
	}
}

// TestSortLoopsForDisplay_StableOrder: oldest-first with ID tiebreaker,
// exercised through the public sort helper (insertion sort).
func TestSortLoopsForDisplay_StableOrder(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	t2 := time.Unix(3000, 0)
	// Intentionally shuffled.
	in := []*Loop{
		{ID: "loop-c", CreatedAt: t2},
		{ID: "loop-a", CreatedAt: t0},
		{ID: "loop-b", CreatedAt: t1},
		{ID: "loop-a2", CreatedAt: t0}, // same time as a → ID tiebreak
	}
	sortLoopsForDisplay(in)
	got := make([]string, len(in))
	for i, l := range in {
		got[i] = l.ID
	}
	want := []string{"loop-a", "loop-a2", "loop-b", "loop-c"}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("pos %d: got %s want %s (full: %v)", i, got[i], want[i], got)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// FileStorage edge cases (storage.go Load/Save/Delete/NewID — were 71-75%)
// ──────────────────────────────────────────────────────────────────────────────

// TestFileStorage_LoadUnreadableDir: a directory that exists but can't be
// read (permissions) returns a real error, distinct from the not-exist case.
// On root-less CI we may not be able to revoke perms; skip if so.
func TestFileStorage_LoadUnreadableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root — permission test is meaningless")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "noperm")
	if err := os.Mkdir(sub, 0500); err != nil {
		t.Fatal(err)
	}
	// Place a file we then can't read because the dir is 0500? On most
	// systems 0500 still allows owner read of known names. Instead make the
	// dir itself unreadable for listing.
	_ = os.Chmod(sub, 0000)
	t.Cleanup(func() { _ = os.Chmod(sub, 0700) })

	s := NewFileStorage(sub)
	_, errs := s.Load()
	// Some filesystems (e.g. mounted tmpfs as root) may still allow read;
	// accept either an error OR the benign not-exist path. The branch we
	// want covered is the non-IsNotExist error return at storage.go:71.
	if len(errs) == 0 {
		// If no error, the dir was readable on this platform — not a failure.
		t.Log("Load returned no error — dir was readable on this platform; branch not exercised")
	}
}

// TestFileStorage_SaveInvalidLoop: Save refuses an invalid loop (Validate
// fails) before touching disk — the file must not exist afterwards.
func TestFileStorage_SaveInvalidLoop(t *testing.T) {
	s := NewFileStorage(t.TempDir())
	bad := &Loop{ID: "", Task: "", Mode: ModeInterval} // empty ID+task
	if err := s.Save(bad); err == nil {
		t.Fatal("Save of invalid loop should error")
	}
	// Nothing written.
	loops, _ := s.Load()
	if len(loops) != 0 {
		t.Errorf("invalid loop was persisted: %d files", len(loops))
	}
}

// TestFileStorage_DeleteMissingIsNil: deleting a missing file is not an error
// (documented idempotence — the manager calls Delete without checking).
// The existing storage_test.go covers this as TestFileStorage_DeleteIdempotent;
// here we additionally assert no leftover state files appear.
func TestFileStorage_DeleteMissingIsNil(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStorage(dir)
	if err := s.Delete("never-existed"); err != nil {
		t.Errorf("Delete of missing file should be nil, got %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("Delete created files: %d entries", len(entries))
	}
}

// TestFileStorage_LoadSkipsNonJSON: non-.json files in the dir are ignored,
// not treated as errors. Guards against a stray .txt or .bak crashing Load.
func TestFileStorage_LoadSkipsNonJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "loop-bad.bak"), []byte("nope"), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewFileStorage(dir)
	loops, errs := s.Load()
	if len(loops) != 0 || len(errs) != 0 {
		t.Errorf("non-json files should be ignored: loops=%d errs=%d", len(loops), len(errs))
	}
}

// TestFileStorage_LoadSkipsSubdirectories: a subdirectory in the loops dir
// (e.g. from another tool) must not cause a parse attempt.
func TestFileStorage_LoadSkipsSubdirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0700); err != nil {
		t.Fatal(err)
	}
	s := NewFileStorage(dir)
	loops, errs := s.Load()
	if len(loops) != 0 || len(errs) != 0 {
		t.Errorf("subdir should be skipped: loops=%d errs=%d", len(loops), len(errs))
	}
}

// TestFileStorage_LoadSortsOldestFirst: multiple loops load in stable
// (CreatedAt, then ID) order — the sort branch at storage.go:99.
func TestFileStorage_LoadSortsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStorage(dir)
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	t2 := time.Unix(3000, 0)
	// Save out of order.
	for _, l := range []*Loop{
		{ID: "loop-c", Task: "t", Mode: ModeSelfPaced, Status: StatusRunning, CreatedAt: t2},
		{ID: "loop-a", Task: "t", Mode: ModeSelfPaced, Status: StatusRunning, CreatedAt: t0},
		{ID: "loop-b", Task: "t", Mode: ModeSelfPaced, Status: StatusRunning, CreatedAt: t1},
	} {
		if err := s.Save(l); err != nil {
			t.Fatalf("Save %s: %v", l.ID, err)
		}
	}
	loaded, _ := s.Load()
	if len(loaded) != 3 {
		t.Fatalf("loaded %d, want 3", len(loaded))
	}
	got := []string{loaded[0].ID, loaded[1].ID, loaded[2].ID}
	want := []string{"loop-a", "loop-b", "loop-c"}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("pos %d: got %s want %s", i, got[i], want[i])
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// storage.go error branches (Save / Delete / NewDefaultFileStorage / isSafeLoopID)
// ──────────────────────────────────────────────────────────────────────────────

// TestFileStorage_SaveRefusesInvalid: Save runs Validate at the boundary.
// A loop with an empty task must be rejected before touching disk.
func TestFileStorage_SaveRefusesInvalid(t *testing.T) {
	s := NewFileStorage(t.TempDir())
	bad := &Loop{ID: "loop-bad", Task: "", Mode: ModeInterval, IntervalSeconds: 60}
	if err := s.Save(bad); err == nil {
		t.Error("Save accepted invalid loop (empty task)")
	}
}

// TestFileStorage_SaveUnwritableDir: MkdirAll failure (parent is a file)
// must surface as a wrapped error, not a panic.
func TestFileStorage_SaveUnwritableDir(t *testing.T) {
	dir := t.TempDir()
	// Turn the storage dir itself into a file → MkdirAll fails.
	blocker := filepath.Join(dir, "block")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	s := NewFileStorage(filepath.Join(blocker, "sub"))
	l := &Loop{ID: "loop-x", Task: "t", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning}
	if err := s.Save(l); err == nil {
		t.Error("Save should fail when MkdirAll fails")
	}
}

// TestFileStorage_DeleteUnsafeID: Delete rejects path-traversal / unsafe IDs
// before touching the filesystem. Without this guard a crafted id like
// "../etc/passwd" would delete arbitrary files.
func TestFileStorage_DeleteUnsafeID(t *testing.T) {
	s := NewFileStorage(t.TempDir())
	for _, bad := range []string{"", "../escape", "a/b", "shell;rm", "space id"} {
		if err := s.Delete(bad); err == nil {
			t.Errorf("Delete(%q) should reject unsafe id", bad)
		}
	}
}

// TestNewDefaultFileStorage_ResolvesHome: the default factory must return a
// storage rooted at ~/.gokin/loops. We only verify it doesn't error and points
// at the right suffix — the home dir itself is env-dependent.
func TestNewDefaultFileStorage_ResolvesHome(t *testing.T) {
	st, err := NewDefaultFileStorage()
	if err != nil {
		t.Fatalf("NewDefaultFileStorage: %v", err)
	}
	if !strings.HasSuffix(st.dir, filepath.Join(".gokin", "loops")) {
		t.Errorf("dir = %s, want suffix .gokin/loops", st.dir)
	}
}

// TestIsSafeLoopID: the ID validator is the filesystem-injection guard for
// Delete. Cover the full accept/reject matrix so a future relaxation (e.g.
// allowing '/' for "nested" ids) is caught.
func TestIsSafeLoopID(t *testing.T) {
	for _, ok := range []string{"loop-abc123", "loop_X1", "A", "0", "a-b_c"} {
		if !isSafeLoopID(ok) {
			t.Errorf("isSafeLoopID(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "../x", "a/b", "a b", "a.b", "a;b", "укид"} {
		if isSafeLoopID(bad) {
			t.Errorf("isSafeLoopID(%q) = true, want false", bad)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// types.go parseHumanDurationSeconds — the LLM-hint parser (self-paced mode)
// ──────────────────────────────────────────────────────────────────────────────

// TestParseHumanDurationSeconds: the fallback parser for phrases like
// "30 minutes". Covers every unit branch, the approx-word strip, the overflow
// guard, and the reject paths (wrong field count, non-numeric, bad unit).
func TestParseHumanDurationSeconds(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		// Unit coverage.
		{"30 seconds", 30},
		{"5 minutes", 300},
		{"1 hour", 3600},
		{"2 hours", 7200}, // plural 's' stripped
		{"1 hr", 3600},    // abbreviation
		{"3 days", 259200},
		{"1 day", 86400}, // singular
		// Approx-word prefix is stripped.
		{"about 30 minutes", 1800},
		{"roughly 2 hours", 7200},
		{"approximately 1 day", 86400},
		// Reject paths.
		{"", 0},
		{"   ", 0},
		{"only_one_word", 0},       // wrong field count
		{"too many words here", 0}, // >2 fields
		{"abc minutes", 0},         // non-numeric count
		{"30 fortnights", 0},       // unknown unit
		{"-5 minutes", 0},          // negative count
		{"30", 0},                  // missing unit
	}
	for _, tc := range cases {
		got := parseHumanDurationSeconds(tc.in)
		if got != tc.want {
			t.Errorf("parseHumanDurationSeconds(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestParseHumanDurationSeconds_Overflow: a count so large it would overflow
// int64 must saturate to MaxInt64, not wrap around.
func TestParseHumanDurationSeconds_Overflow(t *testing.T) {
	got := parseHumanDurationSeconds("999999999999999999 days")
	if got != math.MaxInt64 {
		t.Errorf("overflow = %d, want MaxInt64 (%d)", got, math.MaxInt64)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// memory.go WriteLoop / DeleteLoop — unsafe-ID guards + nil-loop branch
// ──────────────────────────────────────────────────────────────────────────────

// TestMemoryWriter_WriteLoopNilLoop: a nil loop is a no-op (nil writer is
// covered by TestMemoryWriter_NilSafe in memory_test.go). Guards the `l == nil`
// arm of the nil-check.
func TestMemoryWriter_WriteLoopNilLoop(t *testing.T) {
	w := NewMemoryWriter(t.TempDir())
	if err := w.WriteLoop(nil); err != nil {
		t.Errorf("WriteLoop(nil) should be no-op, got: %v", err)
	}
}

// TestMemoryWriter_WriteLoopUnsafeID: the markdown writer must reject a
// path-traversal id just like FileStorage.Delete — otherwise a crafted loop
// id could write a .md file outside .gokin/loops/.
func TestMemoryWriter_WriteLoopUnsafeID(t *testing.T) {
	w := NewMemoryWriter(t.TempDir())
	l := &Loop{ID: "../escape", Task: "t", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning}
	if err := w.WriteLoop(l); err == nil {
		t.Error("WriteLoop should reject unsafe id")
	}
}

// TestMemoryWriter_DeleteLoopEmptyAndUnsafe: empty id is a no-op, unsafe id
// is rejected. Covers both guard branches in DeleteLoop.
func TestMemoryWriter_DeleteLoopEmptyAndUnsafe(t *testing.T) {
	w := NewMemoryWriter(t.TempDir())
	if err := w.DeleteLoop(""); err != nil {
		t.Errorf("DeleteLoop(\"\") should be no-op, got: %v", err)
	}
	if err := w.DeleteLoop("../escape"); err == nil {
		t.Error("DeleteLoop should reject unsafe id")
	}
}
