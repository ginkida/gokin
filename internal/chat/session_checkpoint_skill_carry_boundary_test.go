package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestSessionRestoreCheckpointPreservesRawBoundaryAfterSkillCarryInsertion(t *testing.T) {
	session := NewSession()
	v1 := recordSessionSkill(t, session, "review", "review workflow v1")
	call, response := sessionToolPair("skill-review-v1", "skill", map[string]any{
		"success": true,
		"content": v1.Rendered,
		"data": map[string]any{
			"render_hash": v1.RenderHash,
			"changed":     true,
		},
	})
	boundaryMarker := genai.NewContentFromText("checkpoint boundary marker", genai.RoleModel)
	savedRaw := []*genai.Content{call, response, boundaryMarker}

	// The raw v1 response is a complete delivery witness, so the saved history
	// contains no synthetic carry and its checkpoint index is a raw boundary.
	session.SetHistory(savedRaw)
	saved := session.GetHistory()
	if len(saved) != len(savedRaw) || len(sessionSkillCarryContents(saved)) != 0 {
		t.Fatalf("checkpoint fixture unexpectedly contains a carry: %#v", saved)
	}
	for index := range savedRaw {
		if saved[index] != savedRaw[index] {
			t.Fatalf("saved raw message %d was replaced", index)
		}
	}
	session.SaveCheckpoint("v1-boundary")

	v2, changed, err := session.InvocationLedger().Record(
		"review",
		"review workflow v2",
		"project",
		"/workspace/review/SKILL.md",
	)
	if err != nil || !changed {
		t.Fatalf("record v2: changed=%v err=%v", changed, err)
	}

	// The retained response only witnesses v1. Rebuilding for v2 therefore
	// inserts a synthetic message before the raw history and shifts every raw
	// index by one while the saved checkpoint still points at len(savedRaw).
	shifted := session.SetHistoryWithSkillCarry(session.GetHistory(), 0)
	if len(shifted) != len(savedRaw)+1 || len(sessionSkillCarryContents(shifted)) != 1 {
		t.Fatalf("v2 carry did not shift the raw checkpoint boundary: %#v", shifted)
	}
	if text := sessionSkillCarryText(t, shifted); !strings.Contains(text, v2.Rendered) {
		t.Fatalf("shifted carry does not contain v2: %q", text)
	}
	if shifted[len(shifted)-1] != boundaryMarker {
		t.Fatal("boundary marker was lost before checkpoint restore")
	}

	if !session.RestoreCheckpoint("v1-boundary") {
		t.Fatal("RestoreCheckpoint(v1-boundary) failed")
	}
	invocations := session.InvocationLedger().SnapshotNewestFirst()
	if len(invocations) != 1 || invocations[0] != v1 {
		t.Fatalf("checkpoint restored skills = %#v, want only v1", invocations)
	}

	// Restoring must use the raw-message boundary captured at SaveCheckpoint,
	// not the shifted physical slice index. All three original messages,
	// including the boundary marker, must survive exactly.
	restored := session.GetHistory()
	if len(restored) != len(savedRaw) {
		t.Fatalf("restored history len = %d, want %d raw messages", len(restored), len(savedRaw))
	}
	for index := range savedRaw {
		if restored[index] != savedRaw[index] {
			t.Fatalf("restored raw message %d = %#v, want original %#v", index, restored[index], savedRaw[index])
		}
	}
	if carries := sessionSkillCarryContents(restored); len(carries) != 0 {
		t.Fatalf("restored v1 raw witness should suppress carry, got %d", len(carries))
	}
}

func TestSessionCheckpointRawIndexBasisPersistsAcrossResume(t *testing.T) {
	session := NewSession()
	v1 := recordSessionSkill(t, session, "review", "resume review workflow v1")
	session.SetHistory([]*genai.Content{
		genai.NewContentFromText("before checkpoint", genai.RoleUser),
	})
	session.SaveCheckpoint("before-resume")

	stateBytes, err := json.Marshal(session.GetState())
	if err != nil {
		t.Fatalf("marshal session state: %v", err)
	}
	var state SessionState
	if err := json.Unmarshal(stateBytes, &state); err != nil {
		t.Fatalf("unmarshal session state: %v", err)
	}
	if !state.CheckpointRawIndices["before-resume"] {
		t.Fatal("checkpoint raw-index basis was not persisted")
	}

	resumed := NewSession()
	if err := resumed.RestoreFromState(&state); err != nil {
		t.Fatalf("restore session state: %v", err)
	}
	v2, changed, err := resumed.InvocationLedger().Record(
		"review",
		"resume review workflow v2",
		"project",
		"/workspace/review/SKILL.md",
	)
	if err != nil || !changed {
		t.Fatalf("record resumed v2: changed=%v err=%v", changed, err)
	}
	resumed.AddUserMessage("after checkpoint")
	if text := sessionSkillCarryText(t, resumed.GetHistory()); !strings.Contains(text, v2.Rendered) {
		t.Fatalf("resumed carry does not contain v2: %q", text)
	}

	if !resumed.RestoreCheckpoint("before-resume") {
		t.Fatal("RestoreCheckpoint(before-resume) failed after persistence round trip")
	}
	invocations := resumed.InvocationLedger().SnapshotNewestFirst()
	if len(invocations) != 1 || invocations[0] != v1 {
		t.Fatalf("restored invocation = %#v, want v1", invocations)
	}
	history := resumed.GetHistory()
	if len(history) != 2 || history[1].Parts[0].Text != "before checkpoint" {
		t.Fatalf("restored history = %#v, want carry followed by original boundary message", history)
	}
	if text := sessionSkillCarryText(t, history); !strings.Contains(text, v1.Rendered) || strings.Contains(text, v2.Rendered) {
		t.Fatalf("restored carry does not contain only v1: %q", text)
	}
}
