package rewind

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gokin/internal/chat"
	"google.golang.org/genai"
)

func compactText(text string) chat.SerializedContent {
	return chat.SerializeContent(genai.NewContentFromText(text, genai.RoleUser))
}

func TestCompactionMappingPreservesReorderedMessagesAndCheckpointBoundaries(t *testing.T) {
	store, session, ids, work, root := summaryFixture(t)
	original := session.GetState().History
	// Scoring may retain a message from the middle before the new summary.
	after := []chat.SerializedContent{original[0], original[3], compactText("summary"), original[4], original[5]}
	compacted, err := store.ArchiveCompaction(original, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(compacted) != len(after) || !reflect.DeepEqual(compacted[0], original[0]) || !reflect.DeepEqual(compacted[3:], original[4:]) {
		t.Fatal("archive changed unaffected messages or consumed a message slot")
	}
	state := session.GetState()
	state.History = compacted
	for _, selected := range []int{1, 2} {
		for _, mode := range []string{"summarize-from", "summarize-before"} {
			plan, err := store.PlanSummary(ids[selected], mode, state)
			if err != nil {
				t.Fatal(err)
			}
			want := original[selected*2:]
			if mode == "summarize-before" {
				want = original[:selected*2]
			}
			if !reflect.DeepEqual(plan.Messages, want) {
				t.Fatalf("wrong original interval after scoring: %s %d", mode, selected)
			}
		}
	}
	restarted, err := Open(root, session.GetID(), work)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.PlanSummary(ids[1], "summarize-from", state); err != nil {
		t.Fatal("restart lost mapping", err)
	}
	if _, err := store.Fork("compacted-branch"); err != nil {
		t.Fatal(err)
	}
	fork, err := Open(root, "compacted-branch", work)
	if err != nil {
		t.Fatal(err)
	}
	state.ID = "compacted-branch"
	if _, err := fork.PlanSummary(ids[1], "summarize-from", state); err != nil {
		t.Fatal("fork lost mapping", err)
	}
}

func TestRepeatedCompactionCanChangePartOfOlderReplacement(t *testing.T) {
	store, session, ids, _, _ := summaryFixture(t)
	original := session.GetState().History
	compacted, err := store.ArchiveCompaction(original, []chat.SerializedContent{compactText("first summary"), original[2], original[4], original[5]})
	if err != nil {
		t.Fatal(err)
	}
	// Touch only a retained message inside the replacement, as output hygiene
	// would. The new archive must encompass the complete older replacement.
	next := append([]chat.SerializedContent(nil), compacted...)
	next[1] = compactText("shorter preserved output")
	next, err = store.ArchiveCompaction(compacted, next)
	if err != nil {
		t.Fatal(err)
	}
	state := session.GetState()
	state.History = next
	plan, err := store.PlanSummary(ids[1], "summarize-before", state)
	if err != nil || !reflect.DeepEqual(plan.Messages, original[:2]) {
		t.Fatal("partial rewrite lost original lineage", err)
	}
	if err := session.RestoreFromState(state); err != nil {
		t.Fatal(err)
	}
	publishTestSummary(t, store, session, plan)
	plan, err = store.PlanSummary(ids[2], "summarize-from", session.GetState())
	if err != nil || !reflect.DeepEqual(plan.Messages, original[4:]) {
		t.Fatal("targeted summary could not follow automatic compaction", err)
	}
}

func TestCompactionDeletionUsesNoExtraMessageSlot(t *testing.T) {
	store, _, _, _, _ := summaryFixture(t)
	before := make([]chat.SerializedContent, chat.MaxMessages+1)
	for i := range before {
		before[i] = compactText(string(rune('A' + i)))
	}
	after, err := store.ArchiveCompaction(before, before[1:])
	if err != nil || len(after) != chat.MaxMessages {
		t.Fatal("window compaction grew the message count", len(after), err)
	}
	store.mu.Lock()
	blocks, err := store.transcriptBlocks(after)
	store.mu.Unlock()
	if err != nil || !reflect.DeepEqual(flattenBlocks(blocks), before) {
		t.Fatal("window compaction lost dropped original", err)
	}
}

func TestCompactionChangedOrCorruptReplacementFailsClosed(t *testing.T) {
	store, session, ids, _, _ := summaryFixture(t)
	original := session.GetState().History
	compacted, err := store.ArchiveCompaction(original, []chat.SerializedContent{compactText("summary"), original[2], original[4], original[5]})
	if err != nil {
		t.Fatal(err)
	}
	state := session.GetState()
	state.History = append([]chat.SerializedContent(nil), compacted...)
	state.History[1] = compactText("unrecorded change")
	if _, err := store.PlanSummary(ids[1], "summarize-from", state); err == nil {
		t.Fatal("partial replacement accepted")
	}
	state.History = compacted
	ref := store.doc.Summaries[0]
	if err := os.WriteFile(filepath.Join(store.dir, ref.Blob+".json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PlanSummary(ids[1], "summarize-from", state); err == nil {
		t.Fatal("corrupt archive accepted")
	}
}

func TestRepeatedCompactionFlattensArchiveLineage(t *testing.T) {
	store, session, ids, _, _ := summaryFixture(t)
	original := session.GetState().History
	current := original
	for i := 0; i < 40; i++ {
		next := []chat.SerializedContent{compactText("latest summary"), original[4], original[5]}
		var err error
		current, err = store.ArchiveCompaction(current, next)
		if err != nil {
			t.Fatal("repeated compaction exhausted nesting depth", i, err)
		}
	}
	state := session.GetState()
	state.History = current
	plan, err := store.PlanSummary(ids[1], "summarize-from", state)
	if err != nil || !reflect.DeepEqual(plan.Messages, original[2:]) {
		t.Fatal("repeated compaction lost original messages", err)
	}
}
