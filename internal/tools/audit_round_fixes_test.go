package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

// --- #6: agent ids never routed to the runner ---

type stubAgentRunner struct {
	known   []string
	results map[string]AgentResult
}

func (s *stubAgentRunner) Spawn(context.Context, string, string, int, string) (string, error) {
	return "", nil
}
func (s *stubAgentRunner) SpawnAsync(context.Context, string, string, int, string) string { return "" }
func (s *stubAgentRunner) SpawnAsyncWithStreaming(context.Context, string, string, int, string, func(string), func(string, *AgentProgress)) string {
	return ""
}
func (s *stubAgentRunner) Resume(context.Context, string, string) (string, error) { return "", nil }
func (s *stubAgentRunner) ResumeAsync(context.Context, string, string) (string, error) {
	return "", nil
}
func (s *stubAgentRunner) GetResult(id string) (AgentResult, bool) {
	r, ok := s.results[id]
	return r, ok
}
func (s *stubAgentRunner) ListAgents() []string { return s.known }

// The routing predicate used to guess the ID SHAPE — it demanded a dash and
// len>20 ("UUIDs"), while real gokin agent ids are 16 hex characters. It was
// therefore unreachable-true, and every agent id fell through to the shell
// task manager: a background agent's output was unreachable via task_output
// and a runaway agent could not be stopped via task_stop.
func TestRunnerOwnsAgentUsesOwnershipNotIDShape(t *testing.T) {
	const runningID = "3f2a9c1b7e4d5068" // real shape: 16 hex, no dash
	const finishedID = "55c85bc1fccd03ba"
	runner := &stubAgentRunner{
		known:   []string{runningID},
		results: map[string]AgentResult{finishedID: {AgentID: finishedID}},
	}

	if !runnerOwnsAgent(runner, runningID) {
		t.Fatal("a RUNNING agent id must route to the runner (it has no result yet)")
	}
	if !runnerOwnsAgent(runner, finishedID) {
		t.Fatal("a finished agent still in the result ledger must route to the runner")
	}
	if runnerOwnsAgent(runner, "task_1775572596_1") {
		t.Fatal("a shell task id must fall through to the shell manager")
	}
	if runnerOwnsAgent(nil, runningID) || runnerOwnsAgent(runner, "") {
		t.Fatal("nil runner / empty id must never claim ownership")
	}
}

// --- #7: background spawn reported success with a blank agent id ---

func TestTaskBackgroundSpawnFailureIsHonest(t *testing.T) {
	tool := NewTaskTool()
	tool.SetRunner(&stubAgentRunner{}) // SpawnAsync returns "" = never started

	res, err := tool.Execute(context.Background(), map[string]any{
		"subagent_type":     "explore",
		"prompt":            "look around",
		"run_in_background": true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Success {
		t.Fatalf("a spawn that never started must not report success: %q", res.Content)
	}
	if !strings.Contains(res.Error+res.Content, "failed to start") {
		t.Fatalf("failure must say the agent did not start, got %q / %q", res.Error, res.Content)
	}
}

// --- #8: update_scratchpad claimed success while discarding the content ---

func TestUpdateScratchpadFailsHonestlyWhenUnwired(t *testing.T) {
	tool := NewUpdateScratchpadTool(nil) // the foreground registry's shape
	res, err := tool.Execute(context.Background(), map[string]any{"content": "notes worth keeping"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Success {
		t.Fatal("storing nowhere must not report 'Scratchpad updated successfully'")
	}

	var stored string
	wired := NewUpdateScratchpadTool(func(c string) { stored = c })
	res, err = wired.Execute(context.Background(), map[string]any{"content": "notes worth keeping"})
	if err != nil || !res.Success {
		t.Fatalf("wired scratchpad must still succeed: %+v (%v)", res, err)
	}
	if stored != "notes worth keeping" {
		t.Fatalf("content not stored, got %q", stored)
	}
}

// --- #5: refactor's `**` pattern matched nothing ---

func TestRefactorGlobExpandsDoublestar(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	nested := filepath.Join(dir, "internal", "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "target.go"), []byte("package pkg\n\nfunc Executor() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewRefactorTool()
	tool.SetWorkDir(dir)

	res, err := tool.Execute(context.Background(), map[string]any{
		"operation":   "find_refs",
		"target_name": "Executor",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("default **/*.go must reach nested files: %s / %s", res.Error, res.Content)
	}
	if !strings.Contains(res.Content, "target.go") {
		t.Fatalf("nested match missing — `**` did not expand: %q", res.Content)
	}

	// A pattern that matches NO files is not the same answer as "no references
	// in the files I searched": it must fail loudly, not report a confident
	// false "unused".
	res, err = tool.Execute(context.Background(), map[string]any{
		"operation":   "find_refs",
		"target_name": "Executor",
		"pattern":     "**/*.rs",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Success {
		t.Fatalf("a zero-file pattern must not read as 'no references found': %q", res.Content)
	}
	if !strings.Contains(res.Error+res.Content, "matched no files") {
		t.Fatalf("zero-match error must say so, got %q / %q", res.Error, res.Content)
	}
}
