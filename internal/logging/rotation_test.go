package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The documented 10 MiB cap used to be enforced only when the log file was
// OPENED — which never happens again inside a run, and never at all for the
// default per-process filename. A long detached `--bg` worker with --debug
// could therefore write an unbounded file.
func TestDebugLogRotatesDuringTheRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := newRotatingFileWriter(file, path, 0, 512)
	t.Cleanup(func() { _ = writer.Close() })

	record := []byte(strings.Repeat("x", 128) + "\n")
	for range 12 {
		if _, err := writer.Write(record); err != nil {
			t.Fatal(err)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 512 {
		t.Fatalf("live log grew to %d bytes past its %d byte limit", info.Size(), 512)
	}
	backup, err := os.Stat(path + ".old")
	if err != nil {
		t.Fatalf("rotation kept no backup: %v", err)
	}
	if backup.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v, want 0600 — diagnostics stay private", backup.Mode().Perm())
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("rotated log mode = %v, want 0600", info.Mode().Perm())
	}
}

// A single record must never be split across the rotation boundary.
func TestRotationNeverSplitsARecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := newRotatingFileWriter(file, path, 0, 100)
	t.Cleanup(func() { _ = writer.Close() })

	first := []byte(strings.Repeat("a", 80) + "\n")
	second := []byte(strings.Repeat("b", 80) + "\n")
	if _, err := writer.Write(first); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(second); err != nil {
		t.Fatal(err)
	}

	live, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(live) != string(second) {
		t.Fatalf("live log = %q, want exactly the second record", live)
	}
	rotated, err := os.ReadFile(path + ".old")
	if err != nil {
		t.Fatal(err)
	}
	if string(rotated) != string(first) {
		t.Fatalf("rotated log = %q, want exactly the first record", rotated)
	}
}

// EnablePathLogging must install the rotating sink, not a bare file.
func TestEnablePathLoggingInstallsRotatingSink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := EnablePathLogging(path, LevelDebug, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(DisableLogging)

	mu.RLock()
	sink := logSink
	mu.RUnlock()
	if sink == nil {
		t.Fatal("EnablePathLogging did not install a rotating sink")
	}
	Info("hello", "category", "test")
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("log file not written: %v", err)
	}
}

// A benign external rotation — a second Gokin process, logrotate, a manual mv
// of the shared gokin.log — must not kill this process's logging. Returning
// early from the unverifiable branch left the sink nil, and every later Write
// then discarded its record while reporting success.
func TestRotationReattachesAfterAnExternalRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := newRotatingFileWriter(file, path, 0, 128)
	t.Cleanup(func() { _ = writer.Close() })

	if _, err := writer.Write([]byte("before\n")); err != nil {
		t.Fatal(err)
	}
	// Someone else rotates the shared file out from under us.
	if err := os.Rename(path, path+".external"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("someone-elses\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Cross the limit so rotation runs against a path that is no longer ours.
	big := []byte(strings.Repeat("y", 200) + "\n")
	if _, err := writer.Write(big); err != nil {
		t.Fatalf("write after external rotation: %v", err)
	}
	if _, err := writer.Write([]byte("after\n")); err != nil {
		t.Fatalf("second write after external rotation: %v", err)
	}

	live, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(live), "after") {
		t.Fatalf("logging stopped after an external rotation; live log = %q", live)
	}
}
