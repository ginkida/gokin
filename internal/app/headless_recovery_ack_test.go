package app

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/testkit"
)

func TestHeadlessResumeAcknowledgesInterruptedRecovery(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, state := range []string{chat.PendingRecoveryScheduled, chat.PendingRecoveryClaimed} {
		t.Run(state, func(t *testing.T) {
			mock := testkit.NewMockClient().EnqueueText("I will inspect the interrupted work before continuing.")
			application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
			application.session.SetWorkDir(application.workDir)
			application.session.AddUserMessage("original request")
			recovery := validSerializedRecovery("resume-ack", application.session.GetID(), state)
			application.session.AddPendingRecovery(recovery, "")
			manager, err := chat.NewSessionManager(application.session, chat.DefaultSessionManagerConfig())
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.Save(); err != nil {
				t.Fatal(err)
			}
			store, err := chat.NewHistoryManager()
			if err != nil {
				t.Fatal(err)
			}
			saved, err := store.LoadFull(application.session.GetID())
			if err != nil {
				t.Fatal(err)
			}
			workDir := application.workDir
			application, _ = newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
			application.workDir = workDir
			if err := application.session.RestoreFromState(saved); err != nil {
				t.Fatal(err)
			}
			application.sessionManager, err = chat.NewSessionManager(application.session, chat.DefaultSessionManagerConfig())
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			result, err := application.RunHeadlessWithOptions(context.Background(), "continue after the timeout", HeadlessOptions{Stdout: &output, Stderr: io.Discard, NoSelfReview: true})
			if err != nil || result.Status != "success" {
				t.Fatalf("resume status=%s err=%v output=%s", result.Status, err, output.String())
			}
			historyManager, loadErr := chat.NewHistoryManager()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			persisted, loadErr := historyManager.LoadFull(application.session.GetID())
			if loadErr != nil || len(persisted.PendingRecoveries) != 0 {
				t.Fatalf("acknowledgement did not survive restart: %v", loadErr)
			}
			if len(application.session.GetPendingRecoveries()) != 0 {
				t.Fatal("interrupted recovery was not acknowledged")
			}
			if len(mock.Calls()) != 1 {
				t.Fatalf("resume made %d requests", len(mock.Calls()))
			}
			var retained strings.Builder
			for _, msg := range application.session.GetHistory() {
				for _, part := range msg.Parts {
					retained.WriteString(part.Text)
				}
			}
			if !strings.Contains(retained.String(), "interrupted") || !strings.Contains(retained.String(), "write") {
				t.Fatalf("missing interrupted-tool evidence: %s", retained.String())
			}
		})
	}
}

func TestHeadlessRecoveryAcknowledgementRollsBackOnSaveFailure(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("invalid/session/id") // Saving must fail before provider work.
	session.AddUserMessage("original request")
	session.AddPendingRecovery(validSerializedRecovery("claimed", session.GetID(), chat.PendingRecoveryClaimed), "")
	application := newPendingRecoveryTestApp(t, session)
	if _, _, err := application.prepareHeadlessRecovery("new request"); err == nil {
		t.Fatal("acknowledgement ignored persistence failure")
	}
	if len(session.GetPendingRecoveries()) != 1 || len(session.GetHistory()) != 1 || application.recoveryEpoch.Load() != 0 {
		t.Fatal("failed acknowledgement consumed recovery or retained an unsaved note")
	}
}

func TestHeadlessAcknowledgementInvalidatesQueuedAndLateRecovery(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetWorkDir(t.TempDir())
	recovery := validSerializedRecovery("claimed", session.GetID(), chat.PendingRecoveryClaimed)
	session.AddPendingRecovery(recovery, "")
	application := newPendingRecoveryTestApp(t, session)
	oldEpoch := application.recoveryEpoch.Load()
	application.enqueueRecoveryPending(recovery.Message, "original request", recovery.ID, session.GetID(), oldEpoch, nil)
	application.enqueuePending("explicit next user request")
	_, lineage, err := application.prepareHeadlessRecovery("continue after timeout")
	if err != nil {
		t.Fatal(err)
	}
	if lineage.epoch == oldEpoch || application.pendingCount() != 1 {
		t.Fatalf("old executable recovery survived acknowledgement: old=%d new=%d queued=%d", oldEpoch, lineage.epoch, application.pendingCount())
	}
	if outcome := application.handleRecoveryResubmit(recovery.Message, "original request", recovery.ID, session.GetID(), oldEpoch, nil); outcome != recoveryDispatchIgnored {
		t.Fatalf("late recovery dispatched: %v", outcome)
	}
}
