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
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func installSessionStartSettings(t *testing.T, a *App, groups ...map[string]any) {
	t.Helper()
	a.hooksManager.ConfigureClaudeSettings(a.workDir, t.TempDir(), true)
	path := filepath.Join(a.workDir, ".claude/settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"hooks": map[string]any{"SessionStart": groups}})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.closeSessionStartHooks)
}

func sessionStartCommand(matcher, command string) map[string]any {
	return map[string]any{"matcher": matcher, "hooks": []any{map[string]any{"type": "command", "command": command}}}
}

func waitSessionStartFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("startup hook never reached barrier", path)
		}
	}
}

func TestSessionStartWaitsForContextAndEscKeepsHooksRunning(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("Done")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	installSessionStartSettings(t, a, sessionStartCommand("startup", `cat > start-input.json; touch started; while [ ! -f release ]; do sleep 0.02; done; printf '%s' '{"continue":false,"hookSpecificOutput":{"additionalContext":"START_PROOF","sessionTitle":"Startup title"}}'`))
	owner := a.ensureSessionStartHooks()
	waitSessionStartFile(t, filepath.Join(a.workDir, "started"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := a.awaitSessionStart(ctx, owner); err == nil || owner.ctx.Err() != nil || len(worker.Calls()) != 0 {
		t.Fatal("cancelled wait killed startup or sent a request")
	}
	a.advanceConversationEpoch() // Esc also changes recovery authority.
	if a.ensureSessionStartHooks() != owner {
		t.Fatal("Esc replaced SessionStart owner")
	}
	if err := os.WriteFile(filepath.Join(a.workDir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RunHeadlessWithOptions(t.Context(), "inspect", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if len(worker.Calls()) != 1 || !strings.Contains(worker.Calls()[0].Message, "START_PROOF") || a.session.GetTitle() != "Startup title" {
		t.Fatal("startup evidence/title missing", worker.Calls(), a.session.GetTitle())
	}
	data, _ := os.ReadFile(filepath.Join(a.workDir, "start-input.json"))
	var input map[string]any
	_ = json.Unmarshal(data, &input)
	if input["source"] != "startup" || input["session_id"] != a.session.GetID() || input["tool_name"] != nil {
		t.Fatalf("wrong startup identity: %s", data)
	}
}

func TestSessionStartInitialMessageAndExplicitFollowupShareTurnLimit(t *testing.T) {
	for _, tc := range []struct {
		prompt       string
		limit, calls int
		failed       bool
	}{
		{"", 2, 1, false}, {"explicit second turn", 2, 2, false}, {"explicit second turn", 1, 1, true},
	} {
		t.Run(tc.prompt+string(rune('0'+tc.limit)), func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueText("initial response").EnqueueText("explicit response")
			a := newGoalTestApp(t, worker, testkit.NewMockClient())
			installSessionStartSettings(t, a, sessionStartCommand("startup", `printf '%s' '{"hookSpecificOutput":{"initialUserMessage":"initial first turn","additionalContext":"START_PROOF"}}'`))
			result, err := a.RunHeadlessWithOptions(t.Context(), tc.prompt, HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: tc.limit})
			if (err != nil) != tc.failed || len(worker.Calls()) != tc.calls || result.Usage.ModelRounds != tc.calls {
				t.Fatalf("wrong initial/followup accounting: %+v %v calls=%d", result, err, len(worker.Calls()))
			}
			if !strings.Contains(worker.Calls()[0].Message, "initial first turn") || !strings.Contains(worker.Calls()[0].Message, "START_PROOF") {
				t.Fatal("initial message/context missing", worker.Calls())
			}
			if tc.calls == 2 && (!strings.Contains(worker.Calls()[1].Message, tc.prompt) || result.Result != "explicit response") {
				t.Fatal("explicit prompt did not run as the second turn", worker.Calls())
			}
		})
	}
}

func TestSessionStartClearCancelsOldResultsAndEnvironment(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("Done")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	installSessionStartSettings(t, a,
		sessionStartCommand("startup", `touch started; sleep 30; printf 'OLD_PROOF'`),
		sessionStartCommand("clear", `printf 'export CLEAR_PROOF=ready\n' >> "$CLAUDE_ENV_FILE"; printf 'NEW_PROOF'`))
	old := a.ensureSessionStartHooks()
	waitSessionStartFile(t, filepath.Join(a.workDir, "started"))
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	if old.ctx.Err() == nil {
		t.Fatal("old startup was left running")
	}
	if _, err := a.RunHeadlessWithOptions(t.Context(), "inspect new conversation", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	call := worker.Calls()[0]
	if !strings.Contains(call.Message, "NEW_PROOF") || strings.Contains(call.Message, "OLD_PROOF") || a.sessionStart.envFile == old.envFile {
		t.Fatal("clear leaked old startup evidence/environment", call)
	}
}

func TestSessionStartCompactAddsFreshContextWithoutReplacingEnvironment(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("First").EnqueueText("Second")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	installSessionStartSettings(t, a, sessionStartCommand("startup", "printf START_PROOF"), sessionStartCommand("compact", "printf COMPACT_PROOF"))
	opts := HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard}
	if _, err := a.RunHeadlessWithOptions(t.Context(), "first", opts); err != nil {
		t.Fatal(err)
	}
	env := a.sessionStart.envFile
	state := a.session.GetState()
	if _, ok, err := a.commitRewindCompaction(t.Context(), a.session.GetHistory(), state.Version, 0, "test compaction"); err != nil || !ok {
		t.Fatal("compaction failed", ok, err)
	}
	if _, err := a.RunHeadlessWithOptions(t.Context(), "second", opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(worker.Calls()[1].Message, "COMPACT_PROOF") || a.sessionStart.envFile != env {
		t.Fatal("compaction lost context or reset session environment", worker.Calls())
	}
}

func TestSessionStartBashEnvironmentStaysInOwningConversation(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	installSessionStartSettings(t, a, sessionStartCommand("startup", `printf '%s\n' 'export GOKIN_START_PROOF="value with spaces"' >> "$CLAUDE_ENV_FILE"`))
	ctx := a.withSessionStartHooks(t.Context())
	if err := hooks.AwaitSessionStart(ctx); err != nil {
		t.Fatal(err)
	}
	workDir, err := filepath.EvalSymlinks(a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	bash := tools.NewBashTool(workDir)
	result, err := bash.Execute(ctx, map[string]any{"command": `printf '%s' "$GOKIN_START_PROOF"`})
	if err != nil || !result.Success || !strings.Contains(result.Content, "value with spaces") || os.Getenv("GOKIN_START_PROOF") != "" {
		t.Fatal("startup environment was lost or changed host process", result, err)
	}
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx = a.withSessionStartHooks(t.Context())
	if err := hooks.AwaitSessionStart(ctx); err != nil {
		t.Fatal(err)
	}
	result, err = bash.Execute(ctx, map[string]any{"command": `printf 'after-clear:%s' "$GOKIN_START_PROOF"`})
	if err != nil || !result.Success || strings.Contains(result.Content, "value with spaces") {
		t.Fatal("environment survived clear", result, err)
	}
}

func TestSessionStartResumeUsesTargetIdentity(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	installSessionStartSettings(t, a, sessionStartCommand("resume", "cat > resumed-start.json; printf RESUME_PROOF"))
	target := chat.NewSession()
	target.SetWorkDir(a.workDir)
	target.SetProvider("glm")
	target.AddUserMessage("target")
	storage, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveFull(target); err != nil {
		t.Fatal(err)
	}
	a.running = true
	if _, err := a.SwitchSession(t.Context(), target.GetState(), false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(a.workDir, "resumed-start.json"))
	if err != nil || !strings.Contains(string(data), target.GetID()) || !strings.Contains(a.takeSessionStartNotes(a.sessionStart), "RESUME_PROOF") {
		t.Fatalf("resume did not join target startup: %s %v", data, err)
	}
}
