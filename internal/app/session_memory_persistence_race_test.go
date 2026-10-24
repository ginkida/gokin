package app

import (
	"strings"
	"testing"
	"time"

	"gokin/internal/testkit"

	"google.golang.org/genai"
)

func TestSessionMemoryCallbackAcrossSameIDReset(t *testing.T) {
	for _, preserveGoal := range []bool{false, true} {
		name := "provider"
		if preserveGoal {
			name = "plan"
		}
		t.Run(name, func(t *testing.T) {
			a := newMemoryBoundaryApp(t, testkit.ResolvedTempDir(t))
			ready, release, persisted := make(chan struct{}), make(chan struct{}), make(chan struct{})
			attempting := make(chan struct{})
			a.sessionMemory.SetOnUpdate(func() {
				close(ready)
				<-release
				close(attempting)
				a.persistSessionMemory()
				close(persisted)
			})
			history := []*genai.Content{
				genai.NewContentFromText("Update obsolete.go", genai.RoleUser),
				genai.NewContentFromText("I will update obsolete.go.", genai.RoleModel),
				{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "edit", Args: map[string]any{"file_path": "obsolete.go"}}}}},
				{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "edit", Response: map[string]any{"success": true}}}}},
			}
			go a.sessionMemory.Extract(history, 20_000)
			<-ready
			id := a.session.GetID()
			_, err := a.replaceConversationRoot(func() {
				// Extraction committed before the boundary, but its callback
				// writes after Session.Clear and before the manager is reset.
				close(release)
				<-attempting
				select {
				case <-persisted:
				case <-time.After(100 * time.Millisecond):
					// Correct persistence waits for this boundary's lease.
				}
			}, preserveGoal)
			if err != nil {
				t.Fatal(err)
			}
			<-persisted
			if a.session.GetID() != id {
				t.Fatal("test did not exercise a same-ID boundary")
			}
			if summary := a.session.GetState().SessionMemory; summary != "" {
				t.Errorf("stale callback repopulated cleared session: %q", summary)
			}
			if got := a.turnContextContent(); strings.Contains(got, "obsolete.go") {
				t.Errorf("reset restored stale callback content: %s", got)
			}
		})
	}
}

func TestSessionMemoryClearPersistsEmptySnapshot(t *testing.T) {
	a := newMemoryBoundaryApp(t, testkit.ResolvedTempDir(t))
	a.session.AddUserMessage("Investigate the task")
	id := a.session.GetID()
	a.sessionMemory.RestoreForSession(id, "obsolete summary")
	a.persistSessionMemory()
	if err := a.sessionManager.Save(); err != nil {
		t.Fatal(err)
	}
	a.sessionMemory.Clear()
	if err := a.sessionManager.Save(); err != nil {
		t.Fatal(err)
	}
	resumed := newMemoryBoundaryApp(t, a.workDir)
	if err := resumed.ResumeSession(id); err != nil {
		t.Fatal(err)
	}
	if got := resumed.session.GetState().SessionMemory; got != "" {
		t.Fatalf("resume resurrected cleared summary: %q", got)
	}
	if got := resumed.sessionMemory.GetContent(); got != "" {
		t.Fatalf("resume injected cleared summary: %q", got)
	}
}
