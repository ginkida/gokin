package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/agent"
	"gokin/internal/tasks"
)

type fakeTaskRunner struct {
	summaries []agent.TaskSummary
	results   map[string]*agent.AgentResult
	cancelled []string
}

func (f *fakeTaskRunner) ListTaskSummaries() []agent.TaskSummary { return f.summaries }

func (f *fakeTaskRunner) GetResult(agentID string) (*agent.AgentResult, bool) {
	res, ok := f.results[agentID]
	return res, ok
}
func (f *fakeTaskRunner) Cancel(agentID string) error {
	f.cancelled = append(f.cancelled, agentID)
	return nil
}

type fakeShellRunner struct {
	infos     []tasks.Info
	cancelled []string
}

func (f *fakeShellRunner) List() []tasks.Info { return f.infos }
func (f *fakeShellRunner) Cancel(id string) error {
	f.cancelled = append(f.cancelled, id)
	return nil
}

type fakeAppForTasks struct {
	*fakeAppForMCP
	runner AgentTaskRunner
	shells BackgroundShellRunner
}

func (f *fakeAppForTasks) GetAgentTaskRunner() AgentTaskRunner             { return f.runner }
func (f *fakeAppForTasks) GetBackgroundShellRunner() BackgroundShellRunner { return f.shells }

func newTasksApp(runner AgentTaskRunner) *fakeAppForTasks {
	return &fakeAppForTasks{fakeAppForMCP: &fakeAppForMCP{}, runner: runner}
}

func newTasksAppWithShells(runner AgentTaskRunner, shells BackgroundShellRunner) *fakeAppForTasks {
	return &fakeAppForTasks{fakeAppForMCP: &fakeAppForMCP{}, runner: runner, shells: shells}
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

// TestTasksStopCancelsAgent pins the user-facing background-agent stop:
// previously only the MODEL could stop an agent (task_stop tool) — /tasks was
// deliberately list-only, so a user watching a runaway background task had no
// kill switch short of quitting gokin.
func TestTasksStopCancelsAgent(t *testing.T) {
	runner := &fakeTaskRunner{summaries: []agent.TaskSummary{
		{ID: "agent-12345678", Type: "general", Status: "running", Task: "long work"},
	}}
	cmd := &TasksCommand{}
	out, err := cmd.Execute(context.Background(), []string{"stop", "agent-123"}, newTasksApp(runner))
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.cancelled) != 1 || runner.cancelled[0] != "agent-12345678" {
		t.Fatalf("stop must Cancel the prefix-resolved agent, got %v", runner.cancelled)
	}
	if !strings.Contains(out, "Stopped background task agent-12345678") {
		t.Fatalf("unexpected output: %q", out)
	}
}

// --- Background shell task tests (fix #11) ---
// Previously bash/ssh run_in_background commands were completely invisible
// and unstoppable by the USER — only model tools (kill_shell, task_stop) and
// shutdown's CancelAll could reach tasks.Manager. /tasks now surfaces them.

func TestTasks_ListShowsShellTasks(t *testing.T) {
	shells := &fakeShellRunner{infos: []tasks.Info{
		{ID: "task_1720000000_1", Command: "npm run dev", Status: "running", Duration: 90 * time.Second},
		{ID: "task_1720000000_2", Command: "go test ./...", Status: "completed", Duration: 5 * time.Second},
	}}
	cmd := &TasksCommand{}
	out, err := cmd.Execute(context.Background(), nil, newTasksAppWithShells(nil, shells))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, needle := range []string{
		"Background shell tasks", "task_1720000000_1", "npm run dev",
		"task_1720000000_2", "go test ./...",
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("list output missing %q:\n%s", needle, out)
		}
	}
}

func TestTasks_ListMergesAgentsAndShells(t *testing.T) {
	runner := &fakeTaskRunner{summaries: []agent.TaskSummary{
		{ID: "agent-1", Status: agent.AgentStatusRunning, Task: "explore"},
	}}
	shells := &fakeShellRunner{infos: []tasks.Info{
		{ID: "task_1_1", Command: "sleep 100", Status: "running"},
	}}
	cmd := &TasksCommand{}
	out, err := cmd.Execute(context.Background(), nil, newTasksAppWithShells(runner, shells))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "agent-1") || !strings.Contains(out, "task_1_1") {
		t.Fatalf("merged list must show both agent and shell tasks:\n%s", out)
	}
}

func TestTasksStop_ResolvesShellTaskByPrefix(t *testing.T) {
	shells := &fakeShellRunner{infos: []tasks.Info{
		{ID: "task_1720000000_7", Command: "npm run dev", Status: "running"},
	}}
	cmd := &TasksCommand{}
	out, err := cmd.Execute(context.Background(), []string{"stop", "task_1720000000"}, newTasksAppWithShells(nil, shells))
	if err != nil {
		t.Fatal(err)
	}
	if len(shells.cancelled) != 1 || shells.cancelled[0] != "task_1720000000_7" {
		t.Fatalf("stop must cancel the prefix-resolved shell task, got %v", shells.cancelled)
	}
	if !strings.Contains(out, "Stopped background shell task task_1720000000_7") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestTasksStop_TriesAgentFirstThenShell(t *testing.T) {
	runner := &fakeTaskRunner{summaries: []agent.TaskSummary{
		{ID: "agent-only", Status: agent.AgentStatusRunning},
	}}
	shells := &fakeShellRunner{infos: []tasks.Info{
		{ID: "task_5_1", Command: "sleep 50", Status: "running"},
	}}
	cmd := &TasksCommand{}

	// Resolves as an agent — shell runner untouched.
	if _, err := cmd.Execute(context.Background(), []string{"stop", "agent-only"}, newTasksAppWithShells(runner, shells)); err != nil {
		t.Fatal(err)
	}
	if len(runner.cancelled) != 1 || len(shells.cancelled) != 0 {
		t.Fatalf("agent id must resolve via the agent runner only, agent=%v shell=%v", runner.cancelled, shells.cancelled)
	}

	// Doesn't resolve as an agent — falls through to the shell runner.
	if _, err := cmd.Execute(context.Background(), []string{"stop", "task_5"}, newTasksAppWithShells(runner, shells)); err != nil {
		t.Fatal(err)
	}
	if len(shells.cancelled) != 1 || shells.cancelled[0] != "task_5_1" {
		t.Fatalf("shell-shaped id must fall through to the shell runner, got %v", shells.cancelled)
	}
}

func TestTasksStop_ReportsAmbiguousPrefix(t *testing.T) {
	t.Run("agents", func(t *testing.T) {
		runner := &fakeTaskRunner{summaries: []agent.TaskSummary{
			{ID: "agent-a1", Status: agent.AgentStatusRunning},
			{ID: "agent-a2", Status: agent.AgentStatusRunning},
		}}
		out, err := (&TasksCommand{}).Execute(context.Background(), []string{"stop", "agent-a"}, newTasksApp(runner))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "ambiguous") || !strings.Contains(out, "agent-a1") || !strings.Contains(out, "agent-a2") {
			t.Fatalf("ambiguous agent prefix must list candidates, got %q", out)
		}
		if len(runner.cancelled) != 0 {
			t.Fatalf("ambiguous prefix must not cancel a task, got %v", runner.cancelled)
		}
	})

	t.Run("shells", func(t *testing.T) {
		shells := &fakeShellRunner{infos: []tasks.Info{
			{ID: "task_5_1", Status: "running"},
			{ID: "task_5_2", Status: "running"},
		}}
		out, err := (&TasksCommand{}).Execute(context.Background(), []string{"stop", "task_5"}, newTasksAppWithShells(nil, shells))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "ambiguous") || !strings.Contains(out, "task_5_1") || !strings.Contains(out, "task_5_2") {
			t.Fatalf("ambiguous shell prefix must list candidates, got %q", out)
		}
		if len(shells.cancelled) != 0 {
			t.Fatalf("ambiguous prefix must not cancel a task, got %v", shells.cancelled)
		}
	})
}

func TestTasks_DetailShowsShellTaskOutput(t *testing.T) {
	shells := &fakeShellRunner{infos: []tasks.Info{
		{ID: "task_9_1", Command: "go build ./...", Status: "failed", ExitCode: 1, Output: "build error: undefined foo"},
	}}
	cmd := &TasksCommand{}
	out, err := cmd.Execute(context.Background(), []string{"task_9_1"}, newTasksAppWithShells(nil, shells))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, needle := range []string{"task_9_1", "go build ./...", "Exit code: 1", "build error: undefined foo"} {
		if !strings.Contains(out, needle) {
			t.Fatalf("shell detail missing %q:\n%s", needle, out)
		}
	}
}
