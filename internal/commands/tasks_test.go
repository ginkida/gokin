package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/agent"
)

type fakeTaskRunner struct {
	summaries []agent.TaskSummary
	results   map[string]*agent.AgentResult
}

func (f *fakeTaskRunner) ListTaskSummaries() []agent.TaskSummary { return f.summaries }

func (f *fakeTaskRunner) GetResult(agentID string) (*agent.AgentResult, bool) {
	res, ok := f.results[agentID]
	return res, ok
}

type fakeAppForTasks struct {
	*fakeAppForMCP
	runner AgentTaskRunner
}

func (f *fakeAppForTasks) GetAgentTaskRunner() AgentTaskRunner { return f.runner }

func newTasksApp(runner AgentTaskRunner) *fakeAppForTasks {
	return &fakeAppForTasks{fakeAppForMCP: &fakeAppForMCP{}, runner: runner}
}

func TestTasks_NilRunnerReportsUnavailable(t *testing.T) {
	cmd := &TasksCommand{}
	out, err := cmd.Execute(context.Background(), nil, newTasksApp(nil))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "unavailable") {
		t.Fatalf("output = %q, want unavailable message", out)
	}
}

func TestTasks_EmptyListShowsHint(t *testing.T) {
	cmd := &TasksCommand{}
	out, err := cmd.Execute(context.Background(), nil, newTasksApp(&fakeTaskRunner{}))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "No background tasks") {
		t.Fatalf("output = %q, want empty-state hint", out)
	}
}

func TestTasks_ListShowsRunningAndCompleted(t *testing.T) {
	runner := &fakeTaskRunner{
		summaries: []agent.TaskSummary{
			{ID: "agent-run-1", Type: "explore", Status: agent.AgentStatusRunning, Task: "map the auth flow", Duration: 42 * time.Second},
			{ID: "agent-done-2", Type: "general", Status: agent.AgentStatusCompleted, Task: "fix the login bug", Duration: 3 * time.Minute, Completed: true},
			{ID: "agent-fail-3", Type: "general", Status: agent.AgentStatusFailed, Task: "refactor storage", Error: "max turns exceeded\nsecond line", Completed: true},
		},
	}

	cmd := &TasksCommand{}
	out, err := cmd.Execute(context.Background(), nil, newTasksApp(runner))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, needle := range []string{
		"agent-run-1", "map the auth flow", "42s",
		"agent-done-2", "fix the login bug",
		"agent-fail-3", "error: max turns exceeded",
		"/tasks <id>",
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("list output missing %q:\n%s", needle, out)
		}
	}
	if strings.Contains(out, "second line") {
		t.Fatalf("list error must be first-line only:\n%s", out)
	}
}

func TestTasks_DetailShowsOutputTailAndFile(t *testing.T) {
	long := strings.Repeat("x", 4000) + "CONCLUSION"
	runner := &fakeTaskRunner{
		summaries: []agent.TaskSummary{
			{ID: "agent-done-2", Type: "general", Status: agent.AgentStatusCompleted, Task: "fix the login bug", Completed: true},
		},
		results: map[string]*agent.AgentResult{
			"agent-done-2": {AgentID: "agent-done-2", Output: long, OutputFile: "/tmp/agent-done-2.out", Completed: true},
		},
	}

	cmd := &TasksCommand{}
	out, err := cmd.Execute(context.Background(), []string{"agent-done-2"}, newTasksApp(runner))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "CONCLUSION") {
		t.Fatalf("detail must keep the output TAIL:\n%.200s", out)
	}
	if !strings.Contains(out, "/tmp/agent-done-2.out") {
		t.Fatalf("detail missing output file pointer:\n%s", out)
	}
	if strings.Contains(out, strings.Repeat("x", 3500)) {
		t.Fatal("detail output was not tail-capped")
	}
}

func TestTasks_DetailPrefixMatch(t *testing.T) {
	runner := &fakeTaskRunner{
		summaries: []agent.TaskSummary{
			{ID: "agent-abc-123", Status: agent.AgentStatusRunning, Task: "one"},
			{ID: "agent-xyz-456", Status: agent.AgentStatusCompleted, Task: "two", Completed: true},
		},
	}

	cmd := &TasksCommand{}
	out, err := cmd.Execute(context.Background(), []string{"agent-abc"}, newTasksApp(runner))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "agent-abc-123") || !strings.Contains(out, "Still running") {
		t.Fatalf("prefix match failed:\n%s", out)
	}
}

func TestTasks_DetailAmbiguousAndMissingPrefix(t *testing.T) {
	runner := &fakeTaskRunner{
		summaries: []agent.TaskSummary{
			{ID: "agent-a1", Status: agent.AgentStatusCompleted},
			{ID: "agent-a2", Status: agent.AgentStatusCompleted},
		},
	}

	cmd := &TasksCommand{}
	if _, err := cmd.Execute(context.Background(), []string{"agent-a"}, newTasksApp(runner)); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous prefix error = %v, want ambiguous", err)
	}
	if _, err := cmd.Execute(context.Background(), []string{"nope"}, newTasksApp(runner)); err == nil || !strings.Contains(err.Error(), "no background task") {
		t.Fatalf("missing id error = %v, want no-match", err)
	}
}
