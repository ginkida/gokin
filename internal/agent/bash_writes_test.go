package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gokin/internal/donegate"
	"gokin/internal/gitexec"
	"gokin/internal/tasks"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"google.golang.org/genai"
)

func TestAgentAttributesBashWrites(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	bash := tools.NewBashTool(root)
	bash.SetSandboxEnabled(false)
	registry := tools.NewRegistry()
	registry.MustRegister(bash)
	a := NewAgent(AgentTypeGeneral, nil, registry, root, 5, "", nil, nil)
	a.reflector = nil
	a.client = testkit.NewMockClient().EnqueueToolCall("bash", map[string]any{"command": "echo x > child.go"}).EnqueueText("Created child.go.")
	result, err := a.Run(tools.ContextWithoutSelfReview(t.Context()), "Create child.go")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"child.go"}
	if !reflect.DeepEqual(result.TouchedPaths, want) || !reflect.DeepEqual(result.FilesChanged, want) || !reflect.DeepEqual(a.GetTouchedPaths(), want) {
		t.Fatalf("result touched=%v changed=%v gate=%v", result.TouchedPaths, result.FilesChanged, a.GetTouchedPaths())
	}
}

func TestAgentPartialObservationRunsFullProjectChecks(t *testing.T) {
	for _, paths := range [][]string{nil, {"good/good.go"}} {
		t.Run(map[bool]string{true: "no targets", false: "known subset"}[len(paths) == 0], func(t *testing.T) {
			root := testkit.ResolvedTempDir(t)
			for path, content := range map[string]string{"go.mod": "module example\n\ngo 1.25\n", "good/good.go": "package good\n", "bad/bad.go": "syntax error\n"} {
				full := filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			bash := tools.NewBashTool(root)
			bash.SetSandboxEnabled(false)
			registry := tools.NewRegistry()
			registry.MustRegister(bash)
			a := NewAgent(AgentTypeGeneral, nil, registry, root, 5, "", nil, nil)
			a.SetDoneGatePolicy(donegate.Policy{Enabled: true, Mode: "strict", CheckTimeout: 5 * time.Second})
			a.recordToolExecution("bash", nil, tools.ToolResult{Success: true, Data: map[string]any{"workspace_observed": true, "files_changed_partial": true, "written_paths": paths}})
			var output strings.Builder
			fixes, checked := 0, -1
			outcome, err := a.runDoneGateAtBreak(t.Context(), "Do it", &output, &fixes, &checked)
			if err != nil || outcome != agentGateExhausted || !strings.Contains(output.String(), "verification checks are still failing") {
				t.Fatalf("partial observation skipped full checks: outcome=%v output=%s err=%v", outcome, output.String(), err)
			}
		})
	}
}

func TestOverlappingAgentsBothRunDoneGate(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n\ngo 1.25\n"), 0600); err != nil {
		t.Fatal(err)
	}
	newAgent := func() *Agent {
		bash := tools.NewBashTool(root)
		bash.SetSandboxEnabled(false)
		registry := tools.NewRegistry()
		registry.MustRegister(bash)
		a := NewAgent(AgentTypeGeneral, nil, registry, root, 5, "", nil, nil)
		a.SetDoneGatePolicy(donegate.Policy{Enabled: true, Mode: "strict", CheckTimeout: 5 * time.Second})
		return a
	}
	first, second := newAgent(), newAgent()
	started, release := make(chan struct{}), make(chan struct{})
	completed := make(chan tools.ToolResult, 1)
	go func() {
		r, _ := first.workspaceWrites.Observe(t.Context(), root, "bash", nil, func(context.Context) (tools.ToolResult, error) {
			close(started)
			<-release
			err := os.WriteFile(filepath.Join(root, "first.go"), []byte("invalid\n"), 0600)
			return tools.NewSuccessResult("first"), err
		}, nil)
		completed <- r
	}()
	<-started
	r, _ := second.workspaceWrites.Observe(t.Context(), root, "bash", nil, func(context.Context) (tools.ToolResult, error) {
		err := os.WriteFile(filepath.Join(root, "second.go"), []byte("invalid\n"), 0600)
		return tools.NewSuccessResult("second"), err
	}, nil)
	close(release)
	first.recordToolExecution("bash", nil, <-completed)
	second.recordToolExecution("bash", nil, r)
	for _, a := range []*Agent{first, second} {
		var output strings.Builder
		fixes, checked := 0, -1
		outcome, err := a.runDoneGateAtBreak(t.Context(), "Do it", &output, &fixes, &checked)
		if err != nil || outcome != agentGateExhausted {
			t.Fatalf("overlapping agent skipped verification: outcome=%v output=%s err=%v", outcome, output.String(), err)
		}
	}
}

func TestAgentBashWritesArmDoneGateWithoutCodingPrompt(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n\ngo 1.25\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bash := tools.NewBashTool(root)
	bash.SetSandboxEnabled(false)
	registry := tools.NewRegistry()
	registry.MustRegister(bash)
	a := NewAgent(AgentTypeGeneral, nil, registry, root, 5, "", nil, nil)
	a.reflector = nil
	a.SetDoneGatePolicy(donegate.Policy{Enabled: true, Mode: "strict", CheckTimeout: 5 * time.Second})
	a.client = testkit.NewMockClient().EnqueueToolCall("bash", map[string]any{"command": "echo x > child.go"}).EnqueueText("Created child.go.")
	result, err := a.Run(tools.ContextWithoutSelfReview(t.Context()), "Do it")
	if err != nil || !strings.Contains(result.Output, "[done-gate] verification checks are still failing") || !reflect.DeepEqual(result.TouchedPaths, []string{"child.go"}) {
		t.Fatalf("generated code bypassed sub-agent gate: %+v %v", result, err)
	}
}

func TestAgentBackgroundBashWritesReachLedger(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	manager := tasks.NewManager(root)
	t.Cleanup(manager.CancelAll)
	bash := tools.NewBashTool(root)
	bash.SetSandboxEnabled(false)
	bash.SetTaskManager(manager)
	registry := tools.NewRegistry()
	registry.MustRegister(bash)
	a := NewAgent(AgentTypeGeneral, nil, registry, root, 5, "", nil, nil)
	a.reflector = nil
	r := a.executeNestedToolResultSafely(t.Context(), &genai.FunctionCall{ID: "bg", Name: "bash", Args: map[string]any{"command": "echo child > child.go", "run_in_background": true}})
	if !r.Success {
		t.Fatal(r)
	}
	id, _ := r.Data.(map[string]any)["task_id"].(string)
	task, ok := manager.Get(id)
	if !ok {
		t.Fatal("missing task")
	}
	select {
	case <-task.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("background command did not finish")
	}
	if got := a.GetMutatedPaths(); !reflect.DeepEqual(got, []string{"child.go"}) {
		t.Fatalf("background child ledger=%v", got)
	}
}

func TestAgentPropagatesWorkspaceCap(t *testing.T) {
	t.Setenv("GOKIN_WORKSPACE_SNAPSHOT_MAX_FILES", "1")
	root := testkit.ResolvedTempDir(t)
	for _, name := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("before"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bash := tools.NewBashTool(root)
	bash.SetSandboxEnabled(false)
	registry := tools.NewRegistry()
	registry.MustRegister(bash)
	a := NewAgent(AgentTypeGeneral, nil, registry, root, 5, "", nil, nil)
	a.reflector = nil
	a.client = testkit.NewMockClient().EnqueueToolCall("bash", map[string]any{"command": "echo child > child.go"}).EnqueueText("Created child.go.")
	result, err := a.Run(tools.ContextWithoutSelfReview(t.Context()), "Create child.go")
	if err != nil || result.Metadata["files_changed_partial"] != true || len(result.FilesChanged) != 0 {
		t.Fatalf("child cap was not disclosed: %+v %v", result, err)
	}
}

func TestAgentBashCheckoutClaimUsesObservedWrite(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "m.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "initial"}} {
		cmd := gitexec.CommandContext(t.Context(), args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	for _, name := range []string{"m.go", "other.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("// dirty content\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bash := tools.NewBashTool(root)
	bash.SetSandboxEnabled(false)
	registry := tools.NewRegistry()
	registry.MustRegister(bash)
	a := NewAgent(AgentTypeGeneral, nil, registry, root, 5, "", nil, nil)
	a.SetDoneGatePolicy(donegate.Policy{Enabled: true})
	r := a.executeNestedToolResultSafely(t.Context(), &genai.FunctionCall{ID: "restore", Name: "bash", Args: map[string]any{"command": "git checkout -- m.go"}})
	if !r.Success || len(a.GetMutatedPaths()) != 1 {
		t.Fatalf("checkout=%+v paths=%v", r, a.GetMutatedPaths())
	}
	if a.injectClaimCorrectionIfNeeded("Updated m.go.") {
		t.Fatal("real shell restoration called a false claim")
	}
}
