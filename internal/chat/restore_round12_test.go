package chat

import "testing"

// TestRestoreFromState_ClearsStaleCheckpoints pins the round-12 fix: restoring
// a state that has NO checkpoints/branches/tool-checkpoints must CLEAR any the
// session already holds. `/resume <id>` restores into the current in-memory
// session; before the fix the `if len(...) > 0` guards left a prior session's
// checkpoints in place, so a later `/restore <name>` jumped to a history index
// that belonged to the OLD session.
func TestRestoreFromState_ClearsStaleCheckpoints(t *testing.T) {
	s := NewSession()
	s.Checkpoints = map[string]int{"stale": 5}
	s.Branches = map[string]*Session{"old-branch": NewSession()}
	s.toolCheckpoints = []SerializedToolCheckpoint{{}}

	// Restore a state with none of these.
	state := &SessionState{ID: "fresh"}
	if err := s.RestoreFromState(state); err != nil {
		t.Fatalf("RestoreFromState: %v", err)
	}

	if len(s.Checkpoints) != 0 {
		t.Errorf("stale checkpoints survived restore: %v", s.Checkpoints)
	}
	if len(s.Branches) != 0 {
		t.Errorf("stale branches survived restore: %v", s.Branches)
	}
	if len(s.toolCheckpoints) != 0 {
		t.Errorf("stale tool checkpoints survived restore: %d", len(s.toolCheckpoints))
	}
}

// TestRestoreFromState_KeepsRestoredCheckpoints guards the positive case: a
// state that DOES carry checkpoints restores them.
func TestRestoreFromState_KeepsRestoredCheckpoints(t *testing.T) {
	s := NewSession()
	state := &SessionState{
		ID:          "with-cp",
		Checkpoints: map[string]int{"good": 2},
	}
	if err := s.RestoreFromState(state); err != nil {
		t.Fatalf("RestoreFromState: %v", err)
	}
	if s.Checkpoints["good"] != 2 {
		t.Errorf("restored checkpoint missing: %v", s.Checkpoints)
	}
}
