package chat

import (
	"os"
	"path/filepath"
	"testing"
)

func rotationFixture(t *testing.T) (*Session, *SessionManager, *HistoryManager, *SessionState) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := NewSession()
	session.SetID("before-clear")
	session.SetWorkDir(t.TempDir())
	session.AddUserMessage("preserve source transcript")
	session.AddPendingRecovery(SerializedPendingRecovery{ID: "cancelled-retry", SessionID: session.GetID(), State: PendingRecoveryScheduled}, "")
	session.SetToolCheckpoints([]SerializedToolCheckpoint{{CallID: "old-call", ToolName: "edit"}})
	manager, err := NewSessionManager(session, DefaultSessionManagerConfig())
	if err != nil {
		t.Fatal(err)
	}
	history, err := NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Save(); err != nil {
		t.Fatal(err)
	}
	fresh := NewSession().GetState()
	fresh.WorkDir = session.GetWorkDir()
	return session, manager, history, fresh
}
func TestRotateConversationPreservesSourceAndSelectsFreshRoot(t *testing.T) {
	session, manager, history, fresh := rotationFixture(t)
	committed, err := manager.RotateConversation(fresh)
	if err != nil || !committed {
		t.Fatal("rotation failed", committed, err)
	}
	if session.GetID() != fresh.ID || len(session.GetHistory()) != 0 {
		t.Fatal("fresh root not selected")
	}
	source, err := history.LoadFull("before-clear")
	if err != nil {
		t.Fatal(err)
	}
	if len(source.History) != 1 || source.History[0].Parts[0].Text != "preserve source transcript" {
		t.Fatal("clear destroyed the old dialogue")
	}
	if len(source.PendingRecoveries) > 0 || len(source.ToolCheckpoints) > 0 {
		t.Fatal("clear preserved executable old recovery")
	}
	latest, _, err := manager.LoadLatest()
	if err != nil || latest == nil || latest.ID != fresh.ID || len(latest.History) > 0 {
		t.Fatal("auto-resume would resurrect old conversation", latest, err)
	}
}
func TestRotateConversationSourceFailureKeepsLiveAndCleansFresh(t *testing.T) {
	session, manager, history, fresh := rotationFixture(t)
	dir, _ := getSessionsDir()
	sourcePath := filepath.Join(dir, session.GetID()+".json")
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sourcePath, 0700); err != nil {
		t.Fatal(err)
	}
	committed, err := manager.RotateConversation(fresh)
	if err == nil || committed {
		t.Fatal("unwritable source accepted", committed, err)
	}
	if session.GetID() != "before-clear" || len(session.GetHistory()) != 1 || len(session.GetPendingRecoveries()) != 1 {
		t.Fatal("failed rotation changed live source")
	}
	if _, err := history.LoadFull(fresh.ID); !os.IsNotExist(err) {
		t.Fatal("failed rotation left a fresh root", err)
	}
}
func TestRotateConversationNeverOverwritesExistingTarget(t *testing.T) {
	session, manager, history, fresh := rotationFixture(t)
	target := NewSession()
	target.SetID(fresh.ID)
	target.AddUserMessage("preexisting other session")
	if err := history.SaveFull(target); err != nil {
		t.Fatal(err)
	}
	if committed, err := manager.RotateConversation(fresh); err == nil || committed {
		t.Fatal("existing target overwritten")
	}
	state, err := history.LoadFull(fresh.ID)
	if err != nil || state.History[0].Parts[0].Text != "preexisting other session" {
		t.Fatal("target content lost", err)
	}
	if session.GetID() != "before-clear" {
		t.Fatal("failed collision changed owner")
	}
}
