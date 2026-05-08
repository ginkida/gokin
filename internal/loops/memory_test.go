package loops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryWriter_NilSafe(t *testing.T) {
	// Empty workDir → returns nil. Nil writer is safe to call methods on.
	w := NewMemoryWriter("")
	if w != nil {
		t.Errorf("expected nil writer for empty workDir, got %v", w)
	}
	// Nil method receivers are safe — no panic.
	if err := w.WriteLoop(&Loop{ID: "loop-x"}); err != nil {
		t.Errorf("WriteLoop on nil writer should be no-op, got: %v", err)
	}
	if err := w.DeleteLoop("loop-x"); err != nil {
		t.Errorf("DeleteLoop on nil writer should be no-op, got: %v", err)
	}
}

func TestMemoryWriter_WriteAndDelete(t *testing.T) {
	dir := t.TempDir()
	w := NewMemoryWriter(dir)

	now := time.Now()
	l := &Loop{
		ID:              "loop-mem1",
		Task:            "Refactor user service",
		Mode:            ModeInterval,
		IntervalSeconds: 600,
		Status:          StatusRunning,
		CreatedAt:       now,
		LastRunAt:       now.Add(time.Minute),
		IterationCount:  2,
		Iterations: []Iteration{
			{N: 1, StartedAt: now, Duration: 30 * time.Second, Summary: "Mapped surface", OK: true},
			{N: 2, StartedAt: now.Add(time.Minute), Duration: 45 * time.Second, Summary: "Started migration", OK: true},
		},
	}

	if err := w.WriteLoop(l); err != nil {
		t.Fatalf("WriteLoop: %v", err)
	}

	path := filepath.Join(dir, ".gokin", "loops", "loop-mem1.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}

	got := string(data)
	checks := []string{
		"# Loop loop-mem1",
		"**Task:** Refactor user service",
		"**Status:** running",
		"interval (every 10m)",
		"## Recent iterations",
		"#2",
		"#1",
		"Started migration",
		"Mapped surface",
	}
	for _, want := range checks {
		if !strings.Contains(got, want) {
			t.Errorf("rendered markdown missing %q. Got:\n%s", want, got)
		}
	}

	// Newest first: #2 must appear before #1 in the output.
	if strings.Index(got, "#2") > strings.Index(got, "#1") {
		t.Error("iterations not in newest-first order")
	}

	// Delete should remove the file.
	if err := w.DeleteLoop("loop-mem1"); err != nil {
		t.Fatalf("DeleteLoop: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still exists after Delete: %v", err)
	}

	// Idempotent — second delete is no-op.
	if err := w.DeleteLoop("loop-mem1"); err != nil {
		t.Errorf("DeleteLoop should be idempotent: %v", err)
	}
}

func TestMemoryWriter_WriteLoop_RegeneratesOnUpdate(t *testing.T) {
	// User runs a loop for an hour — file should always reflect the
	// latest snapshot, not append-only history.
	dir := t.TempDir()
	w := NewMemoryWriter(dir)

	now := time.Now()
	l := &Loop{
		ID:        "loop-update",
		Task:      "Watch CI",
		Mode:      ModeSelfPaced,
		Status:    StatusRunning,
		CreatedAt: now,
	}

	// First iteration.
	l.Iterations = []Iteration{
		{N: 1, StartedAt: now, Duration: time.Second, Summary: "first", OK: true},
	}
	l.IterationCount = 1
	if err := w.WriteLoop(l); err != nil {
		t.Fatal(err)
	}

	// Second iteration overwrites first file.
	l.Iterations = append(l.Iterations,
		Iteration{N: 2, StartedAt: now.Add(time.Minute), Duration: time.Second, Summary: "second", OK: true})
	l.IterationCount = 2
	if err := w.WriteLoop(l); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, ".gokin", "loops", "loop-update.md")
	data, _ := os.ReadFile(path)
	got := string(data)

	// Both iterations present.
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Errorf("regenerated file missing iterations: %s", got)
	}
	// Header reflects updated count.
	if !strings.Contains(got, "**Iterations:** 2") {
		t.Errorf("header iteration count not updated: %s", got)
	}
}

func TestMemoryWriter_HandlesEmptyIterations(t *testing.T) {
	dir := t.TempDir()
	w := NewMemoryWriter(dir)

	l := &Loop{
		ID:        "loop-empty",
		Task:      "Just-created",
		Mode:      ModeSelfPaced,
		Status:    StatusRunning,
		CreatedAt: time.Now(),
	}
	if err := w.WriteLoop(l); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".gokin", "loops", "loop-empty.md"))
	got := string(data)
	if !strings.Contains(got, "_No iterations yet._") {
		t.Errorf("empty-iterations marker missing: %s", got)
	}
}

func TestMemoryWriter_FailedIterationMarker(t *testing.T) {
	dir := t.TempDir()
	w := NewMemoryWriter(dir)

	l := &Loop{
		ID:        "loop-fail",
		Task:      "Will fail",
		Mode:      ModeSelfPaced,
		Status:    StatusRunning,
		CreatedAt: time.Now(),
		Iterations: []Iteration{
			{N: 1, StartedAt: time.Now(), Duration: time.Second, Summary: "Iteration error: timeout", OK: false},
		},
	}
	_ = w.WriteLoop(l)

	data, _ := os.ReadFile(filepath.Join(dir, ".gokin", "loops", "loop-fail.md"))
	got := string(data)
	if !strings.Contains(got, "✗") {
		t.Errorf("failed iteration should be marked with ✗: %s", got)
	}
	if !strings.Contains(got, "Iteration error") {
		t.Errorf("failed iteration summary missing: %s", got)
	}
}

func TestRenderDuration(t *testing.T) {
	cases := []struct {
		seconds float64
		want    string
	}{
		{30, "30s"},
		{60, "1m"},
		{90, "1m"},
		{600, "10m"},
		{3600, "1h"},
		{3660, "1h1m"},
		{86400, "1d"},
		{90000, "1d1h"},
	}
	for _, tc := range cases {
		got := renderDuration(tc.seconds)
		if got != tc.want {
			t.Errorf("renderDuration(%v) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

func TestRenderMode_Formats(t *testing.T) {
	cases := []struct {
		l    *Loop
		want string
	}{
		{&Loop{Mode: ModeInterval, IntervalSeconds: 1800}, "interval (every 30m)"},
		{&Loop{Mode: ModeInterval, IntervalSeconds: 3600}, "interval (every 1h)"},
		{&Loop{Mode: ModeSelfPaced}, "self-paced"},
		{&Loop{Mode: ModeSelfPaced, MinDelaySeconds: DefaultMinDelaySeconds}, "self-paced"},
		{&Loop{Mode: ModeSelfPaced, MinDelaySeconds: 60}, "self-paced (min 1m between iterations)"},
	}
	for _, tc := range cases {
		got := renderMode(tc.l)
		if got != tc.want {
			t.Errorf("renderMode(%+v) = %q, want %q", tc.l, got, tc.want)
		}
	}
}
