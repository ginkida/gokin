package app

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/testkit"

	"google.golang.org/genai"
)

func TestRewindAfterRepeatedHardHistoryWindows(t *testing.T) {
	a := newRewindApp(t)
	a.session.SetHistoryWindowHandler(a.flushHistoryWindowArchives)
	mock := testkit.NewMockClient()
	for i := 0; i < 16; i++ {
		mock.EnqueueText("Selected technical decisions and unfinished work.")
	}
	a.client = mock
	var original []chat.SerializedContent
	for i := 0; i < 65; i++ {
		prompt := fmt.Sprintf("request-%02d", i)
		answer := strings.Repeat(fmt.Sprintf("original details for request-%02d. ", i), 100)
		a.captureTurnRewind(prompt)
		a.session.AddUserMessage(prompt)
		a.session.AddModelMessage(answer)
		original = append(original, chat.SerializeContent(genai.NewContentFromText(prompt, genai.RoleUser)), chat.SerializeContent(genai.NewContentFromText(answer, genai.RoleModel)))
		if a.session.MessageCount() > chat.MaxMessages {
			t.Fatal("history window exceeded its hard bound")
		}
	}
	if _, pending := a.session.PendingHistoryWindows(); len(pending) != 0 {
		t.Fatal("healthy archive handoff left originals queued")
	}
	points, err := a.ListRewindPoints(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, point := range []int{0, 12, 52, 64} {
		plan, err := a.rewindStore.PlanSummary(points[point].ID, "summarize-from", a.session.GetState())
		if err != nil || !reflect.DeepEqual(plan.Messages, original[point*2:]) {
			t.Fatalf("window moved original point %d: %v", point, err)
		}
	}
	if err := a.sessionManager.Save(); err != nil {
		t.Fatal(err)
	}
	a.resetRewindOwner()
	if _, err := a.SummarizeAtPoint(t.Context(), points[12].ID, "summarize-from", "Keep original details"); err != nil {
		t.Fatal("selected summary failed after repeated windows and journal reopen", err)
	}
	if _, err := a.RewindToPoint(t.Context(), points[12].ID, "conversation"); err != nil {
		t.Fatal("original conversation checkpoint could not be restored", err)
	}
}

func TestHistoryWindowFailureKeepsDurablePendingOriginalsAndBlocksNextModel(t *testing.T) {
	a := newRewindApp(t)
	a.client = testkit.NewMockClient()
	a.session.SetHistoryWindowHandler(a.flushHistoryWindowArchives)
	a.captureTurnRewind("initial")
	root, err := chat.RewindStorageDir()
	if err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(root, a.session.GetID(), "index.json")
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(index, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < chat.MaxMessages+1; i++ {
		a.session.AddUserMessage(fmt.Sprintf("original-%d", i))
	}
	id, pending := a.session.PendingHistoryWindows()
	if len(pending) != 1 || pending[0].Before[0].Parts[0].Text != "original-0" {
		t.Fatal("archive failure lost the trimmed originals")
	}
	if err := a.sessionManager.Save(); err != nil {
		t.Fatal("pending originals could not be saved for restart", err)
	}
	a.processMessageWithContext(t.Context(), "new work must wait")
	if len(a.client.(*testkit.MockClient).Calls()) != 0 {
		t.Fatal("new model work ran with an unresolved archive failure")
	}
	// Restart with only the session snapshot and the older journal. The retry
	// imports pending originals before any new work and acknowledges afterwards.
	history, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := history.LoadFull(id)
	if err != nil || len(saved.HistoryWindows) != 1 {
		t.Fatal("restart snapshot lost pending originals", err)
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := a.session.RestoreFromState(saved); err != nil {
		t.Fatal(err)
	}
	if err := a.ensureHistoryWindowsReady(); err != nil {
		t.Fatal("repaired archive did not drain pending originals", err)
	}
	if _, pending := a.session.PendingHistoryWindows(); len(pending) != 0 {
		t.Fatal("durable originals were not acknowledged")
	}
	text, err := a.ReadSummaryResource(t.Context(), saved.HistoryWindows[0].ID)
	if err != nil || !strings.Contains(text, "original-0") {
		t.Fatal("recovered originals were not model-readable", err)
	}
}

func TestRewindAfterSoftHistoryGovernance(t *testing.T) {
	a := newRewindApp(t)
	a.session.SetHistoryWindowHandler(a.flushHistoryWindowArchives)
	var original []chat.SerializedContent
	for i := 0; i < 46; i++ {
		prompt := fmt.Sprintf("request-%d", i)
		a.captureTurnRewind(prompt)
		a.session.AddUserMessage(prompt)
		a.session.AddModelMessage("original response")
		original = append(original, chat.SerializeContent(genai.NewContentFromText(prompt, genai.RoleUser)), chat.SerializeContent(genai.NewContentFromText("original response", genai.RoleModel)))
	}
	a.enforceSessionMemoryGovernance("test")
	if a.session.MessageCount() != sessionGovernanceKeepTail {
		t.Fatal("governance did not retain its configured tail")
	}
	points, err := a.ListRewindPoints(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := a.rewindStore.PlanSummary(points[9].ID, "summarize-before", a.session.GetState())
	if err != nil || !reflect.DeepEqual(plan.Messages, original[:18]) {
		t.Fatal("soft-window governance broke rewind boundaries", err)
	}
}
