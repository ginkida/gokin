package chat

import (
	"testing"

	"google.golang.org/genai"
)

func TestSessionManagerLoadLatestIgnoresOnlyAutoLoadPreference(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	workDir := t.TempDir()

	persisted := NewSession()
	persisted.SetID("explicit-continue")
	persisted.SetWorkDir(workDir)
	persisted.SetHistory([]*genai.Content{genai.NewContentFromText("continue me", genai.RoleUser)})
	history, err := NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}
	if err := history.SaveFull(persisted); err != nil {
		t.Fatalf("SaveFull: %v", err)
	}

	active := NewSession()
	active.SetWorkDir(workDir)
	manager, err := NewSessionManager(active, SessionManagerConfig{
		Enabled:  true,
		AutoLoad: false,
	})
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	state, info, err := manager.LoadLast()
	if err != nil || state != nil || info != nil {
		t.Fatalf("ambient LoadLast with auto_load=false = state %v info %v err %v", state, info, err)
	}
	state, info, err = manager.LoadLatest()
	if err != nil {
		t.Fatalf("explicit LoadLatest: %v", err)
	}
	if state == nil || info == nil || state.ID != "explicit-continue" || info.ID != "explicit-continue" {
		t.Fatalf("explicit continue selected state=%+v info=%+v", state, info)
	}
}
