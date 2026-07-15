package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSessionManagerLoadLastRejectsMetadataRedirect pins the on-disk identity
// boundary used by automatic resume. A session file must describe the session
// named by its filename; otherwise a shallow metadata record for the current
// project could redirect LoadLast to a different project's real session file.
func TestSessionManagerLoadLastRejectsMetadataRedirect(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	projectA := t.TempDir()
	projectB := t.TempDir()

	history, err := NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}

	validA := NewSession()
	validA.SetID("project-a-valid")
	validA.SetWorkDir(projectA)
	validA.AddUserMessage("project A history")
	if err := history.SaveFull(validA); err != nil {
		t.Fatalf("SaveFull(project A): %v", err)
	}

	foreignB := NewSession()
	foreignB.SetID("project-b-foreign")
	foreignB.SetWorkDir(projectB)
	foreignB.AddUserMessage("project B secret history")
	if err := history.SaveFull(foreignB); err != nil {
		t.Fatalf("SaveFull(project B): %v", err)
	}

	// The file advertises project A and is newest, but its embedded ID points
	// at the real project-B file. Before the fix ListSessions trusted this ID,
	// so LoadLast opened project-b-foreign.json and returned its history.
	redirect := &SessionState{
		ID:         foreignB.GetID(),
		StartTime:  time.Now(),
		LastActive: time.Now().Add(time.Hour),
		WorkDir:    projectA,
	}
	data, err := json.Marshal(redirect)
	if err != nil {
		t.Fatalf("Marshal redirect: %v", err)
	}
	sessionsDir, err := getSessionsDir()
	if err != nil {
		t.Fatalf("getSessionsDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "redirect.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile redirect: %v", err)
	}
	if _, err := history.LoadFull("redirect"); err == nil {
		t.Fatal("LoadFull accepted a state whose embedded ID differs from its filename")
	}

	current := NewSession()
	current.SetWorkDir(projectA)
	manager, err := NewSessionManager(current, SessionManagerConfig{Enabled: true, AutoLoad: true})
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	state, info, err := manager.LoadLast()
	if err != nil {
		t.Fatalf("LoadLast: %v", err)
	}
	if state == nil || info == nil {
		t.Fatal("LoadLast found no valid project-A session")
	}
	if state.ID != validA.GetID() || info.ID != validA.GetID() {
		t.Fatalf("LoadLast returned state=%q info=%q, want valid project-A session %q",
			state.ID, info.ID, validA.GetID())
	}
	if filepath.Clean(state.WorkDir) != filepath.Clean(projectA) {
		t.Fatalf("LoadLast crossed projects: state work_dir=%q, want %q", state.WorkDir, projectA)
	}
}

// TestSessionManagerLoadLastFallsBackFromShallowOnlyCorruptNewest ensures one
// damaged snapshot cannot make every older valid snapshot for the project
// unresumable. ListSessions intentionally reads history as RawMessage, so a
// candidate still has to survive full decoding before it becomes authoritative.
func TestSessionManagerLoadLastFallsBackFromShallowOnlyCorruptNewest(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	projectDir := t.TempDir()

	history, err := NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}
	valid := NewSession()
	valid.SetID("valid-older")
	valid.SetWorkDir(projectDir)
	valid.AddUserMessage("last intact snapshot")
	if err := history.SaveFull(valid); err != nil {
		t.Fatalf("SaveFull(valid): %v", err)
	}

	// This shape is valid for the shallow metadata decoder because history is
	// []json.RawMessage. It is invalid for SessionState: a number cannot decode
	// into SerializedContent. Its future LastActive makes it the first choice.
	corrupt := map[string]any{
		"id":          "corrupt-newest",
		"start_time":  time.Now(),
		"last_active": time.Now().Add(time.Hour),
		"work_dir":    projectDir,
		"history":     []any{42},
	}
	data, err := json.Marshal(corrupt)
	if err != nil {
		t.Fatalf("Marshal corrupt candidate: %v", err)
	}
	sessionsDir, err := getSessionsDir()
	if err != nil {
		t.Fatalf("getSessionsDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "corrupt-newest.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile corrupt candidate: %v", err)
	}

	current := NewSession()
	current.SetWorkDir(projectDir)
	manager, err := NewSessionManager(current, SessionManagerConfig{Enabled: true, AutoLoad: true})
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	state, info, err := manager.LoadLast()
	if err != nil {
		t.Fatalf("LoadLast should fall back to the intact snapshot: %v", err)
	}
	if state == nil || info == nil {
		t.Fatal("LoadLast found no intact fallback snapshot")
	}
	if state.ID != valid.GetID() || info.ID != valid.GetID() {
		t.Fatalf("LoadLast returned state=%q info=%q, want intact snapshot %q",
			state.ID, info.ID, valid.GetID())
	}
}
