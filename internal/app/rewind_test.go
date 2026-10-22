package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/hooks"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"gokin/internal/undo"
)

func newRewindApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := newSessionLeaseTestApp(t, "turn-rewind", "glm", work, "")
	a.undoManager = undo.NewManager()
	a.registry = tools.NewRegistry()
	a.hooksManager = hooks.NewManager(true, work)
	return a
}
func rewindWrite(t *testing.T, a *App, path, text string) {
	t.Helper()
	tool := tools.NewWriteTool(a.workDir)
	tool.SetUndoManager(a.undoManager)
	result, err := tool.Execute(t.Context(), map[string]any{"file_path": path, "content": text})
	if err != nil || !result.Success {
		t.Fatalf("write failed: %+v %v", result, err)
	}
}
func TestTurnRewindPersistsCodeAndConversationWithRetryBoundary(t *testing.T) {
	for _, mode := range []string{"code", "conversation", "both"} {
		t.Run(mode, func(t *testing.T) {
			a := newRewindApp(t)
			path := filepath.Join(a.workDir, "file")
			a.captureTurnRewind("first")
			a.session.AddUserMessage("first")
			rewindWrite(t, a, path, "one")
			a.captureTurnRewind("second")
			a.session.AddUserMessage("second")
			rewindWrite(t, a, path, "two")
			points, err := a.ListRewindPoints(t.Context())
			if err != nil || len(points) != 2 {
				t.Fatalf("points=%+v %v", points, err)
			}
			a.session.SetToolCheckpoints([]chat.SerializedToolCheckpoint{{CallID: "old-call", ToolName: "write"}})
			a.session.AddPendingRecovery(chat.SerializedPendingRecovery{ID: "old-retry", SessionID: a.session.GetID(), State: chat.PendingRecoveryScheduled}, "")
			oldEpoch := a.recoveryEpoch.Load()
			// Discard the runtime journal and undo stack, as happens after restart.
			a.rewindMu.Lock()
			a.rewindStore = nil
			a.rewindSessionID = ""
			a.rewindMu.Unlock()
			a.undoManager.Clear()
			if _, err := a.RewindToPoint(t.Context(), points[1].ID, mode); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			want := "one"
			messages := 1
			if mode == "conversation" {
				want = "two"
			}
			if mode == "code" {
				messages = 2
			}
			if err != nil || string(data) != want || len(a.session.GetHistory()) != messages {
				t.Fatalf("mode=%s code=%q err=%v history=%d", mode, data, err, len(a.session.GetHistory()))
			}
			if a.recoveryEpoch.Load() <= oldEpoch || len(a.session.GetToolCheckpoints()) > 0 || len(a.session.GetPendingRecoveries()) > 0 {
				t.Fatal("rewind retained executable retry generation")
			}
			hm, err := chat.NewHistoryManager()
			if err != nil {
				t.Fatal(err)
			}
			state, err := hm.LoadFull(a.session.GetID())
			if err != nil {
				t.Fatal(err)
			}
			if len(state.History) != messages || len(state.PendingRecoveries) > 0 || len(state.ToolCheckpoints) > 0 {
				t.Fatalf("rewind not saved: %+v", state)
			}
		})
	}
}
func TestTurnRewindConflictLeavesSessionAndOtherFiles(t *testing.T) {
	a := newRewindApp(t)
	path := filepath.Join(a.workDir, "file")
	a.captureTurnRewind("first")
	a.session.AddUserMessage("first")
	rewindWrite(t, a, path, "agent")
	if err := os.WriteFile(path, []byte("manual"), 0600); err != nil {
		t.Fatal(err)
	}
	points, _ := a.ListRewindPoints(t.Context())
	before := a.session.GetState()
	if _, err := a.RewindToPoint(t.Context(), points[0].ID, "both"); err == nil {
		t.Fatal("manual change overwritten")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "manual" || len(a.session.GetHistory()) != len(before.History) {
		t.Fatal("failed rewind changed state")
	}
}
func TestTurnRewindDisabledPersistenceWritesNothing(t *testing.T) {
	a := newRewindApp(t)
	config := chat.DefaultSessionManagerConfig()
	config.Enabled = false
	manager, err := chat.NewSessionManager(a.session, config)
	if err != nil {
		t.Fatal(err)
	}
	a.sessionManager = manager
	a.captureTurnRewind("private prompt")
	root, err := chat.RewindStorageDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("disabled session persistence wrote checkpoints")
	}
	if _, err := a.ListRewindPoints(t.Context()); err == nil || !strings.Contains(err.Error(), "persistence") {
		t.Fatal("disabled checkpoints available", err)
	}
}

func TestTurnRewindSessionSaveFailureRollsBackCodeAndConversation(t *testing.T) {
	a := newRewindApp(t)
	path := filepath.Join(a.workDir, "file")
	a.captureTurnRewind("change")
	a.session.AddUserMessage("change")
	rewindWrite(t, a, path, "current")
	if err := a.sessionManager.Save(); err != nil {
		t.Fatal(err)
	}
	points, _ := a.ListRewindPoints(t.Context())
	before := a.session.GetState()
	sessionFile := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gokin", "sessions", a.session.GetID()+".json")
	if err := os.Remove(sessionFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sessionFile, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RewindToPoint(t.Context(), points[0].ID, "both"); err == nil {
		t.Fatal("failed mandatory save was ignored")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "current" || len(a.session.GetHistory()) != len(before.History) {
		t.Fatal("failed session publication left a partial rewind")
	}
	if len(a.rewindStore.Points()[0].Events) != 1 {
		t.Fatal("failed rewind changed journal")
	}
}

func TestTurnRewindRestoresSkillHookAuthorityAndOnceConsumption(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeResumeHookSkill(t, root)
	a, write, _ := newResumeHookApp(t, root, testkit.NewMockClient(), "claude-global", false)
	t.Cleanup(func() { _ = a.ReleaseSessionWriterLease() })
	tools.BindSkillInvocationLedger(a.registry, a.session.InvocationLedger())
	a.captureTurnRewind("before guard")
	a.session.AddUserMessage("before guard")
	result, err := a.executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "guard"})
	if err != nil || !result.Success {
		t.Fatalf("load=%+v %v", result, err)
	}
	a.captureTurnRewind("guard loaded")
	a.session.AddUserMessage("guard loaded")
	result, err = a.executor.InvokeTool(t.Context(), "read", map[string]any{"file_path": "notes.txt"})
	if err != nil || !result.Success {
		t.Fatalf("read=%+v %v", result, err)
	}
	refs, _ := a.session.InvocationLedger().HookActivations()
	if len(refs) != 1 || !refs[0].Consumed[0] {
		t.Fatal("once fixture not consumed")
	}
	points, err := a.ListRewindPoints(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.RewindToPoint(t.Context(), points[1].ID, "conversation"); err != nil {
		t.Fatal(err)
	}
	refs, _ = a.session.InvocationLedger().HookActivations()
	if len(refs) != 1 || refs[0].Consumed[0] {
		t.Fatal("rewind did not rearm snapshot's once hook")
	}
	result, err = a.executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": "denied.txt"})
	if err != nil || result.Success || write.CallCount() != 0 {
		t.Fatalf("restored guard not enforced: %+v %v", result, err)
	}
	if _, err := a.RewindToPoint(t.Context(), points[0].ID, "conversation"); err != nil {
		t.Fatal(err)
	}
	result, err = a.executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": "allowed.txt"})
	if err != nil || !result.Success || write.CallCount() != 1 || len(a.GetSkillHooks()) != 0 {
		t.Fatalf("rewind retained later authority: %+v %v", result, err)
	}
}
