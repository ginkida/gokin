package toolschema

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func encoded(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProfileFreezesNativeSchemasAndNeverLoadsDeferredDeclarations(t *testing.T) {
	p := New(true)
	base := []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "read", Description: "initial"}, {Name: "skill", Description: "catalog one"}}}}
	first := encoded(t, p.Publish(base))
	base[0].FunctionDeclarations[0].Description = "changed"
	base[0].FunctionDeclarations[1].Description = "catalog two"
	base[0].FunctionDeclarations = append(base[0].FunctionDeclarations, &genai.FunctionDeclaration{Name: "new_mcp_tool"})
	if next := encoded(t, p.Publish(base)); !bytes.Equal(first, next) {
		t.Fatalf("publication changed: %s -> %s", first, next)
	}
	if strings.Contains(string(first), "catalog") || !strings.Contains(string(first), "tool_search") || !strings.Contains(string(first), "use_tool") {
		t.Fatal(string(first))
	}
	// Callers cannot mutate the retained snapshot through either API.
	p.Publish(base)[0].FunctionDeclarations[0].Description = "mutated output"
	s := p.Snapshot()
	s.Declarations[0].Description = "mutated snapshot"
	if next := encoded(t, p.Publish(base)); !bytes.Equal(first, next) {
		t.Fatal("mutable publication escaped")
	}
	restored, err := Restore(p.Snapshot())
	if err != nil || !bytes.Equal(first, encoded(t, restored.Publish(base))) {
		t.Fatalf("resume changed bytes: %v", err)
	}
	saved := p.Snapshot()
	for _, d := range saved.Declarations {
		if d.Name == "tool_search" {
			d.Description = "older binary's exact discovery instructions"
		}
	}
	older, err := Restore(saved)
	if err != nil || !strings.Contains(string(encoded(t, older.Publish(base))), "older binary") {
		t.Fatal("saved meta declarations were regenerated", err)
	}
}

func TestProfilePolicyAndLegacyState(t *testing.T) {
	base := []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "read"}, {Name: "write"}}}}
	legacy, err := Restore(nil)
	if err != nil || legacy.Deferred() || !bytes.Equal(encoded(t, base), encoded(t, legacy.Publish(base))) {
		t.Fatal("legacy session changed mode")
	}
	p := New(true)
	p.Configure([]*genai.Tool{{FunctionDeclarations: base[0].FunctionDeclarations[:1]}})
	if strings.Contains(string(encoded(t, p.Publish(base))), `"name":"write"`) {
		t.Fatal("raw publisher bypassed current policy")
	}
	p.Configure(base) // Explicit plan/config policy transitions may invalidate.
	if !strings.Contains(string(encoded(t, p.Publish(base))), `"name":"write"`) {
		t.Fatal("explicit policy update did not apply")
	}
	p.Configure(nil)
	if len(p.Publish(base)) != 0 {
		t.Fatal("empty capability ceiling advertised meta-tools")
	}
}

func TestProfileRejectsInvalidPersistedState(t *testing.T) {
	for _, snapshot := range []*Snapshot{
		{Version: 2},
		{Version: 1, Deferred: true, Declarations: []*genai.FunctionDeclaration{nil}},
		{Version: 1, Deferred: true, Declarations: []*genai.FunctionDeclaration{{Name: "task"}}},
		{Version: 1, Deferred: true, Declarations: []*genai.FunctionDeclaration{{Name: "read"}, {Name: "read"}}},
		{Version: 1, Declarations: []*genai.FunctionDeclaration{{Name: "read"}}},
		{Version: 1, Deferred: true, Declarations: []*genai.FunctionDeclaration{{Name: "read", Description: strings.Repeat("x", maxSnapshotBytes)}}},
	} {
		if _, err := Restore(snapshot); err == nil {
			t.Errorf("accepted invalid snapshot: %.100s", encoded(t, snapshot))
		}
	}
}
