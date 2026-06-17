package chat

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadSessionMeta pins the metadata-only session read (the v0.100.13 audit
// follow-up): it counts history entries WITHOUT deep-parsing each message, and
// ignores the heavy fields (total_tokens, system_instruction, …). The history
// entries here are deliberately minimal `{"role":...}` shapes that would NOT
// fully unmarshal into SerializedContent — proving we only count, never decode.
func TestLoadSessionMeta(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-1.json")
	raw := `{
		"id": "sess-1",
		"start_time": "2026-06-17T08:00:00Z",
		"last_active": "2026-06-17T09:00:00Z",
		"work_dir": "/tmp/wd",
		"summary": "did stuff",
		"history": [{"role":"user"},{"role":"model"},{"role":"user"}],
		"total_tokens": 1234,
		"system_instruction": "a long system prompt the listing must not need"
	}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}

	info, err := loadSessionMeta(path)
	if err != nil {
		t.Fatalf("loadSessionMeta: %v", err)
	}
	if info.ID != "sess-1" {
		t.Errorf("ID = %q, want sess-1", info.ID)
	}
	if info.MessageCount != 3 {
		t.Errorf("MessageCount = %d, want 3 (counted without deep-parsing)", info.MessageCount)
	}
	if info.WorkDir != "/tmp/wd" {
		t.Errorf("WorkDir = %q, want /tmp/wd", info.WorkDir)
	}
	if info.Summary != "did stuff" {
		t.Errorf("Summary = %q, want 'did stuff'", info.Summary)
	}
	if info.LastActive.IsZero() {
		t.Error("LastActive was not parsed")
	}

	// A corrupt file surfaces an error (ListSessions skips such files).
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSessionMeta(bad); err == nil {
		t.Error("loadSessionMeta should error on corrupt JSON")
	}
}
