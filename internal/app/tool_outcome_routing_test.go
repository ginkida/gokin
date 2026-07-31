package app

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"gokin/internal/permission"
	"gokin/internal/router"
)

func TestRecordToolPhaseOutcomeFeedsMetricsAndConversationMode(t *testing.T) {
	taskRouter := &router.Router{}
	application := &App{
		taskRouter:   taskRouter,
		phaseMetrics: NewPhaseMetrics(),
		toolMetrics:  NewToolMetrics(),
	}

	application.recordToolPhaseOutcome("read", 10*time.Millisecond, true)
	if mode := taskRouter.GetConversationMode(); mode != "exploring" {
		t.Fatalf("mode after read=%q, want exploring", mode)
	}
	application.recordToolPhaseOutcome("edit", 20*time.Millisecond, true)
	if mode := taskRouter.GetConversationMode(); mode != "implementing" {
		t.Fatalf("mode after edit=%q, want implementing", mode)
	}
	for range 3 {
		application.recordToolPhaseOutcome("bash", 30*time.Millisecond, false)
	}
	if mode := taskRouter.GetConversationMode(); mode != "debugging" {
		t.Fatalf("mode after repeated bash failures=%q, want debugging", mode)
	}

	phases := application.phaseMetrics.Snapshot()
	if len(phases) != 1 || phases[0].Phase != PhaseTool || phases[0].Count != 5 {
		t.Fatalf("phase metrics=%+v, want five tool samples", phases)
	}
	tools := application.toolMetrics.Snapshot()
	if len(tools) != 3 {
		t.Fatalf("tool metrics=%+v, want read/edit/bash", tools)
	}
}

func TestBeginTurnIntentDistinguishesFreshExploreAndImplementHistory(t *testing.T) {
	taskRouter := &router.Router{}
	application := &App{
		program:     &tea.Program{},
		permManager: permission.NewManager(nil, true),
		taskRouter:  taskRouter,
	}
	ambiguous := "the parser is slow on large inputs"

	application.beginTurnIntent(ambiguous)
	if application.discussGate() {
		t.Fatal("fresh ambiguous task was treated as an existing exploration")
	}

	application.recordToolPhaseOutcome("read", time.Millisecond, true)
	application.beginTurnIntent(ambiguous)
	if !application.discussGate() {
		t.Fatal("ambiguous follow-up after read-only exploration should remain discussion")
	}

	application.recordToolPhaseOutcome("edit", time.Millisecond, true)
	application.beginTurnIntent(ambiguous)
	if application.discussGate() {
		t.Fatal("ambiguous follow-up during implementation should continue acting")
	}
}
