package app

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	"gokin/internal/chat"
	"gokin/internal/config"
	"gokin/internal/hooks"
	"gokin/internal/permission"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestRunHeadless_PolicyBlockCannotBeMaskedByModelText(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		wantKind  tools.PolicyBlockKind
		configure func(t *testing.T, exec *tools.Executor)
	}{
		{
			name:     "permission denial",
			toolName: "write",
			wantKind: tools.PolicyBlockPermission,
			configure: func(t *testing.T, exec *tools.Executor) {
				rules := permission.DefaultRules()
				rules.SetPolicy("write", permission.LevelDeny)
				exec.SetPermissions(permission.NewManager(rules, true))
			},
		},
		{
			name:     "safety validator unavailable",
			toolName: "read",
			wantKind: tools.PolicyBlockSafety,
			configure: func(t *testing.T, exec *tools.Executor) {
				exec.SetSafetyValidator(appUnavailableSafetyValidator{})
			},
		},
		{
			name:     "trusted hook refusal",
			toolName: "write",
			wantKind: tools.PolicyBlockHook,
			configure: func(t *testing.T, exec *tools.Executor) {
				manager := hooks.NewManager(true, t.TempDir())
				manager.AddHook(&hooks.Hook{
					Name:        "headless-guard",
					Type:        hooks.PreTool,
					ToolName:    "write",
					Command:     "echo 'writes frozen by policy' >&2; exit 1",
					Enabled:     true,
					FailOnError: true,
				})
				exec.SetHooks(manager)
			},
		},
		{
			name:     "plan mode refusal",
			toolName: "write",
			wantKind: tools.PolicyBlockPlan,
			configure: func(t *testing.T, exec *tools.Executor) {
				exec.SetPlanModeCheck(func() bool { return true })
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := testkit.NewMockClient().
				EnqueueToolCall(tt.toolName, map[string]any{"file_path": "blocked.txt"}).
				EnqueueText("Everything is complete despite the refusal.")
			tool := &appHeadlessScriptedTool{
				name:    tt.toolName,
				results: []tools.ToolResult{tools.NewSuccessResult("executed")},
			}
			app, exec := newHeadlessPolicyTestApp(t, mock, tool)
			tt.configure(t, exec)

			stdout, err := captureHeadlessStdoutResult(t, func() error {
				return app.RunHeadless(context.Background(), "perform the blocked action")
			})
			if err == nil {
				t.Fatal("RunHeadless returned nil after a policy block masked by model text")
			}
			for _, needle := range []string{string(tt.wantKind) + " policy", `tool "` + tt.toolName + `"`} {
				if !strings.Contains(err.Error(), needle) {
					t.Fatalf("RunHeadless error missing %q: %v", needle, err)
				}
			}
			if !strings.Contains(stdout, "Everything is complete despite the refusal.") {
				t.Fatalf("model text should remain available on stdout, got %q", stdout)
			}
			if calls := tool.CallCount(); calls != 0 {
				t.Fatalf("policy-blocked tool executed %d times, want 0", calls)
			}
			if calls := mock.Calls(); len(calls) != 2 {
				t.Fatalf("model calls = %d, want tool round plus adversarial text round", len(calls))
			}
		})
	}
}

func TestRunHeadless_PolicyStatusIsTurnScoped(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueToolCall("write", map[string]any{"file_path": "blocked.txt"}).
		EnqueueText("First turn continued after denial.").
		EnqueueText("Second turn is clean.")
	tool := &appHeadlessScriptedTool{
		name:    "write",
		results: []tools.ToolResult{tools.NewSuccessResult("unexpected")},
	}
	app, exec := newHeadlessPolicyTestApp(t, mock, tool)
	rules := permission.DefaultRules()
	rules.SetPolicy("write", permission.LevelDeny)
	exec.SetPermissions(permission.NewManager(rules, true))

	_, firstErr := captureHeadlessStdoutResult(t, func() error {
		return app.RunHeadless(context.Background(), "blocked first turn")
	})
	if firstErr == nil {
		t.Fatal("first turn should fail closed")
	}

	secondOut, secondErr := captureHeadlessStdoutResult(t, func() error {
		return app.RunHeadless(context.Background(), "clean second turn")
	})
	if secondErr != nil {
		t.Fatalf("stale policy status leaked into next headless turn: %v", secondErr)
	}
	if !strings.Contains(secondOut, "Second turn is clean.") {
		t.Fatalf("second turn stdout = %q", secondOut)
	}
}

func TestRunHeadless_PlanRejectionProtocolSuccessStillFailsClosed(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueToolCall("enter_plan_mode", map[string]any{}).
		EnqueueText("I will stop after the rejected plan.")
	tool := &appHeadlessScriptedTool{
		name: "enter_plan_mode",
		results: []tools.ToolResult{tools.WithPolicyBlock(
			tools.NewSuccessResult("Plan rejected by user."),
			tools.PolicyBlockPlan,
			"plan rejected by user",
		)},
	}
	app, _ := newHeadlessPolicyTestApp(t, mock, tool)

	_, err := captureHeadlessStdoutResult(t, func() error {
		return app.RunHeadless(context.Background(), "propose and execute a plan")
	})
	if err == nil || !strings.Contains(err.Error(), "plan policy") {
		t.Fatalf("protocol-successful plan rejection must fail closed, got %v", err)
	}
	if calls := tool.CallCount(); calls != 1 {
		t.Fatalf("plan tool calls = %d, want 1", calls)
	}
}

func TestInteractiveTurn_PolicyBlockRemainsRecoverable(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueToolCall("write", map[string]any{"file_path": "blocked.txt"}).
		EnqueueText("Interactive explanation after denial.")
	tool := &appHeadlessScriptedTool{
		name:    "write",
		results: []tools.ToolResult{tools.NewSuccessResult("unexpected")},
	}
	app, exec := newHeadlessPolicyTestApp(t, mock, tool)
	rules := permission.DefaultRules()
	rules.SetPolicy("write", permission.LevelDeny)
	exec.SetPermissions(permission.NewManager(rules, true))

	app.processMessageWithContext(context.Background(), "interactive request")
	if app.lastError != "" {
		t.Fatalf("interactive policy denial became a terminal model error: %q", app.lastError)
	}
	if blocked := app.headlessPolicyFailureSnapshot(); blocked != nil {
		t.Fatalf("interactive turn contaminated headless policy state: %+v", blocked)
	}
}

func TestRunHeadless_OrdinaryToolFailureCanRecover(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueToolCall("flaky", map[string]any{}).
		EnqueueToolCall("flaky", map[string]any{}).
		EnqueueText("Recovered after retrying the ordinary tool failure.")
	tool := &appHeadlessScriptedTool{
		name: "flaky",
		results: []tools.ToolResult{
			tools.NewErrorResult("temporary execution failure"),
			tools.NewSuccessResult("recovered"),
		},
	}
	app, _ := newHeadlessPolicyTestApp(t, mock, tool)

	stdout, err := captureHeadlessStdoutResult(t, func() error {
		return app.RunHeadless(context.Background(), "recover from a normal tool error")
	})
	if err != nil {
		t.Fatalf("ordinary tool recovery must keep exit status successful: %v", err)
	}
	if !strings.Contains(stdout, "Recovered after retrying") {
		t.Fatalf("stdout missing recovered answer: %q", stdout)
	}
	if calls := tool.CallCount(); calls != 2 {
		t.Fatalf("tool calls = %d, want 2", calls)
	}
}

func TestRunHeadless_SynchronousSubagentPolicyBlockPropagates(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueToolCall("task", map[string]any{
			"prompt":        "perform protected work",
			"subagent_type": "general",
		}).
		EnqueueText("The delegated work is complete.")
	taskTool := tools.NewTaskTool()
	taskTool.SetRunner(appPolicyAgentRunner{result: tools.AgentResult{
		AgentID:     "agent-policy",
		Type:        "general",
		Status:      "completed",
		Output:      "The subagent explained that the protected action was denied.",
		Completed:   true,
		PolicyBlock: &tools.PolicyBlock{Kind: tools.PolicyBlockPermission, Reason: "subagent write denied"},
	}})
	app, _ := newHeadlessPolicyTestApp(t, mock, taskTool)

	stdout, err := captureHeadlessStdoutResult(t, func() error {
		return app.RunHeadless(context.Background(), "delegate protected work")
	})
	if err == nil || !strings.Contains(err.Error(), "permission policy") {
		t.Fatalf("subagent policy refusal was masked by parent prose: %v", err)
	}
	if !strings.Contains(stdout, "delegated work is complete") {
		t.Fatalf("parent prose should remain on stdout, got %q", stdout)
	}
}

type appPolicyAgentRunner struct {
	result tools.AgentResult
}

func (r appPolicyAgentRunner) Spawn(context.Context, string, string, int, string) (string, error) {
	return r.result.AgentID, nil
}
func (r appPolicyAgentRunner) SpawnAsync(context.Context, string, string, int, string) string {
	return r.result.AgentID
}
func (r appPolicyAgentRunner) SpawnAsyncWithStreaming(context.Context, string, string, int, string, func(string), func(string, *tools.AgentProgress)) string {
	return r.result.AgentID
}
func (r appPolicyAgentRunner) Resume(context.Context, string, string) (string, error) {
	return r.result.AgentID, nil
}
func (r appPolicyAgentRunner) ResumeAsync(context.Context, string, string) (string, error) {
	return r.result.AgentID, nil
}
func (r appPolicyAgentRunner) GetResult(string) (tools.AgentResult, bool) {
	return r.result, true
}

type appHeadlessScriptedTool struct {
	mu      sync.Mutex
	name    string
	results []tools.ToolResult
	calls   int
}

func (t *appHeadlessScriptedTool) Name() string        { return t.name }
func (t *appHeadlessScriptedTool) Description() string { return "headless policy test tool" }
func (t *appHeadlessScriptedTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.name, Description: t.Description()}
}
func (t *appHeadlessScriptedTool) Validate(map[string]any) error { return nil }
func (t *appHeadlessScriptedTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	index := t.calls
	t.calls++
	if index >= len(t.results) {
		return tools.NewErrorResult("test tool result queue exhausted"), nil
	}
	return t.results[index], nil
}
func (t *appHeadlessScriptedTool) CallCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

type appUnavailableSafetyValidator struct{}

func (appUnavailableSafetyValidator) ValidateSafety(context.Context, string, map[string]any) (*tools.PreFlightCheck, error) {
	return nil, errors.New("validator unavailable")
}
func (appUnavailableSafetyValidator) GetSummary(string, map[string]any) *tools.ExecutionSummary {
	return nil
}
func (appUnavailableSafetyValidator) GetMetadata(string) (*tools.ToolMetadata, bool) {
	return nil, false
}

func newHeadlessPolicyTestApp(t *testing.T, mock *testkit.MockClient, tool tools.Tool) (*App, *tools.Executor) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Model.Provider = "mock"
	cfg.Model.Name = "mock-model"
	cfg.DoneGate.Enabled = false

	registry := tools.NewRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatalf("Register(%s): %v", tool.Name(), err)
	}
	exec := tools.NewExecutor(registry, mock, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	app := &App{
		config:              cfg,
		workDir:             t.TempDir(),
		client:              mock,
		registry:            registry,
		executor:            exec,
		session:             chat.NewSession(),
		ctx:                 ctx,
		cancel:              cancel,
		rateLimitRetryCount: make(map[string]int),
	}
	exec.SetHandler(app.buildExecutionHandler(nil))
	return app, exec
}

func captureHeadlessStdoutResult(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	original := os.Stdout
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe(): %v", pipeErr)
	}
	os.Stdout = w
	runErr := fn()
	closeErr := w.Close()
	os.Stdout = original
	output, readErr := io.ReadAll(r)
	_ = r.Close()
	if closeErr != nil {
		t.Fatalf("close stdout pipe: %v", closeErr)
	}
	if readErr != nil {
		t.Fatalf("read stdout pipe: %v", readErr)
	}
	return string(output), runErr
}
