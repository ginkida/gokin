package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/commands"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestClearRewindRotatesAndResumesOriginalCheckpoints(t *testing.T) {
	a := newRewindApp(t)
	a.client = testkit.NewMockClient()
	path := filepath.Join(a.workDir, "file")
	a.captureTurnRewind("change file")
	a.session.AddUserMessage("change file")
	rewindWrite(t, a, path, "preserved code")
	sourceID := a.session.GetID()
	points, err := a.ListRewindPoints(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	a.session.AddPendingRecovery(chat.SerializedPendingRecovery{ID: "old-retry", SessionID: sourceID, State: chat.PendingRecoveryScheduled}, "")
	a.session.SetToolCheckpoints([]chat.SerializedToolCheckpoint{{CallID: "old-write", ToolName: "write"}})
	a.enqueuePending("next ordinary request")
	if _, err := (&commands.ClearCommand{}).Execute(t.Context(), nil, a); err != nil {
		t.Fatal(err)
	}
	freshID := a.session.GetID()
	if freshID == sourceID || a.PreviousClearSessionID() != sourceID || len(a.session.GetHistory()) != 0 {
		t.Fatal("clear did not rotate session")
	}
	if a.sessionLease == nil || a.sessionLease.SessionID() != freshID {
		t.Fatal("fresh identity has no writer lease")
	}
	currentPoints, err := a.ListRewindPoints(t.Context())
	if err != nil || len(currentPoints) != 0 {
		t.Fatal("old checkpoints leaked into fresh root", err)
	}
	listing, err := (&commands.RewindCommand{}).Execute(t.Context(), nil, a)
	if err != nil || !strings.Contains(listing, "/resume "+sourceID+" (previous session)") {
		t.Fatal("missing previous-session entry", listing, err)
	}
	if _, err := (&commands.RewindCommand{}).Execute(t.Context(), []string{"previous"}, a); err != nil {
		t.Fatal(err)
	}
	if a.session.GetID() != sourceID || len(a.session.GetHistory()) != 1 || a.PreviousClearSessionID() != "" {
		t.Fatal("previous dialogue not restored or entry not consumed")
	}
	if len(a.session.GetPendingRecoveries()) != 0 || len(a.session.GetToolCheckpoints()) != 0 {
		t.Fatal("return rearmed cancelled mutations")
	}
	restoredPoints, err := a.ListRewindPoints(t.Context())
	if err != nil || len(restoredPoints) != 1 || restoredPoints[0].ID != points[0].ID {
		t.Fatal("original file checkpoints lost", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "preserved code" {
		t.Fatal("clear or resume changed code")
	}
	if _, err := a.RewindToPoint(t.Context(), points[0].ID, "code"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("restored source code checkpoint did not work")
	}
	message, _, ok := a.dequeuePending()
	if !ok || message != "next ordinary request" {
		t.Fatal("ordinary type-ahead lost")
	}
}
func TestClearRewindBusySourceAndUnrelatedResumeInvalidateOnlyOnSuccess(t *testing.T) {
	a := newRewindApp(t)
	a.client = testkit.NewMockClient()
	a.session.AddUserMessage("source")
	sourceID := a.session.GetID()
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	freshID := a.session.GetID()
	if lease, err := chat.AcquireSessionWriterLease(sourceID); !errors.Is(err, chat.ErrSessionWriterLeaseBusy) {
		if lease != nil {
			_ = lease.Release()
		}
		t.Fatal("previous entry was not protected", err)
	}
	other := saveSessionLeaseFixture(t, "unrelated-session", "glm", a.workDir, "unrelated history")
	lease, err := chat.AcquireSessionWriterLease(other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SwitchSession(t.Context(), other, false); !errors.Is(err, chat.ErrSessionWriterLeaseBusy) {
		t.Fatal("busy target resumed", err)
	}
	if a.session.GetID() != freshID || a.PreviousClearSessionID() != sourceID {
		t.Fatal("failed resume consumed previous entry")
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SwitchSession(t.Context(), other, false); err != nil {
		t.Fatal(err)
	}
	if a.PreviousClearSessionID() != "" {
		t.Fatal("previous entry survived explicit session selection")
	}
	if _, err := a.ResumePreviousClearSession(t.Context(), sourceID, false); err == nil {
		t.Fatal("stale entry resumed after switching elsewhere")
	}
	released, err := chat.AcquireSessionWriterLease(sourceID)
	if err != nil {
		t.Fatal("unrelated resume retained old previous lease", err)
	}
	_ = released.Release()
}
func TestClearRewindEphemeralKeepsPreviousOnlyInMemory(t *testing.T) {
	a := newRewindApp(t)
	a.client = testkit.NewMockClient()
	_ = a.ReleaseSessionWriterLease()
	a.sessionManager = nil
	a.session.AddUserMessage("ephemeral conversation")
	sourceID := a.session.GetID()
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	if a.session.GetID() == sourceID || a.PreviousClearSessionID() != sourceID {
		t.Fatal("ephemeral clear did not rotate")
	}
	listing, err := (&commands.RewindCommand{}).Execute(t.Context(), nil, a)
	if err != nil || !strings.Contains(listing, sourceID) {
		t.Fatal("ephemeral previous entry missing", listing, err)
	}
	if _, err := (&commands.ResumeCommand{}).Execute(t.Context(), []string{sourceID}, a); err != nil {
		t.Fatal(err)
	}
	if a.session.GetID() != sourceID || len(a.session.GetHistory()) != 1 {
		t.Fatal("ephemeral previous context lost")
	}
	root := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gokin", "sessions")
	entries, err := os.ReadDir(root)
	if err == nil {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".json") {
				t.Fatal("ephemeral clear wrote session content")
			}
		}
	}
}
func TestClearRewindCancellationAndSaveFailureKeepPreviousConversation(t *testing.T) {
	for _, mode := range []string{"cancel", "save"} {
		t.Run(mode, func(t *testing.T) {
			a := newRewindApp(t)
			a.client = testkit.NewMockClient()
			a.session.AddUserMessage("current")
			beforeID := a.session.GetID()
			ctx := t.Context()
			if mode == "cancel" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			} else {
				path := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gokin", "sessions", beforeID+".json")
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.ClearConversationForRewind(ctx); err == nil {
				t.Fatal("failed clear reported success")
			}
			if a.session.GetID() != beforeID || len(a.session.GetHistory()) != 1 || a.PreviousClearSessionID() != "" {
				t.Fatal("failed clear changed current conversation")
			}
		})
	}
}

func TestClearRewindRepeatedClearReplacesEntryAndRejectsStaleMenu(t *testing.T) {
	a := newRewindApp(t)
	a.client = testkit.NewMockClient()
	a.session.AddUserMessage("first")
	first := a.session.GetID()
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Clearing an unused fresh root must retain the last meaningful dialogue.
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	if a.PreviousClearSessionID() != first {
		t.Fatal("empty clear discarded the recoverable dialogue")
	}
	a.session.AddUserMessage("second")
	second := a.session.GetID()
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	if a.PreviousClearSessionID() != second {
		t.Fatal("latest clear did not replace previous entry")
	}
	current := a.session.GetID()
	if _, err := a.ResumePreviousClearSession(t.Context(), first, false); err == nil {
		t.Fatal("stale menu selected an earlier clear")
	}
	if a.session.GetID() != current || a.PreviousClearSessionID() != second {
		t.Fatal("stale menu changed owner")
	}
}
func TestClearRewindPreviousEntryIsNotPersistedAcrossProcessRestart(t *testing.T) {
	a := newRewindApp(t)
	a.client = testkit.NewMockClient()
	a.session.AddUserMessage("saved previous dialogue")
	source := a.session.GetID()
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	fresh := a.session.GetID()
	if err := a.ReleaseSessionWriterLease(); err != nil {
		t.Fatal(err)
	}
	restarted := newSessionLeaseTestApp(t, "restarted-process", "glm", a.workDir, "")
	latest, _, err := restarted.sessionManager.LoadLatest()
	if err != nil || latest.ID != fresh || len(latest.History) > 0 {
		t.Fatal("restart would auto-resume cleared dialogue", latest, err)
	}
	if restarted.PreviousClearSessionID() != "" {
		t.Fatal("process-only previous entry was persisted")
	}
	history, _ := chat.NewHistoryManager()
	previous, err := history.LoadFull(source)
	if err != nil || len(previous.History) != 1 {
		t.Fatal("manual /resume source is unavailable after restart", err)
	}
}

func TestClearRewindRestoresSkillHooksWithoutRearmingOnce(t *testing.T) {
	for _, persistent := range []bool{true, false} {
		t.Run(map[bool]string{true: "persistent", false: "ephemeral"}[persistent], func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			writeResumeHookSkill(t, root)
			a, write, read := newResumeHookApp(t, root, testkit.NewMockClient(), "claude-global", false)
			t.Cleanup(func() { _ = a.ReleaseSessionWriterLease() })
			if !persistent {
				a.sessionManager = nil
			}
			read.results = append(read.results, tools.NewSuccessResult("notes"))
			tools.BindSkillInvocationLedger(a.registry, a.session.InvocationLedger())
			a.captureTurnRewind("use guard")
			a.session.AddUserMessage("use guard")
			result, err := a.executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "guard"})
			if err != nil || !result.Success {
				t.Fatal("load guard failed", err)
			}
			result, err = a.executor.InvokeTool(t.Context(), "read", map[string]any{"file_path": "notes.txt"})
			if err != nil || !result.Success {
				t.Fatal("first read failed", err)
			}
			before := a.session.GetState()
			sourceID := before.ID
			if len(before.SkillHooks) != 1 || !before.SkillHooks[0].Consumed[0] {
				t.Fatal("fixture did not retain consumed hook in the session")
			}
			if err := a.ClearConversationForRewind(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(a.GetSkillHooks()) != 0 {
				t.Fatal("old hooks leaked into fresh root")
			}
			if _, err := a.ResumePreviousClearSession(t.Context(), sourceID, false); err != nil {
				t.Fatal(err)
			}
			after := a.session.GetState()
			if !reflect.DeepEqual(before.InvokedSkills, after.InvokedSkills) || !reflect.DeepEqual(before.SkillHooks, after.SkillHooks) {
				t.Fatal("return lost skill snapshots or once consumption")
			}
			result, err = a.executor.InvokeTool(t.Context(), "read", map[string]any{"file_path": "notes.txt"})
			if err != nil || !result.Success {
				t.Fatalf("resumed read failed: %+v %v", result, err)
			}
			runs, err := os.ReadFile(filepath.Join(root, "once-runs"))
			if err != nil || string(runs) != "x" {
				t.Fatal("clear/return rearmed once hook", string(runs), err)
			}
			result, err = a.executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": "denied.txt"})
			if err != nil || result.Success || write.CallCount() != 0 {
				t.Fatalf("restored guard was not enforced: %+v %v write calls=%d", result, err, write.CallCount())
			}
		})
	}
}

func TestClearRewindChangedRequiredSkillLeavesFreshOwnerAndEntry(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeResumeHookSkill(t, root)
	a, _, _ := newResumeHookApp(t, root, testkit.NewMockClient(), "claude-global", false)
	t.Cleanup(func() { _ = a.ReleaseSessionWriterLease() })
	tools.BindSkillInvocationLedger(a.registry, a.session.InvocationLedger())
	a.session.AddUserMessage("source with guard")
	result, err := a.executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "guard"})
	if err != nil || !result.Success {
		t.Fatal("skill load failed", err)
	}
	source := a.session.GetID()
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	fresh := a.session.GetID()
	path := filepath.Join(root, "guard", "SKILL.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), "resume-denied", "changed-denied", 1))
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ResumePreviousClearSession(t.Context(), source, false); err == nil {
		t.Fatal("changed required skill silently restored")
	}
	if a.session.GetID() != fresh || a.PreviousClearSessionID() != source || len(a.session.GetHistory()) != 0 {
		t.Fatal("failed hook restore changed owner or consumed entry")
	}
	if a.previousClearLease == nil || !a.previousClearLease.IsActive() || a.previousClearLease.SessionID() != source {
		t.Fatal("failed restore discarded the previous retention lease")
	}
	if err := a.ReleaseSessionWriterLease(); err != nil {
		t.Fatal(err)
	}
	lease, err := chat.AcquireSessionWriterLease(source)
	if err != nil {
		t.Fatal("shutdown retained previous lease", err)
	}
	_ = lease.Release()
}

func TestClearRewindRetentionLeaseProtectsPreviousDuringCleanup(t *testing.T) {
	a := newRewindApp(t)
	a.client = testkit.NewMockClient()
	cfg := chat.DefaultSessionManagerConfig()
	cfg.MaxSessionCount = 1
	manager, err := chat.NewSessionManager(a.session, cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.sessionManager = manager
	a.captureTurnRewind("keep snapshots")
	a.session.AddUserMessage("keep snapshots")
	source := a.session.GetID()
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := manager.CleanupOldSessions(); err != nil {
		t.Fatal(err)
	}
	history, _ := chat.NewHistoryManager()
	if _, err := history.LoadFull(source); err != nil {
		t.Fatal("cleanup removed live previous dialogue", err)
	}
	if _, err := a.ResumePreviousClearSession(t.Context(), source, false); err != nil {
		t.Fatal("retention pin could not transfer into active writer ownership", err)
	}
	points, err := a.ListRewindPoints(t.Context())
	if err != nil || len(points) != 1 {
		t.Fatal("cleanup removed previous file snapshots", err)
	}
}
