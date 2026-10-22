package chat

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestHistoryWindowHandoffSurvivesSessionSaveAndOwnsItsPayload(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := NewSession()
	callbacks := 0
	session.SetHistoryWindowHandler(func(id string) {
		// The callback may query/save state: it must not run under Session.mu.
		if session.GetState().ID != id {
			t.Fatal("callback received a different identity")
		}
		callbacks++
	})
	for i := 0; i < MaxMessages+2; i++ {
		session.AddUserMessage(fmt.Sprintf("original-%03d", i))
	}
	id, windows := session.PendingHistoryWindows()
	if callbacks != 2 || len(windows) != 2 || session.MessageCount() != MaxMessages {
		t.Fatal("window handoff or hard limit missing", callbacks, len(windows), session.MessageCount())
	}
	before := session.GetState()
	windows[0].Before[0].Parts[0].Text = "external mutation"
	if got := session.GetState(); got.HistoryWindows[0].Before[0].Parts[0].Text == "external mutation" {
		t.Fatal("pending originals alias caller-owned state")
	}
	history, err := NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := history.SaveFull(session); err != nil {
		t.Fatal(err)
	}
	state, err := history.LoadFull(id)
	if err != nil {
		t.Fatal(err)
	}
	restored := NewSession()
	if err := restored.RestoreFromState(state); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.GetState().HistoryWindows, before.HistoryWindows) {
		t.Fatal("restart lost pending originals")
	}
	_, owned := session.PendingHistoryWindows()
	session.AcknowledgeHistoryWindows("different-session", owned)
	if _, stillPending := session.PendingHistoryWindows(); len(stillPending) != 2 {
		t.Fatal("stale callback acknowledged another conversation")
	}
	session.AcknowledgeHistoryWindows(id, owned[:1])
	if _, pending := session.PendingHistoryWindows(); len(pending) != 1 || pending[0].ID != owned[1].ID {
		t.Fatal("acknowledgement removed an untransferred window")
	}
	session.Clear()
	if _, pending := session.PendingHistoryWindows(); len(pending) != 0 {
		t.Fatal("new conversation kept previous window ownership")
	}
}

func TestHistoryWindowVersionedPublicationPreservesNewTurnAndSkillCarry(t *testing.T) {
	session := NewSession()
	session.SetHistoryWindowHandler(func(string) {})
	recordSessionSkill(t, session, "guard", "Keep the active guard instructions")
	history := make([]*genai.Content, MaxMessages)
	for i := range history {
		history[i] = genai.NewContentFromText(fmt.Sprintf("retained-%d", i), genai.RoleUser)
	}
	version := session.GetVersion()
	if _, ok := session.SetHistoryIfVersionWithSkillCarry(history, version+1, 0); ok {
		t.Fatal("stale history was published")
	}
	if _, pending := session.PendingHistoryWindows(); len(pending) != 0 {
		t.Fatal("stale publication queued a window")
	}
	if _, ok := session.SetHistoryIfVersionWithSkillCarry(history, version, 0); !ok {
		t.Fatal("current history was rejected")
	}
	_, pending := session.PendingHistoryWindows()
	if len(pending) != 1 || len(pending[0].After) != MaxMessages-1 || session.MessageCount() != MaxMessages {
		t.Fatal("carry slot was not reserved in the archived window")
	}
	if !strings.Contains(sessionSkillCarryText(t, session.GetHistory()), "active guard") {
		t.Fatal("window dropped active skill instructions")
	}
	session.AddContentWithTokens(genai.NewContentFromText("newest turn", genai.RoleUser), 7)
	counts := session.GetTokenCounts()
	if len(counts) != session.MessageCount() || counts[len(counts)-1] != 7 {
		t.Fatal("window archive broke token alignment")
	}
}

func TestHistoryWindowCapturePreservesOrphanedBoundaryOriginals(t *testing.T) {
	session := NewSession()
	session.SetHistoryWindowHandler(func(string) {})
	call, result := sessionToolPair("cut-pair", "read", map[string]any{"content": "boundary data"})
	history := []*genai.Content{call, result}
	for i := 0; i < MaxMessages; i++ {
		history = append(history, genai.NewContentFromText(fmt.Sprintf("later-%d", i), genai.RoleUser))
	}
	session.SetHistory(history)
	_, pending := session.PendingHistoryWindows()
	if len(pending) != 1 || pending[0].Before[0].Parts[0].FunctionCall.ID != "cut-pair" || pending[0].Before[1].Parts[0].FunctionResp.ID != "cut-pair" {
		t.Fatal("trim discarded the original tool pair")
	}
}
