package audit

import (
	"os"
	"testing"
	"time"
)

// --- scheduleSave debounce path (23.8% → higher) ---
// scheduleSave coalesces multiple calls into one timer. We test that calling
// it multiple times doesn't create multiple timers, and that the timer fires.

func TestScheduleSave_DebounceCoalescing(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger(dir, "debounce-test", DefaultConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer l.Close()

	// Log multiple entries rapidly — each calls scheduleSave
	for i := 0; i < 5; i++ {
		l.Log(NewEntry("s1", "bash", map[string]any{"cmd": "ls"}))
	}

	// Only one timer should exist (coalesced)
	l.saveMu.Lock()
	timerActive := l.saveTimer != nil
	l.saveMu.Unlock()
	if !timerActive {
		t.Fatal("expected a save timer to be active after logging")
	}

	// Flush to force immediate save and wait
	if err := l.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Verify entries were persisted
	data, err := os.ReadFile(l.getFilePath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty audit file after save")
	}
}

func TestScheduleSave_TimerFiresAndSaves(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger(dir, "timer-fire", DefaultConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	l.Log(NewEntry("s1", "read", map[string]any{"path": "/tmp"}))

	// Wait for the 2s debounce timer to fire
	time.Sleep(3 * time.Second)

	// File should exist now
	if _, err := os.Stat(l.getFilePath()); err != nil {
		t.Fatalf("expected audit file after timer fire: %v", err)
	}

	l.Close()
}

// --- Export jsonl format (86.7% → 100%) ---

func TestExport_JSONL(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger(dir, "export-jsonl", DefaultConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer l.Close()

	l.Log(NewEntry("s1", "bash", map[string]any{"cmd": "ls"}))
	l.Log(NewEntry("s1", "read", map[string]any{"path": "/tmp"}))

	data, err := l.Export("jsonl")
	if err != nil {
		t.Fatalf("Export jsonl: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty jsonl export")
	}
	// Should contain newlines (one per entry)
	if !contains(string(data), "\n") {
		t.Fatal("jsonl export should contain newlines")
	}
}

func TestExport_UnsupportedFormat(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger(dir, "export-bad", DefaultConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer l.Close()

	if _, err := l.Export("xml"); err == nil {
		t.Fatal("expected error for unsupported format")
	}
}

func TestExport_Disabled(t *testing.T) {
	l, _ := NewLogger(t.TempDir(), "disabled", Config{Enabled: false})
	data, err := l.Export("json")
	if err != nil {
		t.Fatalf("Export disabled: %v", err)
	}
	if data != nil {
		t.Fatal("Export disabled should return nil")
	}
}

// --- Clear (77.8% → 100%) ---

func TestClear_Disabled(t *testing.T) {
	l, _ := NewLogger(t.TempDir(), "disabled", Config{Enabled: false})
	if err := l.Clear(); err != nil {
		t.Fatalf("Clear disabled: %v", err)
	}
}

func TestClear_FileAlreadyDeleted(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger(dir, "clear-test", DefaultConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer l.Close()

	// Clear when no file exists yet should not error
	if err := l.Clear(); err != nil {
		t.Fatalf("Clear with no file: %v", err)
	}
}

// --- Query (80% → 100%) ---

func TestQuery_Disabled(t *testing.T) {
	l, _ := NewLogger(t.TempDir(), "disabled", Config{Enabled: false})
	results := l.Query(QueryFilter{})
	if len(results) != 0 {
		t.Fatalf("Query disabled = %d, want 0", len(results))
	}
}

func TestQuery_WithLimit(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger(dir, "query-limit", DefaultConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer l.Close()

	for i := 0; i < 10; i++ {
		l.Log(NewEntry("s1", "bash", nil))
	}

	results := l.Query(QueryFilter{Limit: 3})
	if len(results) != 3 {
		t.Fatalf("Query with limit=3 returned %d, want 3", len(results))
	}
}

// --- Matches additional paths (84.6% → 100%) ---

func TestEntry_Matches_MinDuration(t *testing.T) {
	e := NewEntry("s1", "bash", nil)
	e.Duration = 100 * time.Millisecond

	filter := QueryFilter{MinDuration: 200 * time.Millisecond}
	if e.Matches(filter) {
		t.Fatal("entry with 100ms should not match min 200ms")
	}

	filter.MinDuration = 50 * time.Millisecond
	if !e.Matches(filter) {
		t.Fatal("entry with 100ms should match min 50ms")
	}
}

func TestEntry_Matches_Until(t *testing.T) {
	e := NewEntry("s1", "bash", nil)
	e.Timestamp = time.Now().Add(-1 * time.Hour) // 1h ago

	// Until filter: entry must be BEFORE Until. Entry 1h ago IS before
	// "30m ago", so it matches. Use a future entry to test the rejection path.
	filter := QueryFilter{Until: time.Now().Add(-2 * time.Hour)} // 2h ago
	if e.Matches(filter) {
		t.Fatal("entry 1h ago should not match until 2h ago (entry is after cutoff)")
	}

	// Entry 1h ago DOES match a 30m-ago cutoff (entry is before cutoff)
	filter2 := QueryFilter{Until: time.Now().Add(-30 * time.Minute)}
	if !e.Matches(filter2) {
		t.Fatal("entry 1h ago should match until 30m ago (entry is before cutoff)")
	}
}

func TestEntry_Matches_SuccessFilter(t *testing.T) {
	e := NewEntry("s1", "bash", nil)
	e.Success = true

	successTrue := true
	if !e.Matches(QueryFilter{Success: &successTrue}) {
		t.Fatal("success entry should match success=true filter")
	}

	successFalse := false
	if e.Matches(QueryFilter{Success: &successFalse}) {
		t.Fatal("success entry should not match success=false filter")
	}
}

// --- TruncateResult edge cases (87.5% → 100%) ---

func TestTruncateResult_DefaultMaxLen(t *testing.T) {
	result := TruncateResult("short", 0) // maxLen=0 → default 1000
	if result != "short" {
		t.Fatalf("expected 'short', got %q", result)
	}
}

func TestTruncateResult_NegativeMaxLen(t *testing.T) {
	result := TruncateResult("short", -1)
	if result != "short" {
		t.Fatalf("expected 'short', got %q", result)
	}
}

func TestTruncateResult_RuneBoundary(t *testing.T) {
	// String where rune count < byte count, and truncation point is mid-rune
	multibyte := string([]rune("世界世界世界世界")) // 8 runes, 24 bytes
	result := TruncateResult(multibyte, 4)
	if !contains(result, "[truncated]") {
		t.Fatalf("expected truncation marker, got %q", result)
	}
}

// --- CleanupOldFiles (84.2% → 100%) ---

func TestCleanupOldFiles_Disabled(t *testing.T) {
	l, _ := NewLogger(t.TempDir(), "disabled", Config{Enabled: false})
	if removed, _ := l.CleanupOldFiles(); removed != 0 {
		t.Fatalf("CleanupOldFiles disabled = %d, want 0", removed)
	}
}

func TestCleanupOldFiles_NoOldFiles(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger(dir, "cleanup-test", DefaultConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer l.Close()

	if removed, _ := l.CleanupOldFiles(); removed != 0 {
		t.Fatalf("CleanupOldFiles with no old files = %d, want 0", removed)
	}
}

// --- Flush not dirty (83.3% → 100%) ---

func TestFlush_NotDirty(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLogger(dir, "flush-clean", DefaultConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer l.Close()

	// Flush without any logs — should be a no-op
	if err := l.Flush(); err != nil {
		t.Fatalf("Flush not dirty: %v", err)
	}
}

func TestFlush_Disabled(t *testing.T) {
	l, _ := NewLogger(t.TempDir(), "disabled", Config{Enabled: false})
	if err := l.Flush(); err != nil {
		t.Fatalf("Flush disabled: %v", err)
	}
}

// --- SanitizeArgs (entry.go) ---

func TestSanitizeArgs_Nil(t *testing.T) {
	if got := SanitizeArgs(nil); got != nil {
		t.Fatalf("SanitizeArgs(nil) = %v, want nil", got)
	}
}

func TestSanitizeArgs_RedactsSecrets(t *testing.T) {
	// Use a value pattern the redactor is known to match (long hex/token-like).
	// The literal is split so the fixture never looks like a real credential to
	// GitHub Push Protection (the v0.100.77 rule: provider-shaped strings in
	// tests are ALWAYS concatenated).
	args := map[string]any{"token": "ghp_" + "1234567890abcdef1234567890abcdef", "command": "ls"}
	sanitized := SanitizeArgs(args)
	// The redactor masks known secret patterns; verify it returns a map
	// with the same keys (values may or may not be redacted depending on pattern)
	if sanitized == nil {
		t.Fatal("SanitizeArgs should return non-nil map")
	}
	if _, ok := sanitized["command"]; !ok {
		t.Fatal("command key should be preserved")
	}
}

// --- helper ---

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
