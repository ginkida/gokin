package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/hooks"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func installSessionEndSettings(t *testing.T, a *App, command string) {
	t.Helper()
	a.hooksManager.ConfigureClaudeSettings(a.workDir, t.TempDir(), true)
	path := filepath.Join(a.workDir, ".claude/settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"hooks": map[string]any{"SessionEnd": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}}})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func readSessionEndEvents(t *testing.T, a *App) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(a.workDir, "ended.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func TestSessionEndIsOncePerOwnerAndClearKeepsOldIdentity(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	installSessionEndSettings(t, a, `cat >> ended.jsonl; printf '\n' >> ended.jsonl; printf '%s' '{"continue":false,"decision":"block"}'`)
	a.session.AddUserMessage("old conversation")
	oldID := a.session.GetID()
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	newID := a.session.GetID()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a.EndSessionHooks(ctx, "other")
	a.EndSessionHooks(ctx, "other")
	events := readSessionEndEvents(t, a)
	if len(events) != 2 || events[0]["session_id"] != oldID || events[0]["reason"] != "clear" || events[1]["session_id"] != newID || events[1]["reason"] != "other" || len(a.session.GetHistory()) != 0 {
		t.Fatalf("wrong end lifecycle: %+v", events)
	}
}

func TestFailedSessionReplacementDoesNotRunSessionEnd(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	installSessionEndSettings(t, a, `touch ended`)
	a.session.AddUserMessage("old conversation")
	path := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gokin/sessions", a.session.GetID()+".json")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := a.ClearConversationForRewind(t.Context()); err == nil {
		t.Fatal("invalid source save reported success")
	}
	if _, err := os.Stat(filepath.Join(a.workDir, "ended")); !os.IsNotExist(err) {
		t.Fatal("failed clear ran terminal cleanup")
	}
}

func TestSessionEndOnResumeRunsBeforeSourceLeaseIsReleased(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	installSessionEndSettings(t, a, `cat >> ended.jsonl; printf '\n' >> ended.jsonl`)
	a.session.AddUserMessage("old conversation")
	oldID := a.session.GetID()
	target := chat.NewSession()
	target.SetWorkDir(a.workDir)
	target.SetProvider("glm")
	target.AddUserMessage("target conversation")
	history, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := history.SaveFull(target); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SwitchSession(t.Context(), target.GetState(), false); err != nil {
		t.Fatal(err)
	}
	events := readSessionEndEvents(t, a)
	if len(events) != 1 || events[0]["session_id"] != oldID || events[0]["reason"] != "resume" || a.sessionLease.SessionID() != target.GetID() {
		t.Fatalf("resume end identity/lease wrong: %+v", events)
	}
}

func TestSessionEndSkillOncePersistsInArchivedSourceAndDoesNotLeakAcrossClear(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("skill", map[string]any{"name": "cleanup"}).EnqueueText("Done")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	root := filepath.Join(a.workDir, "skills")
	path := filepath.Join(root, "cleanup/SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	document := "---\ndescription: Cleanup once\nhooks:\n  SessionEnd:\n    - hooks:\n        - type: command\n          command: printf x >> once-runs\n          once: true\n  PreToolUse:\n    - matcher: Bash\n      hooks:\n        - type: command\n          command: exit 0\n          once: true\n---\nInspect the proof.\n"
	if err := os.WriteFile(path, []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	a.registry.MustRegister(tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}), a.workDir))
	if _, err := a.RunHeadlessWithOptions(t.Context(), "load cleanup", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	oldID := a.session.GetID()
	a.hooksManager.SetHandler(func(hook *hooks.Hook, _ string, _ error) {
		if hook.Type != hooks.SessionEnd {
			return
		}
		// Model a late completion already persisted while cleanup was running.
		storage, err := chat.NewHistoryManager()
		if err != nil {
			t.Fatal(err)
		}
		state, err := storage.LoadFull(oldID)
		if err != nil {
			t.Fatal(err)
		}
		state.SkillHooks[0].Consumed[1] = true
		if err := storage.SaveFullState(state); err != nil {
			t.Fatal(err)
		}
	})
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	history, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := history.LoadFull(oldID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.SkillHooks) != 1 || !saved.SkillHooks[0].Consumed[0] || !saved.SkillHooks[0].Consumed[1] {
		t.Fatalf("source once outcome not persisted: %+v", saved.SkillHooks)
	}
	current, err := history.LoadFull(a.session.GetID())
	if err != nil {
		t.Fatal(err)
	}
	listed, err := history.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if !current.LastActive.After(saved.LastActive) || len(listed) < 2 || listed[0].ID != current.ID {
		t.Fatal("saving cleanup once made the ended source the newest session")
	}
	if _, err := a.ResumePreviousClearSession(t.Context(), oldID, false); err != nil {
		t.Fatal(err)
	}
	a.EndSessionHooks(t.Context(), "other")
	data, _ := os.ReadFile(filepath.Join(a.workDir, "once-runs"))
	if string(data) != "x" {
		t.Fatalf("restored cleanup ran twice or in new root: %q", data)
	}
}

func TestSessionEndDoesNotHangShutdownBehindAnUnfinishedWriter(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	installSessionEndSettings(t, a, `touch ended`)
	a.sessionLeaseMu.Lock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	finished := make(chan struct{})
	go func() { a.EndSessionHooks(ctx, "other"); close(finished) }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		a.sessionLeaseMu.Unlock()
		t.Fatal("SessionEnd ignored shutdown deadline while writer was active")
	}
	a.sessionLeaseMu.Unlock()
	if _, err := os.Stat(filepath.Join(a.workDir, "ended")); !os.IsNotExist(err) {
		t.Fatal("cleanup ran against an unfinished owner")
	}
	a.EndSessionHooks(t.Context(), "other")
	if _, err := os.Stat(filepath.Join(a.workDir, "ended")); err != nil {
		t.Fatal("skipped cleanup consumed lifecycle admission", err)
	}
}
