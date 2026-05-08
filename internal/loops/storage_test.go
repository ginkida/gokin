package loops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		id := NewID()
		if seen[id] {
			t.Fatalf("NewID generated duplicate: %s", id)
		}
		seen[id] = true
	}
}

func TestFileStorage_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStorage(dir)

	now := time.Now().Round(time.Second)
	original := &Loop{
		ID:              "loop-test1234",
		Task:            "Write a test for the loop system",
		Mode:            ModeInterval,
		IntervalSeconds: 1800,
		MinDelaySeconds: 300,
		Status:          StatusRunning,
		CreatedAt:       now,
		UpdateMemory:    true,
	}

	if err := s.Save(original); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loops, errs := s.Load()
	if len(errs) > 0 {
		t.Fatalf("Load returned errors: %v", errs)
	}
	if len(loops) != 1 {
		t.Fatalf("Load returned %d loops, want 1", len(loops))
	}

	got := loops[0]
	if got.ID != original.ID {
		t.Errorf("ID mismatch: got %q want %q", got.ID, original.ID)
	}
	if got.Task != original.Task {
		t.Errorf("Task mismatch: got %q want %q", got.Task, original.Task)
	}
	if got.Mode != original.Mode {
		t.Errorf("Mode mismatch: got %q want %q", got.Mode, original.Mode)
	}
	if got.IntervalSeconds != original.IntervalSeconds {
		t.Errorf("IntervalSeconds: got %d want %d", got.IntervalSeconds, original.IntervalSeconds)
	}
}

// TestFileStorage_LoadEmptyDir verifies first-run UX: loading from a
// directory that doesn't exist yet should return (nil, nil), not an
// error. Otherwise every fresh install would fail at startup.
func TestFileStorage_LoadEmptyDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "doesnt-exist")
	s := NewFileStorage(dir)
	loops, errs := s.Load()
	if len(loops) != 0 {
		t.Errorf("expected no loops, got %d", len(loops))
	}
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

// TestFileStorage_CorruptFileSkipped: a corrupt JSON file must not
// block loading the rest. Returns the parse error in errs so the
// manager can log it, but continues reading the directory.
func TestFileStorage_CorruptFileSkipped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "loop-corrupt.json"), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewFileStorage(dir)

	good := &Loop{
		ID: "loop-good", Task: "task", Mode: ModeInterval,
		IntervalSeconds: 60, Status: StatusRunning, CreatedAt: time.Now(),
	}
	if err := s.Save(good); err != nil {
		t.Fatalf("Save good: %v", err)
	}

	loops, errs := s.Load()
	if len(loops) != 1 {
		t.Errorf("expected 1 good loop loaded, got %d", len(loops))
	}
	if len(errs) != 1 {
		t.Errorf("expected 1 error for corrupt file, got %d: %v", len(errs), errs)
	}
}

// TestFileStorage_DeleteIdempotent: deleting a non-existent loop
// must not error. Manager.Remove relies on this so repeated /loop
// remove calls don't surface confusing "file not found" messages.
func TestFileStorage_DeleteIdempotent(t *testing.T) {
	s := NewFileStorage(t.TempDir())
	if err := s.Delete("loop-nope"); err != nil {
		t.Errorf("Delete on missing should be no-op, got: %v", err)
	}
}

// TestFileStorage_RefuseInvalid: validation runs at Save boundary.
// Without this guard, a typo in code could silently persist a loop
// that fails Validate on next Load.
func TestFileStorage_RefuseInvalid(t *testing.T) {
	s := NewFileStorage(t.TempDir())
	bad := &Loop{ID: "loop-x", Task: "", Mode: ModeInterval, IntervalSeconds: 60}
	if err := s.Save(bad); err == nil {
		t.Error("Save accepted invalid loop (empty task)")
	}
}

// TestUnmarshal_RejectsInvalid: same guard at Load boundary. A
// hand-edited file with task removed or mode set to garbage should
// fail to parse, not silently load.
func TestUnmarshal_RejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"empty task":  `{"id":"loop-x","task":"","mode":"interval","interval_seconds":60,"status":"running"}`,
		"unknown mode": `{"id":"loop-x","task":"t","mode":"weird","status":"running"}`,
		"unknown status": `{"id":"loop-x","task":"t","mode":"interval","interval_seconds":60,"status":"weird"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Unmarshal([]byte(raw)); err == nil {
				t.Errorf("expected parse error for %s", name)
			}
		})
	}
}

// TestLoop_AppendIteration_TrimsHistory: state file can't grow
// unbounded over a multi-day loop. After MaxHistorySize entries,
// oldest must drop off.
func TestLoop_AppendIteration_TrimsHistory(t *testing.T) {
	l := &Loop{
		ID: "loop-x", Task: "t", Mode: ModeInterval,
		IntervalSeconds: 60, Status: StatusRunning, CreatedAt: time.Now(),
	}
	for i := range MaxHistorySize + 5 {
		l.AppendIteration(Iteration{N: i + 1, StartedAt: time.Now(), Duration: time.Second, Summary: "ok", OK: true})
	}
	if len(l.Iterations) != MaxHistorySize {
		t.Errorf("history not trimmed to %d, got %d", MaxHistorySize, len(l.Iterations))
	}
	if l.IterationCount != MaxHistorySize+5 {
		t.Errorf("lifetime count should track all appends, got %d", l.IterationCount)
	}
	// First iteration in slice should be N=6 (we appended 1..55, kept last 50).
	if l.Iterations[0].N != 6 {
		t.Errorf("oldest kept iteration should be N=6, got N=%d", l.Iterations[0].N)
	}
}

// TestLoop_AppendIteration_CompletesOnMaxIterations: hitting
// MaxIterations marks the loop completed (terminal). Scheduler
// uses IsActive to skip completed loops.
func TestLoop_AppendIteration_CompletesOnMaxIterations(t *testing.T) {
	l := &Loop{
		ID: "loop-x", Task: "t", Mode: ModeInterval,
		IntervalSeconds: 60, Status: StatusRunning, CreatedAt: time.Now(),
		MaxIterations: 3,
	}
	for i := range 3 {
		l.AppendIteration(Iteration{N: i + 1, StartedAt: time.Now(), Duration: time.Second, OK: true})
	}
	if l.Status != StatusCompleted {
		t.Errorf("expected status completed after 3/3 iterations, got %q", l.Status)
	}
	if l.IsActive() {
		t.Error("completed loop should not be active")
	}
}

// TestLoop_NextRunAt_IntervalMode: NextRunAt advances by interval
// after each iteration.
func TestLoop_NextRunAt_IntervalMode(t *testing.T) {
	now := time.Now()
	l := &Loop{
		ID: "loop-x", Task: "t", Mode: ModeInterval,
		IntervalSeconds: 600, Status: StatusRunning, CreatedAt: now,
	}
	l.AppendIteration(Iteration{N: 1, StartedAt: now, Duration: 30 * time.Second, OK: true})
	want := now.Add(30*time.Second + 600*time.Second)
	if !l.NextRunAt.Equal(want) {
		t.Errorf("NextRunAt = %v, want %v (start + duration + interval)", l.NextRunAt, want)
	}
}

// TestLoop_NextRunAt_SelfPacedRespectsHint: a self-paced iteration
// can suggest a longer wait via NextHint. The runner respects it
// (but never goes below MinDelaySeconds).
func TestLoop_NextRunAt_SelfPacedRespectsHint(t *testing.T) {
	now := time.Now()
	l := &Loop{
		ID: "loop-x", Task: "t", Mode: ModeSelfPaced,
		MinDelaySeconds: 60, Status: StatusRunning, CreatedAt: now,
	}
	l.AppendIteration(Iteration{
		N: 1, StartedAt: now, Duration: time.Second, OK: true,
		NextHint: "wait 30m",
	})
	want := now.Add(time.Second + 30*time.Minute)
	if !l.NextRunAt.Equal(want) {
		t.Errorf("NextRunAt = %v, want %v (start + duration + 30m hint)", l.NextRunAt, want)
	}
}

// TestParseNextHintSeconds: cover the common phrasings the model
// might use in iteration summaries.
func TestParseNextHintSeconds(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"30m", 1800},
		{"wait 30m", 1800},
		{"in 1h", 3600},
		{"after 5m", 300},
		{"next 2h", 7200},
		{"every 10m", 600},
		{"garbage", 0},
		{"-5m", 0}, // negative ignored
	}
	for _, tc := range cases {
		got := parseNextHintSeconds(tc.in)
		if got != tc.want {
			t.Errorf("parseNextHintSeconds(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestLoop_IsDue: scheduler invariants. A loop is due when active
// AND NextRunAt has passed (or is unset = first fire).
func TestLoop_IsDue(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		l    *Loop
		want bool
	}{
		{"running, no next", &Loop{Status: StatusRunning}, true},
		{"running, future", &Loop{Status: StatusRunning, NextRunAt: now.Add(time.Minute)}, false},
		{"running, past", &Loop{Status: StatusRunning, NextRunAt: now.Add(-time.Minute)}, true},
		{"paused, past", &Loop{Status: StatusPaused, NextRunAt: now.Add(-time.Minute)}, false},
		{"stopped, past", &Loop{Status: StatusStopped, NextRunAt: now.Add(-time.Minute)}, false},
		{"completed, past", &Loop{Status: StatusCompleted, NextRunAt: now.Add(-time.Minute)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.l.IsDue(now); got != tc.want {
				t.Errorf("IsDue = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUnmarshal_PreservesUnknownFields would assert forward-compat,
// but Go's json.Unmarshal silently drops unknown fields by default.
// Documenting that here so future readers know — if we ever add a
// new field, old gokin processes will still load the file (without
// the new field), not error.
func TestUnmarshal_PreservesNumericFields(t *testing.T) {
	raw := `{"id":"loop-x","task":"t","mode":"interval","interval_seconds":600,"max_iterations":7,"min_delay_seconds":120,"status":"running","update_memory":true,"created_at":"2026-05-08T03:00:00Z","iteration_count":42}`
	l, err := Unmarshal([]byte(raw))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if l.IntervalSeconds != 600 || l.MaxIterations != 7 || l.MinDelaySeconds != 120 || l.IterationCount != 42 {
		// Round-trip check — re-marshal to make sure nothing's dropped.
		out, _ := json.Marshal(l)
		t.Errorf("numeric fields not preserved: %s", out)
	}
}
