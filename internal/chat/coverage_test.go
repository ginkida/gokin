package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ===========================================================================
// HistoryManager — Save / Load / List / Delete (0% → full)
// ===========================================================================

func TestHistoryManager_Save_Load_List_Delete(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	m, err := NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}

	s := NewSession()
	s.ID = "test-session-1"
	s.AddUserMessage("hello world")

	// Save (legacy format).
	if err := m.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Load.
	loaded, err := m.Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.SessionID != s.ID {
		t.Errorf("loaded SessionID = %q, want %q", loaded.SessionID, s.ID)
	}
	if len(loaded.Entries) != 1 {
		t.Errorf("entries = %d, want 1", len(loaded.Entries))
	}
	if loaded.Entries[0].Content != "hello world" {
		t.Errorf("entry content = %q", loaded.Entries[0].Content)
	}

	// List.
	sessions, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, sid := range sessions {
		if sid == s.ID {
			found = true
		}
	}
	if !found {
		t.Error("List should contain the saved session")
	}

	// Delete.
	if err := m.Delete(s.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Load after delete should fail.
	if _, err := m.Load(s.ID); err == nil {
		t.Error("Load after Delete should fail")
	}
}

func TestHistoryManager_Load_NotFound(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	m, err := NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}

	if _, err := m.Load("nonexistent"); err == nil {
		t.Error("Load of nonexistent should error")
	}
}

func TestHistoryManager_List_Empty(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	m, err := NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}

	sessions, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("empty List = %d, want 0", len(sessions))
	}
}

func TestHistoryManager_Delete_Nonexistent(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	m, err := NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}

	// Deleting nonexistent returns an error (os.Remove).
	if err := m.Delete("nonexistent"); err == nil {
		t.Error("Delete nonexistent should error")
	}
}

// ===========================================================================
// HistoryManager — DeleteSession / SaveFull / LoadFull / ListSessions
// ===========================================================================

func TestHistoryManager_SaveFull_LoadFull_DeleteSession(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	m, err := NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}

	s := NewSession()
	s.ID = "full-session-1"
	s.AddUserMessage("test message")

	// SaveFull.
	if err := m.SaveFull(s); err != nil {
		t.Fatalf("SaveFull: %v", err)
	}

	// LoadFull.
	state, err := m.LoadFull(s.ID)
	if err != nil {
		t.Fatalf("LoadFull: %v", err)
	}
	if state.ID != s.ID {
		t.Errorf("state ID = %q, want %q", state.ID, s.ID)
	}

	// ListSessions.
	sessions, err := m.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("ListSessions = %d, want 1", len(sessions))
	}

	// DeleteSession.
	if err := m.DeleteSession(s.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	// LoadFull after delete should fail.
	if _, err := m.LoadFull(s.ID); err == nil {
		t.Error("LoadFull after delete should fail")
	}
}

func TestHistoryManager_LoadFull_NotFound(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	m, err := NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}

	if _, err := m.LoadFull("nonexistent"); err == nil {
		t.Error("LoadFull of nonexistent should error")
	}
}

func TestHistoryManager_ListSessions_Empty(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	m, err := NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}

	sessions, err := m.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("empty ListSessions = %d, want 0", len(sessions))
	}
}

// ===========================================================================
// getDataDir / getSessionsDir
// ===========================================================================

func TestGetDataDir_XDG(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)

	dir, err := getDataDir()
	if err != nil {
		t.Fatalf("getDataDir: %v", err)
	}
	if dir != filepath.Join(tmp, "gokin", "history") {
		t.Errorf("getDataDir = %q, want %q", dir, filepath.Join(tmp, "gokin", "history"))
	}
}

func TestGetSessionsDir_XDG(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)

	dir, err := getSessionsDir()
	if err != nil {
		t.Fatalf("getSessionsDir: %v", err)
	}
	if dir != filepath.Join(tmp, "gokin", "sessions") {
		t.Errorf("getSessionsDir = %q, want %q", dir, filepath.Join(tmp, "gokin", "sessions"))
	}
}

// ===========================================================================
// SessionManager — DefaultSessionManagerConfig (0% → full)
// ===========================================================================

func TestDefaultSessionManagerConfig(t *testing.T) {
	cfg := DefaultSessionManagerConfig()
	if !cfg.Enabled {
		t.Error("default should be enabled")
	}
	if cfg.SaveInterval != 2*time.Minute {
		t.Errorf("SaveInterval = %v, want 2m", cfg.SaveInterval)
	}
	if !cfg.AutoLoad {
		t.Error("AutoLoad should be true")
	}
	if cfg.MaxSessionAge != 30*24*time.Hour {
		t.Errorf("MaxSessionAge = %v, want 30d", cfg.MaxSessionAge)
	}
	if cfg.MaxSessionCount != 50 {
		t.Errorf("MaxSessionCount = %d, want 50", cfg.MaxSessionCount)
	}
}

// ===========================================================================
// SessionManager — Start / Stop / SaveAfterMessage / GetLastSaveTime
// ===========================================================================

func TestSessionManager_Start_Stop(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := NewSession()
	s.ID = "sm-test-1"
	cfg := DefaultSessionManagerConfig()
	cfg.SaveInterval = 100 * time.Millisecond // short for testing

	sm, err := NewSessionManager(s, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm.Start(ctx)

	// Verify lastSaveTime was set.
	if sm.GetLastSaveTime().IsZero() {
		t.Error("GetLastSaveTime should be non-zero after Start")
	}

	// Stop should not panic.
	sm.Stop()
}

func TestSessionManager_Start_Disabled(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := NewSession()
	cfg := DefaultSessionManagerConfig()
	cfg.Enabled = false

	sm, err := NewSessionManager(s, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start with disabled config should be a no-op.
	sm.Start(ctx)
	sm.Stop()
}

func TestSessionManager_SaveAfterMessage(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := NewSession()
	s.ID = "sm-save-test"
	cfg := DefaultSessionManagerConfig()
	cfg.SaveInterval = 10 * time.Second

	sm, err := NewSessionManager(s, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm.Start(ctx)
	defer sm.Stop()

	// SaveAfterMessage should not error (async signal).
	if err := sm.SaveAfterMessage(); err != nil {
		t.Errorf("SaveAfterMessage: %v", err)
	}

	// The saver is async (buffered signal + 100ms debounce): poll with a
	// bounded deadline instead of a single-shot read after a fixed sleep —
	// the signal-then-single-shot idiom flakes on loaded CI runners
	// (v0.100.105 CI failure; the same fix class as de80023/ffdc62b).
	deadline := time.Now().Add(5 * time.Second)
	var loadErr error
	for time.Now().Before(deadline) {
		if _, loadErr = sm.historyManager.LoadFull(s.ID); loadErr == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if loadErr != nil {
		t.Errorf("session not saved after SaveAfterMessage: %v", loadErr)
	}
}

func TestSessionManager_SaveAfterMessage_Disabled(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := NewSession()
	cfg := DefaultSessionManagerConfig()
	cfg.Enabled = false

	sm, err := NewSessionManager(s, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	// Disabled config → SaveAfterMessage is a no-op.
	if err := sm.SaveAfterMessage(); err != nil {
		t.Errorf("SaveAfterMessage disabled: %v", err)
	}
}

func TestSessionManager_Save(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := NewSession()
	s.ID = "sm-save-direct"
	cfg := DefaultSessionManagerConfig()

	sm, err := NewSessionManager(s, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	if err := sm.Save(); err != nil {
		t.Errorf("Save: %v", err)
	}

	// Verify saved.
	if _, err := sm.historyManager.LoadFull(s.ID); err != nil {
		t.Errorf("session not saved: %v", err)
	}

	// lastSaveTime should be updated.
	if sm.GetLastSaveTime().IsZero() {
		t.Error("GetLastSaveTime should be non-zero after Save")
	}
}

func TestSessionManager_Save_Disabled(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := NewSession()
	cfg := DefaultSessionManagerConfig()
	cfg.Enabled = false

	sm, err := NewSessionManager(s, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	// Disabled → Save is a no-op.
	if err := sm.Save(); err != nil {
		t.Errorf("Save disabled: %v", err)
	}
}

func TestSessionManager_GetLastSaveTime(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := NewSession()
	cfg := DefaultSessionManagerConfig()

	sm, err := NewSessionManager(s, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	// Before any save, lastSaveTime is zero.
	before := sm.GetLastSaveTime()
	if !before.IsZero() {
		t.Error("lastSaveTime should be zero before any save")
	}

	sm.Save()
	after := sm.GetLastSaveTime()
	if after.IsZero() {
		t.Error("lastSaveTime should be non-zero after Save")
	}
}

// ===========================================================================
// SessionManager — ClearCurrentSession (0% → full)
// ===========================================================================

func TestSessionManager_ClearCurrentSession(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := NewSession()
	s.ID = "sm-clear-test"
	cfg := DefaultSessionManagerConfig()

	sm, err := NewSessionManager(s, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	// Save first.
	if err := sm.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify it exists.
	if _, err := sm.historyManager.LoadFull(s.ID); err != nil {
		t.Fatalf("session not saved: %v", err)
	}

	// Clear.
	if err := sm.ClearCurrentSession(); err != nil {
		t.Fatalf("ClearCurrentSession: %v", err)
	}

	// Verify it's gone.
	if _, err := sm.historyManager.LoadFull(s.ID); err == nil {
		t.Error("session should be deleted after ClearCurrentSession")
	}
}

func TestSessionManager_ClearCurrentSession_Disabled(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := NewSession()
	cfg := DefaultSessionManagerConfig()
	cfg.Enabled = false

	sm, err := NewSessionManager(s, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	// Disabled → no-op.
	if err := sm.ClearCurrentSession(); err != nil {
		t.Errorf("ClearCurrentSession disabled: %v", err)
	}
}

func TestSessionManager_ClearCurrentSession_Nonexistent(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := NewSession()
	s.ID = "never-saved"
	cfg := DefaultSessionManagerConfig()

	sm, err := NewSessionManager(s, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	// Clearing a session that was never saved should not error (os.IsNotExist).
	if err := sm.ClearCurrentSession(); err != nil {
		t.Errorf("ClearCurrentSession on nonexistent: %v", err)
	}
}

// ===========================================================================
// SessionManager — CleanupOldSessions (0% → full)
// ===========================================================================

func TestSessionManager_CleanupOldSessions_Empty(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := NewSession()
	cfg := DefaultSessionManagerConfig()

	sm, err := NewSessionManager(s, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	if err := sm.CleanupOldSessions(); err != nil {
		t.Errorf("CleanupOldSessions on empty: %v", err)
	}
}

func TestSessionManager_CleanupOldSessions_RemovesOld(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := NewSession()
	cfg := DefaultSessionManagerConfig()
	cfg.MaxSessionAge = 1 * time.Hour // short for testing

	sm, err := NewSessionManager(s, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	// Create an old session manually.
	oldSession := NewSession()
	oldSession.ID = "very-old-session"
	if err := sm.historyManager.SaveFull(oldSession); err != nil {
		t.Fatalf("SaveFull old: %v", err)
	}

	// Backdate the persisted activity timestamp used by cleanup. File mtime is
	// intentionally irrelevant: copying or restoring a session must not make an
	// old conversation look recent.
	sessionsDir, _ := getSessionsDir()
	oldPath := filepath.Join(sessionsDir, oldSession.ID+".json")
	oldTime := time.Now().Add(-2 * time.Hour)
	data, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatalf("ReadFile old session: %v", err)
	}
	var oldState SessionState
	if err := json.Unmarshal(data, &oldState); err != nil {
		t.Fatalf("Unmarshal old session: %v", err)
	}
	oldState.LastActive = oldTime
	data, err = json.MarshalIndent(oldState, "", "  ")
	if err != nil {
		t.Fatalf("Marshal old session: %v", err)
	}
	if err := os.WriteFile(oldPath, data, 0o600); err != nil {
		t.Fatalf("WriteFile old session: %v", err)
	}

	// Create a recent session.
	recentSession := NewSession()
	recentSession.ID = "recent-session"
	if err := sm.historyManager.SaveFull(recentSession); err != nil {
		t.Fatalf("SaveFull recent: %v", err)
	}

	// Cleanup should remove the old one but keep the recent one.
	if err := sm.CleanupOldSessions(); err != nil {
		t.Fatalf("CleanupOldSessions: %v", err)
	}

	sessions, _ := sm.historyManager.ListSessions()
	foundRecent := false
	foundOld := false
	for _, si := range sessions {
		if si.ID == "recent-session" {
			foundRecent = true
		}
		if si.ID == "very-old-session" {
			foundOld = true
		}
	}
	if !foundRecent {
		t.Error("recent session should survive cleanup")
	}
	if foundOld {
		t.Error("old session should be removed by cleanup")
	}
}

// ===========================================================================
// SessionManager — NewSessionManager with bad SaveInterval
// ===========================================================================

func TestNewSessionManager_ClampsZeroInterval(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := NewSession()
	cfg := DefaultSessionManagerConfig()
	cfg.SaveInterval = 0 // zero should be clamped to 2m

	sm, err := NewSessionManager(s, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	if sm.config.SaveInterval != 2*time.Minute {
		t.Errorf("zero SaveInterval should be clamped to 2m, got %v", sm.config.SaveInterval)
	}
}

func TestNewSessionManager_ClampsNegativeInterval(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := NewSession()
	cfg := DefaultSessionManagerConfig()
	cfg.SaveInterval = -5 * time.Second

	sm, err := NewSessionManager(s, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	if sm.config.SaveInterval != 2*time.Minute {
		t.Errorf("negative SaveInterval should be clamped to 2m, got %v", sm.config.SaveInterval)
	}
}

// ===========================================================================
// Session — SetProvider / GetProvider (0% → full)
// ===========================================================================

func TestSession_SetProvider_GetProvider(t *testing.T) {
	s := NewSession()

	if s.GetProvider() != "" {
		t.Error("new session provider should be empty")
	}

	s.SetProvider("glm")
	if got := s.GetProvider(); got != "glm" {
		t.Errorf("GetProvider = %q, want 'glm'", got)
	}

	s.SetProvider("deepseek")
	if got := s.GetProvider(); got != "deepseek" {
		t.Errorf("GetProvider = %q, want 'deepseek'", got)
	}
}

// ===========================================================================
// Session — redactMapValues / redactSliceValues (0% → full)
// ===========================================================================

func TestRedactMapValues_Nil(t *testing.T) {
	// Should not panic on nil.
	redactMapValues(nil)
}

func TestRedactMapValues_Strings(t *testing.T) {
	m := map[string]any{
		"key":  "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"safe": "normal text",
	}
	redactMapValues(m)

	if m["key"].(string) == "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Error("GitHub token should be redacted")
	}
	if m["safe"].(string) != "normal text" {
		t.Error("normal text should be unchanged")
	}
}

func TestRedactMapValues_NestedMap(t *testing.T) {
	nested := map[string]any{
		"inner": "ghp_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
	}
	m := map[string]any{
		"outer": nested,
	}
	redactMapValues(m)

	if nested["inner"].(string) == "ghp_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB" {
		t.Error("nested GitHub token should be redacted")
	}
}

func TestRedactMapValues_NestedSlice(t *testing.T) {
	m := map[string]any{
		"items": []any{
			"ghp_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC",
			"safe text",
		},
	}
	redactMapValues(m)

	items := m["items"].([]any)
	if items[0].(string) == "ghp_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC" {
		t.Error("slice item GitHub token should be redacted")
	}
	if items[1].(string) != "safe text" {
		t.Error("safe slice item should be unchanged")
	}
}

func TestRedactSliceValues_Strings(t *testing.T) {
	s := []any{
		"ghp_DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD",
		"normal",
	}
	redactSliceValues(s)

	if s[0].(string) == "ghp_DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD" {
		t.Error("GitHub token in slice should be redacted")
	}
	if s[1].(string) != "normal" {
		t.Error("normal text in slice should be unchanged")
	}
}

func TestRedactSliceValues_NestedMap(t *testing.T) {
	s := []any{
		map[string]any{
			"token": "ghp_EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE",
		},
	}
	redactSliceValues(s)

	m := s[0].(map[string]any)
	if m["token"].(string) == "ghp_EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE" {
		t.Error("nested map GitHub token should be redacted")
	}
}

func TestRedactSliceValues_NestedSlice(t *testing.T) {
	s := []any{
		[]any{
			"ghp_FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF",
		},
	}
	redactSliceValues(s)

	inner := s[0].([]any)
	if inner[0].(string) == "ghp_FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF" {
		t.Error("doubly-nested GitHub token should be redacted")
	}
}

// ===========================================================================
// Session — SetOnSaveFailed callback (0% → full)
// ===========================================================================

func TestSessionManager_SetOnSaveFailed(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := NewSession()
	s.ID = "callback-test"
	cfg := DefaultSessionManagerConfig()

	sm, err := NewSessionManager(s, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	called := false
	sm.SetOnSaveFailed(func(err error) {
		called = true
	})

	if sm.onSaveFailed == nil {
		t.Error("onSaveFailed should be set")
	}
	_ = called // callback fires on failure streak, not on success
}
