package audit

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNewEntry(t *testing.T) {
	e := NewEntry("session-1", "bash", map[string]any{"command": "ls"})
	if e.ID == "" {
		t.Error("ID should be generated")
	}
	if e.ToolName != "bash" {
		t.Errorf("ToolName = %q", e.ToolName)
	}
	if e.SessionID != "session-1" {
		t.Errorf("SessionID = %q", e.SessionID)
	}
	if e.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}
}

func TestEntryComplete(t *testing.T) {
	e := NewEntry("s1", "read", nil)
	e.Complete("file content", true, "", 50*time.Millisecond)

	if e.Result != "file content" {
		t.Errorf("Result = %q", e.Result)
	}
	if !e.Success {
		t.Error("should be success")
	}
	if e.Duration != 50*time.Millisecond {
		t.Errorf("Duration = %v", e.Duration)
	}
}

func TestEntryMarshalJSON(t *testing.T) {
	e := NewEntry("s1", "bash", nil)
	e.Duration = 150 * time.Millisecond

	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Should contain duration_ms
	var raw map[string]any
	json.Unmarshal(data, &raw)
	if ms, ok := raw["duration_ms"].(float64); !ok || ms != 150 {
		t.Errorf("duration_ms = %v, want 150", raw["duration_ms"])
	}
}

func TestEntryUnmarshalJSON(t *testing.T) {
	jsonData := `{"id":"test","tool_name":"bash","duration_ms":250,"success":true,"timestamp":"2026-01-01T00:00:00Z","session_id":"s1"}`

	var e Entry
	if err := json.Unmarshal([]byte(jsonData), &e); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if e.Duration != 250*time.Millisecond {
		t.Errorf("Duration = %v, want 250ms", e.Duration)
	}
	if e.ToolName != "bash" {
		t.Errorf("ToolName = %q", e.ToolName)
	}
}

func TestEntryMatches(t *testing.T) {
	e := &Entry{
		ToolName:  "bash",
		SessionID: "s1",
		Success:   true,
		Timestamp: time.Now(),
		Duration:  100 * time.Millisecond,
	}

	// Match all
	if !e.Matches(QueryFilter{}) {
		t.Error("empty filter should match")
	}

	// Match tool
	if !e.Matches(QueryFilter{ToolName: "bash"}) {
		t.Error("matching tool should match")
	}
	if e.Matches(QueryFilter{ToolName: "read"}) {
		t.Error("different tool should not match")
	}

	// Match session
	if !e.Matches(QueryFilter{SessionID: "s1"}) {
		t.Error("matching session should match")
	}

	// Match success
	trueVal := true
	falseVal := false
	if !e.Matches(QueryFilter{Success: &trueVal}) {
		t.Error("success=true should match")
	}
	if e.Matches(QueryFilter{Success: &falseVal}) {
		t.Error("success=false should not match")
	}

	// Match time range
	if !e.Matches(QueryFilter{Since: time.Now().Add(-time.Hour)}) {
		t.Error("since 1h ago should match")
	}
	if e.Matches(QueryFilter{Since: time.Now().Add(time.Hour)}) {
		t.Error("since 1h from now should not match")
	}

	// Match min duration
	if !e.Matches(QueryFilter{MinDuration: 50 * time.Millisecond}) {
		t.Error("min 50ms should match 100ms entry")
	}
	if e.Matches(QueryFilter{MinDuration: 200 * time.Millisecond}) {
		t.Error("min 200ms should not match 100ms entry")
	}
}

func TestTruncateResult(t *testing.T) {
	short := "hello"
	if got := TruncateResult(short, 100); got != short {
		t.Errorf("short string should not be truncated: %q", got)
	}

	long := "a very long string that exceeds the limit"
	got := TruncateResult(long, 10)
	if len(got) > 30 { // 10 + "...[truncated]"
		t.Errorf("should be truncated: len=%d", len(got))
	}

	// Zero maxLen defaults to 1000
	got = TruncateResult(short, 0)
	if got != short {
		t.Error("zero maxLen should use default 1000")
	}
}

func TestSanitizeArgs(t *testing.T) {
	if got := SanitizeArgs(nil); got != nil {
		t.Error("nil should return nil")
	}

	args := map[string]any{
		"command": "echo hello",
		"path":    "/tmp/test",
	}
	result := SanitizeArgs(args)
	if result == nil {
		t.Fatal("should return non-nil")
	}
	// Normal values should be preserved
	if result["path"] != "/tmp/test" {
		t.Errorf("normal path should be preserved: %v", result["path"])
	}
}

// --- Logger tests ---

func TestLoggerDisabled(t *testing.T) {
	l, err := NewLogger(t.TempDir(), "test-session", Config{Enabled: false})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	e := NewEntry("test-session", "bash", nil)
	if err := l.Log(e); err != nil {
		t.Errorf("Log on disabled should succeed: %v", err)
	}
	if l.Len() != 0 {
		t.Error("disabled logger should have 0 entries")
	}
}

func TestLoggerLogAndQuery(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLogger(dir, "test-session", Config{
		Enabled:       true,
		MaxEntries:    100,
		MaxResultLen:  1000,
		RetentionDays: 30,
	})

	e1 := NewEntry("test-session", "bash", map[string]any{"command": "ls"})
	e1.Complete("output", true, "", 50*time.Millisecond)
	l.Log(e1)

	e2 := NewEntry("test-session", "read", map[string]any{"file_path": "/tmp/test"})
	e2.Complete("content", true, "", 10*time.Millisecond)
	l.Log(e2)

	if l.Len() != 2 {
		t.Errorf("Len = %d, want 2", l.Len())
	}

	// Query by tool
	results := l.Query(QueryFilter{ToolName: "bash"})
	if len(results) != 1 {
		t.Errorf("query bash: got %d, want 1", len(results))
	}

	// Query all
	results = l.Query(QueryFilter{})
	if len(results) != 2 {
		t.Errorf("query all: got %d, want 2", len(results))
	}

	// Query with limit
	results = l.Query(QueryFilter{Limit: 1})
	if len(results) != 1 {
		t.Errorf("query limit 1: got %d", len(results))
	}

	l.Flush()
}

func TestLoggerGetRecent(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLogger(dir, "s1", Config{Enabled: true, MaxEntries: 100, MaxResultLen: 1000, RetentionDays: 30})

	for i := 0; i < 5; i++ {
		e := NewEntry("s1", "bash", nil)
		l.Log(e)
	}

	recent := l.GetRecent(3)
	if len(recent) != 3 {
		t.Errorf("GetRecent(3) = %d entries", len(recent))
	}

	// More than available
	recent = l.GetRecent(100)
	if len(recent) != 5 {
		t.Errorf("GetRecent(100) = %d, want 5", len(recent))
	}

	// Zero/negative
	if l.GetRecent(0) != nil {
		t.Error("GetRecent(0) should return nil")
	}

	l.Flush()
}

func TestLoggerExport(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLogger(dir, "s1", Config{Enabled: true, MaxEntries: 100, MaxResultLen: 1000, RetentionDays: 30})

	e := NewEntry("s1", "bash", nil)
	l.Log(e)

	// JSON export
	data, err := l.Export("json")
	if err != nil {
		t.Errorf("Export json: %v", err)
	}
	if len(data) == 0 {
		t.Error("JSON export should not be empty")
	}

	// JSONL export
	data, err = l.Export("jsonl")
	if err != nil {
		t.Errorf("Export jsonl: %v", err)
	}
	if len(data) == 0 {
		t.Error("JSONL export should not be empty")
	}

	// Unsupported format
	_, err = l.Export("csv")
	if err == nil {
		t.Error("unsupported format should error")
	}

	l.Flush()
}

func TestLoggerClear(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLogger(dir, "s1", Config{Enabled: true, MaxEntries: 100, MaxResultLen: 1000, RetentionDays: 30})

	l.Log(NewEntry("s1", "bash", nil))
	l.Clear()

	if l.Len() != 0 {
		t.Errorf("after Clear, Len = %d", l.Len())
	}
}

func TestLoggerStats(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLogger(dir, "s1", Config{Enabled: true, MaxEntries: 100, MaxResultLen: 1000, RetentionDays: 30})

	e1 := NewEntry("s1", "bash", nil)
	e1.Complete("ok", true, "", 100*time.Millisecond)
	l.Log(e1)

	e2 := NewEntry("s1", "bash", nil)
	e2.Complete("", false, "error", 200*time.Millisecond)
	l.Log(e2)

	stats := l.Stats()
	if stats.TotalEntries != 2 {
		t.Errorf("TotalEntries = %d", stats.TotalEntries)
	}
	if stats.SuccessCount != 1 {
		t.Errorf("SuccessCount = %d", stats.SuccessCount)
	}
	if stats.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d", stats.ErrorCount)
	}
	if stats.ToolBreakdown["bash"] != 2 {
		t.Errorf("bash count = %d", stats.ToolBreakdown["bash"])
	}

	l.Flush()
}

func TestDefaultAuditConfig(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Enabled {
		t.Error("should be enabled by default")
	}
	if cfg.MaxEntries != 10000 {
		t.Errorf("MaxEntries = %d", cfg.MaxEntries)
	}
}
