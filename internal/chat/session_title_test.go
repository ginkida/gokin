package chat

import "testing"

func TestSessionTitleRoundTripsAndFollowsConversationOwnership(t *testing.T) {
	s := NewSession()
	s.SetTitle("  Review\n migration  ")
	state := s.GetState()
	if state.Title != "Review migration" || state.Summary != state.Title {
		t.Fatal("title not used in session preview", state)
	}
	restored := NewSession()
	if err := restored.RestoreFromState(state); err != nil || restored.GetTitle() != state.Title {
		t.Fatal("title lost on restore", err)
	}
	if restored.Fork("branch").GetTitle() != state.Title {
		t.Fatal("fork lost title")
	}
	restored.Clear()
	if restored.GetTitle() != "" {
		t.Fatal("clear retained old title")
	}
}
