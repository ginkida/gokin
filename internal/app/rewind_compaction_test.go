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
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/testkit"

	"google.golang.org/genai"
)

func wireTestCompaction(t *testing.T, a *App, mock *testkit.MockClient) {
	t.Helper()
	a.ctx = t.Context()
	a.contextManager = appcontext.NewContextManager(t.Context(), a.session, mock, &config.ContextConfig{EnableAutoSummary: true})
	t.Cleanup(a.contextManager.Close)
	a.contextManager.SetSummaryStrategy(appcontext.SummaryStrategy{InitialMessageCount: 0, RecentMessageCount: 2, MinMessagesForSummary: 2, MaxHistorySize: 100})
	a.contextManager.SetCompactionCommitter(a.commitRewindCompaction)
}

func TestRewindSummaryAfterAutomaticCompactionAndResume(t *testing.T) {
	for _, mode := range []string{"summarize-from", "summarize-before"} {
		t.Run(mode, func(t *testing.T) {
			a, ids, mock := summaryAppFixture(t)
			// Leave enough compacted context for another, narrower summary to
			// free space even when its boundary expands part of the older span.
			mock = testkit.NewMockClient().EnqueueText(strings.Repeat("Automatic compact overview. ", 400))
			a.client = mock
			wireTestCompaction(t, a, mock)
			original := a.session.GetState()
			if err := a.contextManager.OptimizeContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			compacted := a.session.GetState()
			if len(compacted.History) >= len(original.History) || !strings.Contains(compacted.History[0].Parts[len(compacted.History[0].Parts)-1].Text, "gokin://summary/") {
				t.Fatal("automatic compaction did not archive its original messages")
			}
			// Load from the saved session and reopen the journal before selection.
			history, err := chat.NewHistoryManager()
			if err != nil {
				t.Fatal(err)
			}
			saved, err := history.LoadFull(original.ID)
			if err != nil || !reflect.DeepEqual(saved.History, compacted.History) {
				t.Fatal("automatic compaction was not durable", err)
			}
			a.resetRewindOwner()
			if err := a.session.RestoreFromState(saved); err != nil {
				t.Fatal(err)
			}
			mock.EnqueueText("Concise selected decisions.")
			if _, err := a.SummarizeAtPoint(t.Context(), ids[1], mode, "Keep original decisions."); err != nil {
				t.Fatal(err)
			}
			calls := mock.Calls()
			last := calls[len(calls)-1].Message
			if !strings.Contains(last, "long technical details") || strings.Contains(last, "gokin://summary/") {
				t.Fatal("targeted model call received the automatic summary rather than original messages")
			}
			if mode == "summarize-before" && strings.Contains(last, "second request") || mode == "summarize-from" && strings.Contains(last, "first request") {
				t.Fatal("automatic compaction moved the selected boundary")
			}
		})
	}
}

func TestRewindIncrementalAndEmergencyCompactionCommitThroughOwner(t *testing.T) {
	for _, mode := range []string{"incremental", "emergency"} {
		t.Run(mode, func(t *testing.T) {
			a := newRewindApp(t)
			mock := testkit.NewMockClient().EnqueueText("Short summary of old turns.")
			a.client = mock
			wireTestCompaction(t, a, mock)
			for i := 0; i < 32; i++ {
				a.captureTurnRewind("request")
				a.session.AddUserMessage("request")
				a.session.AddModelMessage(strings.Repeat("long retained decisions ", 200))
			}
			points, err := a.ListRewindPoints(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			original := a.session.GetState()
			if mode == "incremental" {
				err = a.contextManager.IncrementalCompact(t.Context())
			} else {
				// A small configured model budget forces the synchronous path.
				a.contextManager.SetConfig(&config.ContextConfig{EnableAutoSummary: true, MaxInputTokens: 10000})
				if removed := a.contextManager.EmergencyTruncate(); removed <= 0 {
					t.Fatal("emergency compaction did not execute")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			plan, err := a.rewindStore.PlanSummary(points[3].ID, "summarize-before", a.session.GetState())
			if err != nil || !reflect.DeepEqual(plan.Messages, original.History[:6]) {
				t.Fatal("compaction discarded early-turn boundaries", err)
			}
		})
	}
}

func TestRewindToolOutputCleanupKeepsOriginalResponseAndBoundaries(t *testing.T) {
	a := newRewindApp(t)
	a.captureTurnRewind("large search")
	a.session.AddUserMessage("large search")
	a.session.AddContent(&genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "search-1", Name: "grep"}}}})
	raw := strings.Repeat("original search detail\n", 400)
	a.session.AddContent(&genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "search-1", Name: "grep", Response: map[string]any{"content": raw}}}}})
	for i := 0; i < 6; i++ {
		a.captureTurnRewind("later request")
		a.session.AddUserMessage("later request")
		a.session.AddModelMessage("later answer")
	}
	original := a.session.GetState()
	points, err := a.ListRewindPoints(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	a.applyToolOutputHygiene()
	current := a.session.GetState()
	if current.History[2].Parts[0].FunctionResp.Response["content"] == raw {
		t.Fatal("old tool output was not compacted")
	}
	plan, err := a.rewindStore.PlanSummary(points[1].ID, "summarize-before", current)
	if err != nil || !reflect.DeepEqual(plan.Messages, original.History[:3]) {
		t.Fatal("tool-output cleanup lost its original response or boundary", err)
	}
}

func TestRewindCompactionStaleCancelledAndArchiveFailurePreserveHistory(t *testing.T) {
	for _, scenario := range []string{"stale", "cancelled", "archive-failure"} {
		t.Run(scenario, func(t *testing.T) {
			a, _, _ := summaryAppFixture(t)
			original := a.session.GetState()
			version := original.Version
			ctx := t.Context()
			if scenario == "stale" {
				a.session.AddUserMessage("concurrent newest turn")
			}
			if scenario == "cancelled" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if scenario == "archive-failure" {
				root, err := chat.RewindStorageDir()
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root, a.session.GetID(), "index.json")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before := a.session.GetState()
			_, committed, err := a.commitRewindCompaction(ctx, []*genai.Content{genai.NewContentFromText("short summary", genai.RoleUser)}, version, 1, "test")
			after := a.session.GetState()
			after.LastActive = before.LastActive
			if committed || !reflect.DeepEqual(before, after) {
				t.Fatal("failed compaction changed live history", committed, err)
			}
			if scenario == "cancelled" && !errors.Is(err, context.Canceled) || scenario == "archive-failure" && err == nil {
				t.Fatal("compaction failure was hidden", err)
			}
		})
	}
}

func TestRewindAutomaticCompactionDoesNotOverwriteConcurrentTurnOrClear(t *testing.T) {
	for _, change := range []string{"new-turn", "clear"} {
		t.Run(change, func(t *testing.T) {
			a, _, mock := summaryAppFixture(t)
			wireTestCompaction(t, a, mock)
			var expected *chat.SessionState
			mock.OnSend = func(context.Context) {
				if change == "clear" {
					if err := a.ClearConversationForRewind(t.Context()); err != nil {
						t.Fatal(err)
					}
				} else {
					a.session.AddUserMessage("newest foreground turn")
				}
				expected = a.session.GetState()
			}
			if err := a.contextManager.OptimizeContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if expected == nil {
				t.Fatal("fixture never reached the summarizer")
			}
			after := a.session.GetState()
			after.LastActive = expected.LastActive
			if !reflect.DeepEqual(expected, after) {
				t.Fatal("stale automatic compaction overwrote the current conversation")
			}
		})
	}
}

func TestRewindCompactionWithoutSessionPersistenceWritesNoArchive(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	mock := testkit.NewMockClient().EnqueueText("A short overview of previous decisions.")
	a := &App{session: chat.NewSession(), client: mock}
	for i := 0; i < 10; i++ {
		a.session.AddUserMessage("request")
		a.session.AddModelMessage(strings.Repeat("long response ", 100))
	}
	wireTestCompaction(t, a, mock)
	if err := a.contextManager.OptimizeContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if a.session.MessageCount() >= 20 {
		t.Fatal("ephemeral compaction was skipped")
	}
	if _, err := os.Stat(filepath.Join(data, "gokin")); !os.IsNotExist(err) {
		t.Fatal("ephemeral compaction created persistent session data", err)
	}
}

func TestRewindCompactionPreservesRetainedAndArchivedImageParts(t *testing.T) {
	a := newRewindApp(t)
	a.captureTurnRewind("inspect screenshot")
	image := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{
		genai.NewPartFromText("inspect screenshot"),
		genai.NewPartFromBytes([]byte{1, 2, 3, 4}, "image/png"),
	}}
	a.session.AddContent(image)
	a.session.AddModelMessage(strings.Repeat("visual findings ", 200))
	a.captureTurnRewind("continue")
	a.session.AddUserMessage("continue")
	a.session.AddModelMessage("latest response")
	original := a.session.GetState()
	current := a.session.GetHistory()
	current[1] = genai.NewContentFromText("Short findings.", genai.RoleModel)
	committed, ok, err := a.commitRewindCompaction(t.Context(), current, original.Version, 1, "visual findings")
	if err != nil || !ok || !reflect.DeepEqual(committed[0], image) {
		t.Fatal("compaction lost an unselected image", ok, err)
	}
	current = []*genai.Content{genai.NewContentFromText("Summary of screenshot and findings.", genai.RoleUser)}
	if _, ok, err := a.commitRewindCompaction(t.Context(), current, a.session.GetVersion(), 1, "visual context"); err != nil || !ok {
		t.Fatal("image context could not be archived", ok, err)
	}
	points, err := a.ListRewindPoints(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := a.rewindStore.PlanSummary(points[1].ID, "summarize-before", a.session.GetState())
	if err != nil || !reflect.DeepEqual(plan.Messages, original.History[:2]) {
		t.Fatal("archived image or its original boundary was lost", err)
	}
}
