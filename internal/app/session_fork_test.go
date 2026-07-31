package app

import (
	"testing"

	"gokin/internal/chat"
)

func TestForkLoadedSessionPersistsIndependentSafeCopy(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	workDir := t.TempDir()

	source := chat.NewSession()
	source.SetID("source-session")
	source.SetWorkDir(workDir)
	source.SetProvider("mock")
	source.AddUserMessage("preserve this conversation")
	source.SetToolCheckpoints([]chat.SerializedToolCheckpoint{{CallID: "write-1", ToolName: "write"}})
	source.AddPendingRecovery(chat.SerializedPendingRecovery{
		ID:        "retry-1",
		SessionID: "source-session",
		Message:   "retry interrupted edit",
		State:     chat.PendingRecoveryScheduled,
	}, "")

	manager, err := chat.NewSessionManager(source, chat.DefaultSessionManagerConfig())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	if err := manager.Save(); err != nil {
		t.Fatalf("save source: %v", err)
	}
	application := &App{session: source, sessionManager: manager}

	const forkID = "67c220a6-5ba6-4d36-95bd-2df9a9f49d94"
	if err := application.ForkLoadedSession(forkID); err != nil {
		t.Fatalf("ForkLoadedSession: %v", err)
	}
	if source.GetID() != forkID {
		t.Fatalf("live session ID = %q", source.GetID())
	}

	history, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	persistedSource, err := history.LoadFull("source-session")
	if err != nil {
		t.Fatalf("load source: %v", err)
	}
	if len(persistedSource.History) != 1 || len(persistedSource.PendingRecoveries) != 1 ||
		len(persistedSource.ToolCheckpoints) != 1 {
		t.Fatalf("source snapshot was changed: history=%d recoveries=%d tools=%d",
			len(persistedSource.History), len(persistedSource.PendingRecoveries), len(persistedSource.ToolCheckpoints))
	}
	persistedFork, err := history.LoadFull(forkID)
	if err != nil {
		t.Fatalf("load fork: %v", err)
	}
	if len(persistedFork.History) != 1 {
		t.Fatalf("fork history = %d, want 1", len(persistedFork.History))
	}
	if len(persistedFork.PendingRecoveries) != 0 || len(persistedFork.ToolCheckpoints) != 0 {
		t.Fatalf("fork inherited executable recovery state: recoveries=%d tools=%d",
			len(persistedFork.PendingRecoveries), len(persistedFork.ToolCheckpoints))
	}
}
