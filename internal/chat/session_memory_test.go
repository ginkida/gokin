package chat

import (
	"encoding/json"
	"testing"
)

func TestContextMemoryRoundTripAndFreshBoundaries(t *testing.T) {
	s := NewSession()
	s.SetWorkingMemory("Latest result: added the comment")
	if !s.SetSessionMemory(s.GetID(), "Current task: update m.go") {
		t.Fatal("owning session rejected its summary")
	}
	encoded, err := json.Marshal(s.GetState())
	if err != nil {
		t.Fatal(err)
	}
	var state SessionState
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatal(err)
	}
	resumed := NewSession()
	if err := resumed.RestoreFromState(&state); err != nil {
		t.Fatal(err)
	}
	id, working, summary := resumed.ContextMemory()
	if id != s.GetID() || working != state.WorkingMemory || summary != state.SessionMemory || working == "" || summary == "" {
		t.Fatalf("memory lost during roundtrip: id=%s working=%q summary=%q", id, working, summary)
	}
	if resumed.SetSessionMemory("another-session", "obsolete async summary") {
		t.Fatal("another session's extraction was accepted")
	}
	_, _, got := resumed.ContextMemory()
	if got != summary {
		t.Fatal("rejected async extraction changed the snapshot")
	}
	legacy := NewSession().GetState()
	if err := resumed.RestoreFromState(legacy); err != nil {
		t.Fatal(err)
	}
	_, working, summary = resumed.ContextMemory()
	if working != "" || summary != "" {
		t.Fatal("new or legacy session inherited previously restored memory")
	}
	if err := resumed.RestoreFromState(&state); err != nil {
		t.Fatal(err)
	}
	resumed.Clear()
	_, working, summary = resumed.ContextMemory()
	if working != "" || summary != "" {
		t.Fatal("clear retained session-owned memory")
	}
}
