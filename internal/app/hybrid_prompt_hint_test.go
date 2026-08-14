package app

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"gokin/internal/harness"
	"gokin/internal/repl"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

func TestHeadlessHybridHintIsProviderVisibleButNotPersisted(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText("exact result")
	application, _ := newHeadlessPolicyTestApp(t, mock, tools.NewReplExecTool(nil))
	prompt := "Count TODOs per directory across the repository"

	result, err := application.RunHeadlessWithOptions(context.Background(), prompt, HeadlessOptions{
		OutputFormat: HeadlessOutputText,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	if err != nil || result.Status != "success" {
		t.Fatalf("headless result=%+v err=%v", result, err)
	}
	calls := mock.Calls()
	if len(calls) != 1 || !strings.Contains(calls[0].Message, "repl_exec") ||
		!strings.Contains(calls[0].Message, prompt) {
		t.Fatalf("provider-visible message = %+v", calls)
	}

	var persistedUserText string
	for _, content := range application.session.GetHistory() {
		if content != nil && content.Role == genai.RoleUser && len(content.Parts) == 1 && content.Parts[0] != nil {
			persistedUserText = content.Parts[0].Text
			break
		}
	}
	if persistedUserText != prompt {
		t.Fatalf("persisted user text = %q, want exact original %q", persistedUserText, prompt)
	}
}

func TestHeadlessDoesNotHintUnavailableREPL(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText("structured fallback")
	application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
	prompt := "Count TODOs per directory across the repository"
	result, err := application.RunHeadlessWithOptions(context.Background(), prompt, HeadlessOptions{
		OutputFormat: HeadlessOutputText,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	if err != nil || result.Status != "success" {
		t.Fatalf("headless result=%+v err=%v", result, err)
	}
	calls := mock.Calls()
	if len(calls) != 1 || strings.Contains(calls[0].Message, "repl_exec") || calls[0].Message != prompt {
		t.Fatalf("fallback provider message = %+v", calls)
	}
}

func TestHeadlessAutoSchemaBlocksREPLHallucinationAfterEligibleNonUse(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueText("analytics complete").
		EnqueueToolCall("repl_exec", map[string]any{"code": "1 + 1"}).
		EnqueueText("I continued after the hidden tool was refused.").
		EnqueueText("clean final turn")
	application, _ := newHeadlessPolicyTestApp(t, mock, tools.NewReplExecTool(nil))
	if err := application.registry.Register(tools.NewHarnessTool(nil)); err != nil {
		t.Fatal(err)
	}
	fake := &fakeHybridRuntime{}
	application.deferredHybrid = &deferredHybridInit{
		registry: application.registry,
		opener: func(context.Context, repl.Options) (hybridRuntime, repl.Availability) {
			return fake, repl.Availability{
				Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec,
			}
		},
		opts: repl.Options{WorkDir: application.workDir},
	}
	registered, ok := application.registry.Get("repl_exec")
	if !ok {
		t.Fatal("repl_exec is not registered")
	}
	registered.(*tools.ReplExecTool).SetManager(application.deferredHybrid)
	// The store constructor is part of activation; pin the workspace contract
	// explicitly so this test cannot pass with an unusable harness path.
	if _, err := harness.NewStore(application.workDir); err != nil {
		t.Fatal(err)
	}

	first, err := application.RunHeadlessWithOptions(
		context.Background(), "Count TODOs per directory across the repository",
		HeadlessOptions{OutputFormat: HeadlessOutputText, Stdout: io.Discard, Stderr: io.Discard},
	)
	if err != nil || first.Status != "success" || application.deferredHybrid.isReady() {
		t.Fatalf("eligible non-use result=%+v err=%v ready=%t", first, err, application.deferredHybrid.isReady())
	}
	if fake.executions.Load() != 0 {
		t.Fatalf("activation executed Python without a model tool call: %d", fake.executions.Load())
	}

	second, err := application.RunHeadlessWithOptions(
		context.Background(), "Explain this function",
		HeadlessOptions{OutputFormat: HeadlessOutputText, Stdout: io.Discard, Stderr: io.Discard},
	)
	if err == nil || second.Status != "policy_blocked" || second.Error == nil ||
		second.Error.Tool != "repl_exec" || second.Error.PolicyKind != "permission" {
		t.Fatalf("hallucinated hidden REPL result=%+v err=%v", second, err)
	}
	if fake.executions.Load() != 0 {
		t.Fatalf("hidden repl_exec reached runtime %d times", fake.executions.Load())
	}

	third, err := application.RunHeadlessWithOptions(
		context.Background(), "Explain this function again",
		HeadlessOptions{OutputFormat: HeadlessOutputText, Stdout: io.Discard, Stderr: io.Discard},
	)
	if err != nil || third.Status != "success" || third.Result != "clean final turn" {
		t.Fatalf("schema policy leaked into next turn: result=%+v err=%v", third, err)
	}
}

func TestHeadlessAutoActivatesWorkerOnlyOnModelExecute(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueToolCall("repl_exec", map[string]any{"code": "6 * 7"}).
		EnqueueText("the answer is 42")
	application, _ := newHeadlessPolicyTestApp(t, mock, tools.NewReplExecTool(nil))
	fake := &fakeHybridRuntime{}
	var openerCalls atomic.Int32
	application.deferredHybrid = &deferredHybridInit{
		registry: application.registry,
		opener: func(context.Context, repl.Options) (hybridRuntime, repl.Availability) {
			openerCalls.Add(1)
			return fake, repl.Availability{
				Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec,
			}
		},
		opts: repl.Options{WorkDir: application.workDir},
	}
	registered, ok := application.registry.Get("repl_exec")
	if !ok {
		t.Fatal("repl_exec is not registered")
	}
	registered.(*tools.ReplExecTool).SetManager(application.deferredHybrid)

	result, err := application.RunHeadlessWithOptions(
		context.Background(), "Count TODOs per directory across the repository",
		HeadlessOptions{OutputFormat: HeadlessOutputText, Stdout: io.Discard, Stderr: io.Discard},
	)
	if err != nil || result.Status != "success" || result.Result != "the answer is 42" {
		t.Fatalf("headless lazy execution result=%+v err=%v", result, err)
	}
	if openerCalls.Load() != 1 || fake.executions.Load() != 1 || !application.deferredHybrid.isReady() {
		t.Fatalf("lazy execution opener=%d executions=%d ready=%t",
			openerCalls.Load(), fake.executions.Load(), application.deferredHybrid.isReady())
	}
}
