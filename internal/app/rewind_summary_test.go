package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

func summaryAppFixture(t *testing.T) (*App, []string, *testkit.MockClient) {
	t.Helper()
	a := newRewindApp(t)
	mock := testkit.NewMockClient().EnqueueText("Preserved the selected technical decisions, file changes, and unresolved work.")
	a.client = mock
	a.registry.MustRegister(tools.NewReadTool(a.workDir))
	for _, prompt := range []string{"first request", "second request", "third request"} {
		a.captureTurnRewind(prompt)
		a.session.AddUserMessage(prompt)
		a.session.AddModelMessage(strings.Repeat(prompt+" long technical details ", 180))
	}
	points, err := a.ListRewindPoints(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(points))
	for i, p := range points {
		ids[i] = p.ID
	}
	return a, ids, mock
}
func TestTargetedSummaryPreservesCodeOriginalsAndUnselectedHistory(t *testing.T) {
	for _, mode := range []string{"summarize-from", "summarize-before"} {
		t.Run(mode, func(t *testing.T) {
			a, ids, mock := summaryAppFixture(t)
			before := a.session.GetState()
			path := filepath.Join(a.workDir, "code.go")
			if err := os.WriteFile(path, []byte("unchanged code"), 0600); err != nil {
				t.Fatal(err)
			}
			a.session.SetToolCheckpoints([]chat.SerializedToolCheckpoint{{CallID: "retry-call", ToolName: "edit"}})
			a.session.AddPendingRecovery(chat.SerializedPendingRecovery{ID: "pending", SessionID: a.session.GetID(), State: chat.PendingRecoveryScheduled}, "")
			epoch := a.recoveryEpoch.Load()
			if _, err := a.SummarizeAtPoint(t.Context(), ids[1], mode, "Keep the unresolved database migration."); err != nil {
				t.Fatal(err)
			}
			after := a.session.GetState()
			if mode == "summarize-from" {
				if len(after.History) != 3 || !reflect.DeepEqual(before.History[:2], after.History[:2]) {
					t.Fatal("suffix summary changed earlier messages")
				}
			} else {
				if len(after.History) != 5 || !reflect.DeepEqual(before.History[2:], after.History[1:]) {
					t.Fatal("prefix summary changed later messages")
				}
			}
			data, _ := os.ReadFile(path)
			if string(data) != "unchanged code" {
				t.Fatal("summary changed code")
			}
			if epoch != a.recoveryEpoch.Load() || len(after.ToolCheckpoints) != 1 || len(after.PendingRecoveries) != 1 {
				t.Fatal("summary reset executable retry lineage")
			}
			calls := mock.Calls()
			if len(calls) != 1 || !strings.Contains(calls[0].Message, "Keep the unresolved database migration.") {
				t.Fatal("summary guidance missing")
			}
			if mode == "summarize-from" && strings.Contains(calls[0].Message, "first request") {
				t.Fatal("model received unselected earlier messages")
			}
			if mode == "summarize-before" && strings.Contains(calls[0].Message, "second request") {
				t.Fatal("model received unselected later messages")
			}
			refs := a.rewindStore.Points()
			if len(refs) != 3 {
				t.Fatal("summary changed checkpoints")
			}
			root, err := chat.RewindStorageDir()
			if err != nil {
				t.Fatal(err)
			}
			indexBytes, err := os.ReadFile(filepath.Join(root, a.session.GetID(), "index.json"))
			if err != nil {
				t.Fatal(err)
			}
			var index struct {
				Summaries []struct {
					ID string `json:"id"`
				}
			}
			if err := json.Unmarshal(indexBytes, &index); err != nil || len(index.Summaries) != 1 {
				t.Fatal("summary archive not persisted", err)
			}
			id := index.Summaries[0].ID
			original, err := a.ReadSummaryTranscript(t.Context(), id)
			if err != nil || !strings.Contains(original, "long technical details") {
				t.Fatal("original transcript unavailable", err)
			}
			reader, _ := a.registry.Get("read")
			result, err := reader.Execute(t.Context(), map[string]any{"file_path": "gokin://summary/" + id})
			if err != nil || !result.Success || !strings.Contains(result.Content, "long technical details") {
				t.Fatal("model-readable archive unavailable", result, err)
			}
			if _, err := os.Stat(filepath.Join(a.workDir, ".gokin")); !os.IsNotExist(err) {
				t.Fatal("summary wrote to the project")
			}
			history, err := chat.NewHistoryManager()
			if err != nil {
				t.Fatal(err)
			}
			saved, err := history.LoadFull(a.session.GetID())
			if err != nil || len(saved.History) != len(after.History) || len(saved.PendingRecoveries) != 1 {
				t.Fatal("condensed state not durable", err)
			}
		})
	}
}
func TestTargetedSummaryFailuresLeaveConversationUnchanged(t *testing.T) {
	for _, scenario := range []string{"empty", "tool-call", "larger", "cancelled", "save-failure", "journal-link"} {
		t.Run(scenario, func(t *testing.T) {
			a, ids, _ := summaryAppFixture(t)
			mock := testkit.NewMockClient()
			a.client = mock
			switch scenario {
			case "empty":
				mock.EnqueueText(" ")
			case "tool-call":
				mock.EnqueueToolCall("write", map[string]any{"file_path": "unrequested"})
			case "larger":
				mock.EnqueueText(strings.Repeat("expansion ", 50000))
			default:
				mock.EnqueueText("A concise preservation of selected technical decisions and unfinished work.")
			}
			ctx := t.Context()
			if scenario == "cancelled" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if scenario == "save-failure" {
				file := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gokin", "sessions", a.session.GetID()+".json")
				if err := os.MkdirAll(file, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "journal-link" {
				root, err := chat.RewindStorageDir()
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root, a.session.GetID())
				outside := filepath.Join(t.TempDir(), "saved")
				if err := os.Rename(path, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Skip(err)
				}
			}
			before := a.session.GetState()
			if _, err := a.SummarizeAtPoint(ctx, ids[1], "summarize-from", ""); err == nil {
				t.Fatal("unsafe summary accepted", scenario)
			}
			if !reflect.DeepEqual(before.History, a.session.GetState().History) {
				t.Fatal("failed summary changed history")
			}
			if _, err := os.Stat(filepath.Join(a.workDir, "unrequested")); !os.IsNotExist(err) {
				t.Fatal("summary executed a tool call")
			}
		})
	}
}
func TestTargetedSummaryRejectsConcurrentConversationChange(t *testing.T) {
	a, ids, _ := summaryAppFixture(t)
	mock := testkit.NewMockClient().EnqueueScript(testkit.ResponseScript{DelayBeforeFirstChunk: 100 * time.Millisecond, Chunks: []client.ResponseChunk{{Text: "A concise preservation of selected technical decisions and unfinished work."}, {Done: true}}})
	a.client = mock
	done := make(chan error, 1)
	go func() { _, err := a.SummarizeAtPoint(t.Context(), ids[1], "summarize-from", ""); done <- err }()
	deadline := time.Now().Add(time.Second)
	for len(mock.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	a.session.AddUserMessage("new steering during summary")
	if err := <-done; err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal("stale summary published", err)
	}
	last := a.session.GetState().History
	if !strings.Contains(last[len(last)-1].Parts[0].Text, "new steering") {
		t.Fatal("concurrent message lost")
	}
}
func TestTargetedSummaryProviderErrorLeavesHistory(t *testing.T) {
	a, ids, _ := summaryAppFixture(t)
	a.client = testkit.NewMockClient().EnqueueError(errors.New("provider unavailable"))
	before := a.session.GetState()
	if _, err := a.SummarizeAtPoint(t.Context(), ids[0], "summarize-from", ""); err == nil {
		t.Fatal("provider failure ignored")
	}
	if !reflect.DeepEqual(before.History, a.session.GetState().History) {
		t.Fatal("provider failure lost messages")
	}
}

func TestTargetedSummaryKeepsFileRewindAvailable(t *testing.T) {
	a, ids, _ := summaryAppFixture(t)
	path := filepath.Join(a.workDir, "changed.txt")
	// Record a real edit in the last turn's independent file timeline.
	rewindWrite(t, a, path, "latest code")
	if _, err := a.SummarizeAtPoint(t.Context(), ids[1], "summarize-from", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RewindToPoint(t.Context(), ids[1], "code"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("summarization lost the code checkpoint")
	}
	history := a.session.GetState().History
	if len(history) != 3 || !strings.Contains(history[2].Parts[0].Text, "Summarized conversation") {
		t.Fatal("code-only rewind lost condensed context")
	}
}
func TestTargetedSummaryPreservesSkillHookOnceState(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeResumeHookSkill(t, root)
	mock := testkit.NewMockClient().EnqueueText("Preserve the active guard skill and the remaining work without rearming its hooks.")
	a, write, read := newResumeHookApp(t, root, mock, "claude-global", false)
	read.results = append(read.results, tools.NewSuccessResult("notes"))
	tools.BindSkillInvocationLedger(a.registry, a.session.InvocationLedger())
	t.Cleanup(func() { _ = a.ReleaseSessionWriterLease() })
	a.captureTurnRewind("load guard")
	a.session.AddUserMessage("load guard")
	result, err := a.executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "guard"})
	if err != nil || !result.Success {
		t.Fatalf("load=%+v %v", result, err)
	}
	result, err = a.executor.InvokeTool(t.Context(), "read", map[string]any{"file_path": "notes.txt"})
	if err != nil || !result.Success {
		t.Fatalf("read=%+v %v", result, err)
	}
	a.session.AddModelMessage(strings.Repeat("Guard active and technical details preserved. ", 600))
	before := a.session.GetState()
	if len(before.SkillHooks) != 1 || !before.SkillHooks[0].Consumed[0] {
		t.Fatal("fixture did not retain consumed hook in the session")
	}
	// Automatic compaction uses the same owner without restoring/rebinding the
	// hook runtime. Verify the consumed once handler across both kinds of edit.
	raw := skills.ReattachInvocations(a.session.GetHistory(), nil, 0)
	raw[len(raw)-1] = genai.NewContentFromText(strings.Repeat("Guard active and technical details preserved. ", 100), genai.RoleModel)
	if _, committed, err := a.commitRewindCompaction(t.Context(), raw, before.Version, 1, "test automatic compaction"); err != nil || !committed {
		t.Fatal("automatic compaction failed", committed, err)
	}
	automatic := a.session.GetState()
	if !reflect.DeepEqual(before.InvokedSkills, automatic.InvokedSkills) || !reflect.DeepEqual(before.SkillHooks, automatic.SkillHooks) {
		t.Fatal("automatic compaction changed skill authority or consumed hooks")
	}
	points, err := a.ListRewindPoints(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SummarizeAtPoint(t.Context(), points[0].ID, "summarize-from", ""); err != nil {
		t.Fatal(err)
	}
	after := a.session.GetState()
	if !reflect.DeepEqual(before.InvokedSkills, after.InvokedSkills) || !reflect.DeepEqual(before.SkillHooks, after.SkillHooks) {
		t.Fatal("summary changed skill authority or consumed hooks")
	}
	result, err = a.executor.InvokeTool(t.Context(), "read", map[string]any{"file_path": "notes.txt"})
	if err != nil || !result.Success {
		t.Fatalf("active skill could not continue: %+v %v", result, err)
	}
	runs, err := os.ReadFile(filepath.Join(root, "once-runs"))
	if err != nil || string(runs) != "x" {
		t.Fatal("summary rearmed once hook", string(runs), err)
	}
	result, err = a.executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": "denied.txt"})
	if err != nil || result.Success || write.CallCount() != 0 {
		t.Fatal("summary dropped active guard")
	}
}
