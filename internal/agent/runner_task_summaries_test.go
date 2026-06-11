package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/tools"
)

func TestListTaskSummaries_SortsRunningFirstThenRecent(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())

	base := time.Now().Add(-time.Hour)
	runner.mu.Lock()
	runner.agents["done-old"] = &Agent{
		ID: "done-old", Type: "general",
		status: AgentStatusCompleted, startTime: base, endTime: base.Add(2 * time.Minute),
		originalPrompt: "old finished task",
	}
	runner.agents["done-new"] = &Agent{
		ID: "done-new", Type: "explore",
		status: AgentStatusCompleted, startTime: base.Add(30 * time.Minute), endTime: base.Add(31 * time.Minute),
		originalPrompt: "newer finished task",
	}
	runner.agents["running-1"] = &Agent{
		ID: "running-1", Type: "general",
		status: AgentStatusRunning, startTime: base.Add(10 * time.Minute),
		originalPrompt: "still running task\nsecond line ignored",
	}
	runner.results["done-new"] = &AgentResult{
		AgentID: "done-new", Output: "all good", Completed: true, Duration: time.Minute,
	}
	runner.mu.Unlock()

	summaries := runner.ListTaskSummaries()
	if len(summaries) != 3 {
		t.Fatalf("len(summaries) = %d, want 3", len(summaries))
	}
	if summaries[0].ID != "running-1" {
		t.Fatalf("summaries[0] = %s, want running-1 (running first)", summaries[0].ID)
	}
	if summaries[1].ID != "done-new" || summaries[2].ID != "done-old" {
		t.Fatalf("completed order = %s, %s — want done-new, done-old (recent first)", summaries[1].ID, summaries[2].ID)
	}

	running := summaries[0]
	if running.Task != "still running task" {
		t.Fatalf("running.Task = %q, want first line of prompt", running.Task)
	}
	if running.Duration <= 0 {
		t.Fatalf("running.Duration = %v, want live elapsed > 0", running.Duration)
	}

	doneNew := summaries[1]
	if doneNew.Output != "all good" || !doneNew.Completed {
		t.Fatalf("done-new result not merged: %+v", doneNew)
	}
	if doneNew.Duration != time.Minute {
		t.Fatalf("done-new.Duration = %v, want result's 1m to win over end-start", doneNew.Duration)
	}
}

func TestGetTaskPreview_TruncatesRuneSafe(t *testing.T) {
	a := &Agent{originalPrompt: strings.Repeat("я", 100)}
	got := a.GetTaskPreview(10)
	if got != strings.Repeat("я", 10)+"…" {
		t.Fatalf("GetTaskPreview = %q, want 10 runes + ellipsis", got)
	}
	if a.GetTaskPreview(0) != strings.Repeat("я", 100) {
		t.Fatal("maxRunes<=0 must return the full first line")
	}
}
