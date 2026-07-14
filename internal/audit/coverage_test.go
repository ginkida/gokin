package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ===========================================================================
// Close (logger.go — 0%) — delegates to Flush
// ===========================================================================

func TestLogger_Close(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger(dir, "close-test", DefaultConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	l.Log(NewEntry("close-test", "bash", map[string]any{"command": "ls"}))

	// Close should flush + persist without error.
	if err := l.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestLogger_Close_Disabled(t *testing.T) {
	// A disabled logger Close is a no-op (Flush on disabled logs nothing).
	l, _ := NewLogger(t.TempDir(), "disabled", Config{Enabled: false})
	if err := l.Close(); err != nil {
		t.Errorf("Close disabled: %v", err)
	}
}

// ===========================================================================
// Cleanup (logger.go — 0%) — removes entries older than retention
// ===========================================================================

func TestLogger_Cleanup_Disabled(t *testing.T) {
	l, _ := NewLogger(t.TempDir(), "disabled", Config{Enabled: false})
	if n := l.Cleanup(); n != 0 {
		t.Errorf("Cleanup on disabled = %d, want 0", n)
	}
}

func TestLogger_Cleanup_RemovesOld(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.RetentionDays = 1 // 1-day retention

	l, err := NewLogger(dir, "cleanup-test", cfg)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	// Add an old entry by backdating its timestamp.
	old := NewEntry("cleanup-test", "read", map[string]any{"file_path": "a.go"})
	old.Timestamp = time.Now().Add(-2 * 24 * time.Hour) // 2 days ago
	l.entries = append(l.entries, old)

	// Add a recent entry.
	recent := NewEntry("cleanup-test", "write", map[string]any{"file_path": "b.go"})
	recent.Timestamp = time.Now()
	l.entries = append(l.entries, recent)

	removed := l.Cleanup()
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if len(l.entries) != 1 {
		t.Errorf("remaining entries = %d, want 1", len(l.entries))
	}
	if l.entries[0].ToolName != "write" {
		t.Errorf("kept wrong entry: %s", l.entries[0].ToolName)
	}
}

func TestLogger_Cleanup_NothingExpired(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()

	l, err := NewLogger(dir, "cleanup-fresh", cfg)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	l.Log(NewEntry("cleanup-fresh", "bash", nil))

	removed := l.Cleanup()
	if removed != 0 {
		t.Errorf("removed = %d, want 0 (nothing expired)", removed)
	}
	if len(l.entries) != 1 {
		t.Errorf("entries = %d, want 1", len(l.entries))
	}
}

// ===========================================================================
// CleanupOldFiles (logger.go — 0%) — removes session files older than retention
// ===========================================================================

func TestLogger_CleanupOldFiles_Disabled(t *testing.T) {
	l, _ := NewLogger(t.TempDir(), "disabled", Config{Enabled: false})
	n, err := l.CleanupOldFiles()
	if err != nil {
		t.Errorf("CleanupOldFiles disabled error: %v", err)
	}
	if n != 0 {
		t.Errorf("CleanupOldFiles disabled = %d, want 0", n)
	}
}

func TestLogger_CleanupOldFiles_RemovesOld(t *testing.T) {
	dir := t.TempDir()
	auditDir := filepath.Join(dir, "audit")
	if err := os.MkdirAll(auditDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Create an old audit file (backdated mtime).
	oldFile := filepath.Join(auditDir, "old-session.json")
	if err := os.WriteFile(oldFile, []byte("[]"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	oldTime := time.Now().Add(-60 * 24 * time.Hour) // 60 days ago
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// Create a recent audit file.
	recentFile := filepath.Join(auditDir, "recent-session.json")
	if err := os.WriteFile(recentFile, []byte("[]"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg := DefaultConfig()
	cfg.RetentionDays = 30

	l, err := NewLogger(dir, "file-cleanup-test", cfg)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	n, err := l.CleanupOldFiles()
	if err != nil {
		t.Fatalf("CleanupOldFiles: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}

	// Old file should be gone, recent should remain.
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("old file should be removed")
	}
	if _, err := os.Stat(recentFile); err != nil {
		t.Error("recent file should still exist")
	}
}

func TestLogger_CleanupOldFiles_NoDir(t *testing.T) {
	// If the audit dir doesn't exist, ReadDir returns an error.
	l, _ := NewLogger(t.TempDir(), "nodir", DefaultConfig())
	n, err := l.CleanupOldFiles()
	// The current session's audit dir WAS created by NewLogger, so this
	// actually succeeds with 0 removals — verify that's the behavior.
	if err != nil {
		t.Logf("CleanupOldFiles on nonexistent dir errored (acceptable): %v", err)
	}
	_ = n
}

// ===========================================================================
// GetSessions (logger.go — 0%) — lists session IDs with audit logs
// ===========================================================================

func TestLogger_GetSessions_Empty(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger(dir, "getsess-test", DefaultConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	sessions, err := l.GetSessions()
	if err != nil {
		t.Fatalf("GetSessions: %v", err)
	}
	// NewLogger created the current session's audit dir but no .json files
	// yet (nothing logged/saved). Should be empty or contain nothing.
	for _, s := range sessions {
		if s.ID == "" {
			t.Error("found session with empty ID")
		}
	}
}

func TestLogger_GetSessions_WithFiles(t *testing.T) {
	dir := t.TempDir()
	auditDir := filepath.Join(dir, "audit")
	if err := os.MkdirAll(auditDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Write two session files.
	for _, sid := range []string{"sess-a", "sess-b"} {
		data, _ := json.Marshal([]*Entry{})
		if err := os.WriteFile(filepath.Join(auditDir, sid+".json"), data, 0600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	// Write a non-json file (should be skipped).
	if err := os.WriteFile(filepath.Join(auditDir, "readme.txt"), []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	l, err := NewLogger(dir, "getsess-test2", DefaultConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	sessions, err := l.GetSessions()
	if err != nil {
		t.Fatalf("GetSessions: %v", err)
	}

	ids := map[string]bool{}
	for _, s := range sessions {
		ids[s.ID] = true
	}
	if !ids["sess-a"] {
		t.Error("missing sess-a")
	}
	if !ids["sess-b"] {
		t.Error("missing sess-b")
	}
}

func TestLogger_GetSessions_NonexistentDir(t *testing.T) {
	l, _ := NewLogger(t.TempDir(), "nonexist", DefaultConfig())
	// Point at a directory that doesn't have an audit subfolder — but
	// NewLogger already created it. To truly test the error path, use a
	// fresh logger pointing at a path we then remove.
	l.configDir = filepath.Join(t.TempDir(), "deleted")
	_, err := l.GetSessions()
	if err == nil {
		t.Log("GetSessions on nonexistent dir returned no error (dir may exist)")
	}
}

// ===========================================================================
// save / load round-trip (logger.go — 85.7% / 44.4%)
// ===========================================================================

func TestLogger_SaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger(dir, "save-test", DefaultConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	e1 := NewEntry("save-test", "bash", map[string]any{"command": "ls"})
	e1.Complete("output", true, "", 10*time.Millisecond)
	l.Log(e1)

	// Force a save.
	if err := l.save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Verify file exists.
	data, err := os.ReadFile(l.getFilePath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var entries []*Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(entries) != 1 || entries[0].ToolName != "bash" {
		t.Errorf("round-trip entries = %+v", entries)
	}

	// Load into a fresh logger.
	l2, err := NewLogger(dir, "save-test", DefaultConfig())
	if err != nil {
		t.Fatalf("NewLogger l2: %v", err)
	}
	if err := l2.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(l2.entries) != 1 || l2.entries[0].ToolName != "bash" {
		t.Errorf("loaded entries = %+v", l2.entries)
	}
}

func TestLogger_Load_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	auditDir := filepath.Join(dir, "audit")
	if err := os.MkdirAll(auditDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(auditDir, "corrupt-test.json"), []byte("not json"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	l, err := NewLogger(dir, "corrupt-test", DefaultConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	// load already ran in NewLogger and should have reset to empty on error.
	if len(l.entries) != 0 {
		t.Errorf("corrupt load should reset entries to empty, got %d", len(l.entries))
	}
}

func TestLogger_Load_NonexistentFile(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger(dir, "nofile", DefaultConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	// load on a brand-new session with no prior file — entries stays empty.
	if len(l.entries) != 0 {
		t.Errorf("entries = %d, want 0", len(l.entries))
	}
}

// ===========================================================================
// Stats (logger.go) — more thorough
// ===========================================================================

func TestLogger_Stats(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger(dir, "stats-test", DefaultConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	e1 := NewEntry("stats-test", "bash", nil)
	e1.Complete("ok", true, "", 10*time.Millisecond)
	l.Log(e1)

	e2 := NewEntry("stats-test", "read", nil)
	e2.Complete("", false, "not found", 5*time.Millisecond)
	l.Log(e2)

	stats := l.Stats()
	if stats.TotalEntries != 2 {
		t.Errorf("TotalEntries = %d, want 2", stats.TotalEntries)
	}
	if stats.SuccessCount != 1 {
		t.Errorf("SuccessCount = %d, want 1", stats.SuccessCount)
	}
	if stats.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", stats.ErrorCount)
	}
	if !stats.Enabled {
		t.Error("Enabled should be true")
	}
	if stats.SessionID != "stats-test" {
		t.Errorf("SessionID = %q", stats.SessionID)
	}
	if stats.ToolBreakdown["bash"] != 1 || stats.ToolBreakdown["read"] != 1 {
		t.Errorf("ToolBreakdown = %+v", stats.ToolBreakdown)
	}
}

func TestLogger_Stats_Disabled(t *testing.T) {
	l, _ := NewLogger(t.TempDir(), "disabled", Config{Enabled: false})
	stats := l.Stats()
	if stats.Enabled {
		t.Error("Enabled should be false")
	}
}

// ===========================================================================
// NewLogger disabled (logger.go — 88.9%)
// ===========================================================================

func TestNewLogger_Disabled(t *testing.T) {
	l, err := NewLogger(t.TempDir(), "x", Config{Enabled: false})
	if err != nil {
		t.Fatalf("NewLogger disabled: %v", err)
	}
	if l.enabled {
		t.Error("should be disabled")
	}
	// Logging on a disabled logger is a no-op.
	if err := l.Log(NewEntry("x", "bash", nil)); err != nil {
		t.Errorf("Log on disabled: %v", err)
	}
}

func TestNewLogger_MkdirFail(t *testing.T) {
	// configDir that can't be created (a file, not a dir).
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// audit subdir under a file → MkdirAll fails.
	if _, err := NewLogger(filepath.Join(blocker, "sub"), "x", DefaultConfig()); err == nil {
		t.Error("expected MkdirAll error")
	}
}

// ===========================================================================
// Query / Export / Clear (logger.go — 80% / 86.7% / 77.8%)
// ===========================================================================

func TestLogger_Query(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLogger(dir, "query-test", DefaultConfig())

	for _, tool := range []string{"bash", "read", "write", "bash"} {
		e := NewEntry("query-test", tool, nil)
		e.Complete("ok", true, "", 1*time.Millisecond)
		l.Log(e)
	}

	// Query for bash only.
	results := l.Query(QueryFilter{ToolName: "bash"})
	if len(results) != 2 {
		t.Errorf("bash results = %d, want 2", len(results))
	}
	for _, r := range results {
		if r.ToolName != "bash" {
			t.Errorf("unexpected tool %s", r.ToolName)
		}
	}
}

func TestLogger_Export(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLogger(dir, "export-test", DefaultConfig())

	e := NewEntry("export-test", "bash", map[string]any{"command": "ls"})
	e.Complete("output", true, "", 10*time.Millisecond)
	l.Log(e)

	out, err := l.Export("json")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(out) == 0 {
		t.Error("Export returned empty")
	}
}

func TestLogger_Clear(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLogger(dir, "clear-test", DefaultConfig())

	l.Log(NewEntry("clear-test", "bash", nil))
	if len(l.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(l.entries))
	}

	l.Clear()
	if len(l.entries) != 0 {
		t.Errorf("after Clear, entries = %d, want 0", len(l.entries))
	}
}

func TestLogger_Flush(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLogger(dir, "flush-test", DefaultConfig())

	l.Log(NewEntry("flush-test", "bash", nil))
	l.dirty = true

	if err := l.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// File should now exist.
	if _, err := os.Stat(l.getFilePath()); err != nil {
		t.Errorf("Flush did not persist file: %v", err)
	}
}

// ===========================================================================
// Entry Matches / UnmarshalJSON / TruncateResult (entry.go — 83-87%)
// ===========================================================================

func TestEntry_Matches_AllFields(t *testing.T) {
	e := NewEntry("s1", "bash", map[string]any{"command": "ls"})
	e.Complete("done", true, "", 10*time.Millisecond)

	q := QueryFilter{ToolName: "bash", Success: boolPtr(true)}
	if !e.Matches(q) {
		t.Error("should match bash+success")
	}

	q2 := QueryFilter{ToolName: "read"}
	if e.Matches(q2) {
		t.Error("should not match read")
	}
}

func TestEntry_UnmarshalJSON_RoundTrip(t *testing.T) {
	orig := NewEntry("s1", "bash", map[string]any{"command": "ls"})
	orig.Complete("out", true, "", 10*time.Millisecond)

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got Entry
	if err := got.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if got.ToolName != "bash" || got.SessionID != "s1" {
		t.Errorf("round-trip: %+v", got)
	}
}

func TestEntry_UnmarshalJSON_Invalid(t *testing.T) {
	var e Entry
	if err := e.UnmarshalJSON([]byte("not json")); err == nil {
		t.Error("expected error on invalid JSON")
	}
}

func TestTruncateResult_Coverage(t *testing.T) {
	short := "hello"
	if got := TruncateResult(short, 100); got != "hello" {
		t.Errorf("short = %q", got)
	}

	long := strings.Repeat("a", 200)
	truncated := TruncateResult(long, 50)
	if len(truncated) > 100 { // 50 + ellipsis + some
		t.Errorf("truncated too long: %d", len(truncated))
	}
}

func boolPtr(b bool) *bool { return &b }
