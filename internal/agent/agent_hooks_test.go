package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"gokin/internal/hooks"
	"gokin/internal/tools"
)

type agentHookProbeTool struct {
	name   string
	result tools.ToolResult
	err    error
	calls  int
}

func (t *agentHookProbeTool) Name() string        { return t.name }
func (t *agentHookProbeTool) Description() string { return "agent hook probe" }
func (t *agentHookProbeTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.name, Description: t.Description()}
}
func (t *agentHookProbeTool) Validate(map[string]any) error { return nil }
func (t *agentHookProbeTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	t.calls++
	return t.result, t.err
}

func newHookProbeAgent(t *testing.T, workDir string, probe *agentHookProbeTool) *Agent {
	t.Helper()
	registry := tools.NewRegistry()
	if err := registry.Register(probe); err != nil {
		t.Fatalf("register probe tool: %v", err)
	}
	return NewAgent(AgentTypeGeneral, nil, registry, workDir, 1, "", nil, nil)
}

func TestAgentPreToolHookBlocksDirectSubagentExecutionInEffectiveWorkDir(t *testing.T) {
	workDir := t.TempDir()
	probe := &agentHookProbeTool{name: "write", result: tools.NewSuccessResult("written")}
	agent := newHookProbeAgent(t, workDir, probe)

	manager := hooks.NewManager(true, t.TempDir())
	var observedDir string
	manager.SetHandler(func(hook *hooks.Hook, output string, err error) {
		if hook.Name == "observe-workdir" {
			observedDir = strings.TrimSpace(output)
		}
	})
	manager.AddHooks([]*hooks.Hook{
		{
			Name:     "observe-workdir",
			Type:     hooks.PreTool,
			ToolName: "write",
			Command:  "pwd",
			Enabled:  true,
		},
		{
			Name:        "deny-write",
			Type:        hooks.PreTool,
			ToolName:    "write",
			Command:     "echo 'subagent writes denied' >&2; exit 1",
			Enabled:     true,
			FailOnError: true,
		},
	})
	agent.SetHooks(manager)

	result := agent.executeTool(context.Background(), &genai.FunctionCall{
		ID: "call-1", Name: "write", Args: map[string]any{"file_path": "x.txt"},
	})

	if result.Success {
		t.Fatal("blocking pre-tool hook must refuse the sub-agent call")
	}
	if result.PolicyBlock == nil || result.PolicyBlock.Kind != tools.PolicyBlockHook {
		t.Fatalf("blocking hook result lacks typed policy metadata: %#v", result.PolicyBlock)
	}
	if probe.calls != 0 {
		t.Fatalf("tool executed %d times despite blocking hook, want 0", probe.calls)
	}
	if got := agent.StatefulToolAttemptCount(); got != 0 {
		t.Fatalf("blocked tool crossed stateful execution provenance: got %d attempts, want 0", got)
	}
	if !strings.Contains(result.Error, "deny-write") || !strings.Contains(result.Error, "subagent writes denied") {
		t.Fatalf("hook identity/reason missing from error: %q", result.Error)
	}
	wantDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		wantDir = workDir
	}
	gotDir, err := filepath.EvalSymlinks(observedDir)
	if err != nil {
		gotDir = observedDir
	}
	if gotDir != wantDir {
		t.Fatalf("pre-tool hook workdir = %q, want effective agent workdir %q", gotDir, wantDir)
	}
}

func TestAgentToolHooksRouteSuccessAndFailure(t *testing.T) {
	workDir := t.TempDir()
	successTool := &agentHookProbeTool{name: "read", result: tools.NewSuccessResult("ok")}
	failureTool := &agentHookProbeTool{name: "write", err: errors.New("write exploded")}
	registry := tools.NewRegistry()
	if err := registry.Register(successTool); err != nil {
		t.Fatalf("register success tool: %v", err)
	}
	if err := registry.Register(failureTool); err != nil {
		t.Fatalf("register failure tool: %v", err)
	}
	agent := NewAgent(AgentTypeGeneral, nil, registry, workDir, 1, "", nil, nil)

	manager := hooks.NewManager(true, workDir)
	var ran []string
	manager.SetHandler(func(hook *hooks.Hook, _ string, _ error) {
		ran = append(ran, hook.Name)
	})
	manager.AddHooks([]*hooks.Hook{
		{Name: "after-read", Type: hooks.PostTool, ToolName: "read", Command: "printf post", Enabled: true},
		{Name: "read-error", Type: hooks.OnError, ToolName: "read", Command: "printf wrong", Enabled: true},
		{Name: "after-write", Type: hooks.PostTool, ToolName: "write", Command: "printf wrong", Enabled: true},
		{Name: "write-error", Type: hooks.OnError, ToolName: "write", Command: "printf error", Enabled: true},
	})
	agent.SetHooks(manager)

	if result := agent.executeTool(context.Background(), &genai.FunctionCall{ID: "read-1", Name: "read", Args: map[string]any{}}); !result.Success {
		t.Fatalf("read result = %#v, want success", result)
	}
	if result := agent.executeTool(context.Background(), &genai.FunctionCall{ID: "write-1", Name: "write", Args: map[string]any{}}); result.Success || !strings.Contains(result.Error, "write exploded") {
		t.Fatalf("write result = %#v, want execution failure", result)
	}

	if got, want := strings.Join(ran, ","), "after-read,write-error"; got != want {
		t.Fatalf("executed hooks = %q, want %q", got, want)
	}
}

func TestAgentBashHookCannotChangeCapturedExecutionScopeWithoutPermissions(t *testing.T) {
	workDir := t.TempDir()
	registry := tools.NewRegistry()
	if err := registry.Register(tools.NewBashTool(workDir)); err != nil {
		t.Fatalf("register bash: %v", err)
	}
	agent := NewAgent(AgentTypeGeneral, nil, registry, workDir, 1, "", nil, nil)

	manager := hooks.NewManager(true, workDir)
	manager.AddHook(&hooks.Hook{
		Name:     "slow-pre-bash",
		Type:     hooks.PreTool,
		ToolName: "bash",
		Command:  "touch .hook-started; sleep 0.3",
		Enabled:  true,
	})
	agent.SetHooks(manager)

	resultCh := make(chan tools.ToolResult, 1)
	go func() {
		resultCh <- agent.executeTool(context.Background(), &genai.FunctionCall{
			ID: "bash-hook-scope", Name: "bash", Args: map[string]any{"command": "printf must-not-run"},
		})
	}()

	marker := filepath.Join(workDir, ".hook-started")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pre-tool hook did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	tool, ok := agent.registry.Get("bash")
	if !ok {
		t.Fatal("agent bash clone missing")
	}
	tool.(*tools.BashTool).SetSandboxEnabled(true)

	result := <-resultCh
	if result.Success || !strings.Contains(result.Error, "Permission scope changed before execution") {
		t.Fatalf("hook-time policy change was not rejected: %#v", result)
	}
}
