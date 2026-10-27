package chat

import (
	"encoding/json"
	"testing"

	"gokin/internal/toolschema"
	"google.golang.org/genai"
)

func TestSessionToolSchemaRoundTripForkAndClear(t *testing.T) {
	s := NewSession()
	s.SetToolSchemaProfile(toolschema.New(true))
	base := []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "read", Description: "original"}}}}
	want, _ := json.Marshal(s.ToolSchemaProfile().Publish(base))
	state := s.GetState()
	restored := NewSession()
	if err := restored.RestoreFromState(state); err != nil {
		t.Fatal(err)
	}
	base[0].FunctionDeclarations[0].Description = "new version"
	got, _ := json.Marshal(restored.ToolSchemaProfile().Publish(base))
	if string(got) != string(want) {
		t.Fatal("resume changed schema bytes")
	}
	branch := s.Fork("fork")
	if branch.ToolSchemaProfile() == s.ToolSchemaProfile() {
		t.Fatal("fork shared mutable publication")
	}
	s.Clear()
	if !s.ToolSchemaProfile().Deferred() || len(s.ToolSchemaProfile().Snapshot().Declarations) != 0 || len(branch.ToolSchemaProfile().Snapshot().Declarations) != 3 {
		t.Fatal("clear/fork isolation failed")
	}
	legacy := NewSession()
	if err := legacy.RestoreFromState(&SessionState{ID: "legacy"}); err != nil || legacy.ToolSchemaProfile().Deferred() {
		t.Fatal("legacy must stay full", err)
	}
	// Validate the entire branch graph before publishing any restored state.
	state.Branches = map[string]*SessionState{"bad": {ToolSchemas: &toolschema.Snapshot{Version: 99}}}
	id := restored.GetID()
	if err := restored.RestoreFromState(state); err == nil || restored.GetID() != id {
		t.Fatal("invalid branch partially restored")
	}
}
