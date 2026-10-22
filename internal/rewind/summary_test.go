package rewind

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gokin/internal/chat"
	"google.golang.org/genai"
)

func summaryFixture(t *testing.T) (*Store, *chat.Session, []string, string, string) {
	t.Helper()
	store, session, _, work, root := fixture(t)
	var ids []string
	for _, name := range []string{"first", "second", "third"} {
		ids = append(ids, begin(t, store, session, name))
		session.AddModelMessage(strings.Repeat(name+" details ", 100))
	}
	return store, session, ids, work, root
}
func publishTestSummary(t *testing.T, store *Store, session *chat.Session, plan *SummaryPlan) chat.SerializedContent {
	t.Helper()
	marker := chat.SerializeContent(genai.NewContentFromText("[Summarized conversation: "+plan.ID+"]\nConcise summary of the selected details.", genai.RoleUser))
	if err := store.SaveSummary(plan, marker); err != nil {
		t.Fatal(err)
	}
	state := session.GetState()
	state.History = append(append(append([]chat.SerializedContent{}, plan.Before...), marker), plan.After...)
	state.TokenCounts = nil
	state.TotalTokens = 0
	if err := session.RestoreFromState(state); err != nil {
		t.Fatal(err)
	}
	return marker
}
func TestSummaryPlansPreserveExactUnselectedMessages(t *testing.T) {
	for _, mode := range []string{"summarize-from", "summarize-before"} {
		t.Run(mode, func(t *testing.T) {
			store, session, ids, _, _ := summaryFixture(t)
			original := session.GetState().History
			plan, err := store.PlanSummary(ids[1], mode, session.GetState())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "summarize-from" {
				if !reflect.DeepEqual(plan.Before, original[:2]) || !reflect.DeepEqual(plan.Messages, original[2:]) || len(plan.After) != 0 {
					t.Fatal("wrong suffix interval")
				}
			} else {
				if len(plan.Before) != 0 || !reflect.DeepEqual(plan.Messages, original[:2]) || !reflect.DeepEqual(plan.After, original[2:]) {
					t.Fatal("wrong prefix interval")
				}
			}
			marker := publishTestSummary(t, store, session, plan)
			archive, err := store.Summary(plan.ID)
			if err != nil || !reflect.DeepEqual(archive.Original, plan.Messages) || !reflect.DeepEqual(archive.Marker, marker) {
				t.Fatal("original transcript lost", err)
			}
			if got := store.Points(); len(got) != 3 || got[1].ID != ids[1] {
				t.Fatal("summary changed rewind points")
			}
		})
	}
}
func TestSummaryBoundaryInsideOlderSummaryExpandsOnlyAffectedSpan(t *testing.T) {
	store, session, ids, _, root := summaryFixture(t)
	original := session.GetState().History
	suffix, err := store.PlanSummary(ids[1], "summarize-from", session.GetState())
	if err != nil {
		t.Fatal(err)
	}
	marker := publishTestSummary(t, store, session, suffix)
	before, err := store.PlanSummary(ids[1], "summarize-before", session.GetState())
	if err != nil {
		t.Fatal(err)
	}
	if len(before.After) != 1 || !reflect.DeepEqual(before.After[0], marker) {
		t.Fatal("unselected summary representation changed")
	}
	inside, err := store.PlanSummary(ids[2], "summarize-from", session.GetState())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inside.Before, original[:4]) || !reflect.DeepEqual(inside.Messages, original[4:]) {
		t.Fatal("point inside previous summary chose the wrong original messages")
	}
	publishTestSummary(t, store, session, inside)
	restarted, err := Open(root, session.GetID(), session.GetWorkDir())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := restarted.PlanSummary(ids[1], "summarize-from", session.GetState())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Messages, original[2:]) {
		t.Fatal("restart lost summary expansion mapping")
	}
}
func TestSummaryArchivesSurviveCheckpointPruningAndFork(t *testing.T) {
	store, session, ids, work, root := summaryFixture(t)
	plan, err := store.PlanSummary(ids[0], "summarize-from", session.GetState())
	if err != nil {
		t.Fatal(err)
	}
	publishTestSummary(t, store, session, plan)
	for i := 0; i < MaxPoints+1; i++ {
		// Exercise snapshot retention independently of Session's separate
		// 100-message sliding window.
		if _, err := store.Begin("next", session.GetState()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Summary(plan.ID); err != nil {
		t.Fatal("checkpoint pruning deleted transcript", err)
	}
	if _, err := store.Fork("summary-branch"); err != nil {
		t.Fatal(err)
	}
	fork, err := Open(root, "summary-branch", work)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fork.Summary(plan.ID); err != nil {
		t.Fatal("fork lost original transcript", err)
	}
	state := session.GetState()
	state.ID = "summary-branch"
	if _, err := fork.PlanSummary(fork.Points()[0].ID, "summarize-before", state); err != nil {
		t.Fatal("fork could not resolve summarized checkpoint", err)
	}
}
func TestSummaryCorruptOrDivergedHistoryFailsWithoutMutation(t *testing.T) {
	store, session, ids, _, _ := summaryFixture(t)
	plan, err := store.PlanSummary(ids[1], "summarize-from", session.GetState())
	if err != nil {
		t.Fatal(err)
	}
	publishTestSummary(t, store, session, plan)
	hash := store.doc.Summaries[0].Blob
	if err := os.WriteFile(filepath.Join(store.dir, hash+".json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PlanSummary(ids[2], "summarize-from", session.GetState()); err == nil {
		t.Fatal("corrupt original transcript expanded")
	}
	if _, err := store.Summary(plan.ID); err == nil {
		t.Fatal("corrupt transcript read accepted")
	}
}
