package chat

import (
	"strings"
	"testing"
)

func TestSessionManagerLoadSessionIsExactProjectScopedAndSideEffectFree(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	project := t.TempDir()
	history, err := NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	saved := NewSession()
	saved.SetID("exact-session")
	saved.SetWorkDir(project)
	saved.AddUserMessage("persisted")
	if err := history.SaveFull(saved); err != nil {
		t.Fatal(err)
	}

	active := NewSession()
	active.SetWorkDir(project)
	active.AddUserMessage("active-must-not-change")
	activeID := active.GetID()
	manager, err := NewSessionManager(active, SessionManagerConfig{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	state, info, err := manager.LoadSession("exact-session")
	if err != nil {
		t.Fatal(err)
	}
	if state.ID != "exact-session" || info.ID != "exact-session" || info.MessageCount != 1 {
		t.Fatalf("loaded state=%+v info=%+v", state, info)
	}
	if active.GetID() != activeID || len(active.GetHistory()) != 1 {
		t.Fatal("LoadSession mutated the active session")
	}

	active.SetWorkDir(t.TempDir())
	if _, _, err := manager.LoadSession("exact-session"); err == nil || !strings.Contains(err.Error(), "different work directory") {
		t.Fatalf("cross-project LoadSession error = %v", err)
	}
	if _, _, err := manager.LoadSession("../escape"); err == nil {
		t.Fatal("LoadSession accepted an unsafe ID")
	}
}
