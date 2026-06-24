package chat

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

// TestSessionManager_SaveLoadRestoreRoundTrip pins the persistence mechanism
// that headless `--resume` relies on (v0.100.50): a session saved synchronously
// at the end of one `gokin --headless` turn must be recoverable — with the full
// conversation history — by a FRESH SessionManager in the same WorkDir via
// LoadLast + RestoreFromState. That pair is exactly what App.ResumeLastSession
// runs, and RunHeadless now calls Save() on exit so the next `--resume` turn can
// find it. If this round-trip regresses, scriptable multi-turn headless sessions
// break silently (each turn would start from empty context). The cross-process
// continuity itself was manually verified end-to-end (the "9173" two-turn recall
// and the four-turn coherent build); this test guards the underlying mechanism
// without the full request pipeline.
func TestSessionManager_SaveLoadRestoreRoundTrip(t *testing.T) {
	// Isolate session storage to a temp dir (getDataDir reads XDG_DATA_HOME).
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	workDir := t.TempDir()
	cfg := SessionManagerConfig{Enabled: true, AutoLoad: true}

	// Turn 1: a session carrying conversation history, persisted via the same
	// synchronous Save() that RunHeadless now invokes on exit.
	s1 := NewSession()
	s1.SetWorkDir(workDir)
	s1.AddUserMessage("the secret code is ZEBRA-42")
	s1.AddUserMessage("please keep it in mind")
	sm1, err := NewSessionManager(s1, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager(turn1): %v", err)
	}
	if err := sm1.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A fresh "process": a new SessionManager in the same WorkDir resumes it,
	// exactly as App.ResumeLastSession does (LoadLast → RestoreFromState).
	s2 := NewSession()
	s2.SetWorkDir(workDir)
	sm2, err := NewSessionManager(s2, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager(turn2): %v", err)
	}
	state, info, err := sm2.LoadLast()
	if err != nil {
		t.Fatalf("LoadLast: %v", err)
	}
	if state == nil || len(state.History) == 0 {
		t.Fatal("LoadLast found no prior session — headless --resume would start from empty context")
	}
	if err := sm2.RestoreFromState(state); err != nil {
		t.Fatalf("RestoreFromState: %v", err)
	}

	// The resumed session must carry turn 1's conversation forward intact.
	hist := s2.GetHistory()
	if len(hist) != 2 {
		t.Fatalf("restored history has %d messages, want 2", len(hist))
	}
	if got := historyContains(hist, "ZEBRA-42"); !got {
		t.Errorf("restored history lost the turn-1 content (the secret code)")
	}
	if info != nil && info.MessageCount == 0 {
		t.Errorf("session info reported 0 messages for a 2-message session")
	}
}

// historyContains reports whether any message part contains substr.
func historyContains(hist []*genai.Content, substr string) bool {
	for _, c := range hist {
		if c == nil {
			continue
		}
		for _, p := range c.Parts {
			if p != nil && strings.Contains(p.Text, substr) {
				return true
			}
		}
	}
	return false
}
