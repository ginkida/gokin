package rewind

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"gokin/internal/chat"
	"google.golang.org/genai"
)

func TestPendingWindowsCrossExistingCompactedSpanAndImportIdempotently(t *testing.T) {
	store, session, ids, work, root := summaryFixture(t)
	original := session.GetState().History
	// Keep several messages inside one compacted span. A slide through that
	// span must consume the complete older replacement without duplicating it.
	compacted, err := store.ArchiveCompaction(original, []chat.SerializedContent{original[3], compactText("summary"), original[2], original[4], original[5]})
	if err != nil {
		t.Fatal(err)
	}
	state := session.GetState()
	state.History = compacted
	if err := session.RestoreFromState(state); err != nil {
		t.Fatal(err)
	}
	session.SetHistoryWindowHandler(func(string) {})
	for i := 0; i < chat.MaxMessages+4; i++ {
		message := genai.NewContentFromText(fmt.Sprintf("later-%d", i), genai.RoleUser)
		session.AddContent(message)
		original = append(original, chat.SerializeContent(message))
	}
	_, pending := session.PendingHistoryWindows()
	if len(pending) < 3 {
		t.Fatal("fixture did not cross the older compacted span")
	}
	if err := store.ImportHistoryWindows(pending); err != nil {
		t.Fatal(err)
	}
	count := len(store.doc.Summaries)
	if err := store.ImportHistoryWindows(pending); err != nil || len(store.doc.Summaries) != count {
		t.Fatal("replayed handoff duplicated or rejected durable archives", err)
	}
	state = session.GetState()
	plan, err := store.PlanSummary(ids[1], "summarize-from", state)
	if err != nil || !reflect.DeepEqual(plan.Messages, original[2:]) {
		t.Fatal("pending windows lost the original selected interval", err)
	}
	restarted, err := Open(root, session.GetID(), work)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Fork("window-fork"); err != nil {
		t.Fatal(err)
	}
	fork, err := Open(root, "window-fork", work)
	if err != nil {
		t.Fatal(err)
	}
	state.ID = "window-fork"
	plan, err = fork.PlanSummary(ids[1], "summarize-before", state)
	if err != nil || !reflect.DeepEqual(plan.Messages, original[:2]) {
		t.Fatal("restart/fork lost pending-window source messages", err)
	}
}

func TestWindowArchivesPreserveLargeJSONNumbersAcrossReopen(t *testing.T) {
	store, session, _, work, root := fixture(t)
	point := begin(t, store, session, "request")
	call := &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
		ID: "large-id", Name: "read", Args: map[string]any{"record_id": json.Number("9007199254740993")},
	}}}}
	result := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "large-id", Name: "read", Response: map[string]any{"id": json.Number("9007199254740993")}}}}}
	session.AddContent(call)
	session.AddContent(result)
	session.SetHistoryWindowHandler(func(string) {})
	for i := 0; i < chat.MaxMessages; i++ {
		session.AddUserMessage(fmt.Sprintf("later-%d", i))
	}
	_, pending := session.PendingHistoryWindows()
	if err := store.ImportHistoryWindows(pending); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, session.GetID(), work)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reopened.PlanSummary(point, "summarize-from", session.GetState())
	if err != nil {
		t.Fatal(err)
	}
	if number := plan.Messages[1].Parts[0].FunctionCall.Args["record_id"]; number != json.Number("9007199254740993") {
		t.Fatal("archive rounded a tool identifier", number)
	}
}
