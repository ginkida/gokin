package tasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The truncation notice must be honest about where the rest of the output
// lives (v0.100.90): with no output file it claimed "Full output in: " with
// an empty path; after a mid-stream write failure it advertised an
// incomplete file as the full output.
func TestSafeBufferTruncationNoticeHonesty(t *testing.T) {
	big := strings.Repeat("x", maxMemoryOutputBytes)

	// No file configured: no false "Full output in:" pointer.
	var noFile safeBuffer
	if _, err := noFile.Write([]byte(big)); err != nil {
		t.Fatal(err)
	}
	if _, err := noFile.Write([]byte("overflow")); err != nil {
		t.Fatal(err)
	}
	s := noFile.String()
	if strings.Contains(s, "Full output in") {
		t.Fatalf("notice advertises a file that was never created:\n...%s", s[len(s)-200:])
	}
	if !strings.Contains(s, "not retained") {
		t.Fatalf("notice must say the overflow was lost:\n...%s", s[len(s)-200:])
	}

	// Healthy file: the full-output pointer is correct and stays.
	var withFile safeBuffer
	path := filepath.Join(t.TempDir(), "out.log")
	if err := withFile.SetOutputFile(path); err != nil {
		t.Fatal(err)
	}
	if _, err := withFile.Write([]byte(big)); err != nil {
		t.Fatal(err)
	}
	if _, err := withFile.Write([]byte("overflow")); err != nil {
		t.Fatal(err)
	}
	if s := withFile.String(); !strings.Contains(s, "Full output in: "+path) {
		t.Fatalf("healthy file pointer missing:\n...%s", s[len(s)-200:])
	}

	// Mid-stream failure: the file is incomplete and must be labeled so.
	var failed safeBuffer
	failedPath := filepath.Join(t.TempDir(), "gone.log")
	if err := failed.SetOutputFile(failedPath); err != nil {
		t.Fatal(err)
	}
	failed.mu.Lock()
	failed.file.Close() // simulate the descriptor dying mid-stream
	failed.mu.Unlock()
	if _, err := failed.Write([]byte(big)); err != nil {
		t.Fatal(err)
	}
	if _, err := failed.Write([]byte("overflow")); err != nil {
		t.Fatal(err)
	}
	s = failed.String()
	if strings.Contains(s, "Full output in") {
		t.Fatalf("notice advertises an incomplete file as full output:\n...%s", s[len(s)-200:])
	}
	if !strings.Contains(s, "incomplete") {
		t.Fatalf("notice must disclose the incomplete file:\n...%s", s[len(s)-200:])
	}
}

// Output logs from PREVIOUS runs are swept at manager construction
// (v0.100.90): the in-process Cleanup only reaps tasks in the current map,
// so files from crashed/exited runs accumulated forever. Age-based so a
// concurrently-running second gokin's live logs (fresh mtime) are safe.
func TestNewManagerSweepsStaleOutputFiles(t *testing.T) {
	workDir := t.TempDir()
	dir := filepath.Join(workDir, ".gokin", "task-output")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "task_1_1.log")
	fresh := filepath.Join(dir, "task_2_1.log")
	other := filepath.Join(dir, "notes.txt")
	for _, p := range []string{stale, fresh, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-3 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, old, old); err != nil {
		t.Fatal(err)
	}

	NewManager(workDir)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale log should be swept, stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh log must survive: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("non-.log files must never be touched: %v", err)
	}
}
