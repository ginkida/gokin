package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/permission"
)

func TestPermissionArgsForBashIncludeCurrentSessionScope(t *testing.T) {
	bash := NewBashTool("/repo/a")
	args := map[string]any{"command": "make deploy"}
	first := PermissionArgsForTool(bash, args)
	if _, mutated := args[permissionExecutionScopeArg]; mutated {
		t.Fatal("permission scope mutated model arguments")
	}
	firstScope, ok := first[permissionExecutionScopeArg].(map[string]any)
	if !ok || firstScope["cwd"] != "/repo/a" {
		t.Fatalf("first scope = %#v", first[permissionExecutionScopeArg])
	}

	bash.session.SetWorkDir("/repo/b")
	second := PermissionArgsForTool(bash, args)
	secondScope, ok := second[permissionExecutionScopeArg].(map[string]any)
	if !ok || secondScope["cwd"] != "/repo/b" {
		t.Fatalf("second scope = %#v", second[permissionExecutionScopeArg])
	}
	if firstScope["cwd"] == secondScope["cwd"] {
		t.Fatal("bash cwd change did not change permission identity")
	}
}

func TestPermissionArgsForBashFingerprintEnvironmentWithoutExposingValues(t *testing.T) {
	bash := NewBashTool(t.TempDir())
	const secret = "super-secret-deploy-token"
	if err := bash.session.SetEnv("DEPLOY_TOKEN", secret); err != nil {
		t.Fatal(err)
	}

	first := PermissionArgsForTool(bash, map[string]any{"command": "make deploy"})
	firstScope := first[permissionExecutionScopeArg].(map[string]any)
	if _, exposed := firstScope["env"]; exposed {
		t.Fatalf("permission scope exposed raw environment: %#v", firstScope)
	}
	firstFingerprint, ok := firstScope["env_fingerprint"].(string)
	if !ok || firstFingerprint == "" || strings.Contains(firstFingerprint, secret) {
		t.Fatalf("environment fingerprint = %#v", firstScope["env_fingerprint"])
	}

	if err := bash.session.SetEnv("DEPLOY_TOKEN", "rotated-token"); err != nil {
		t.Fatal(err)
	}
	second := PermissionArgsForTool(bash, map[string]any{"command": "make deploy"})
	secondScope := second[permissionExecutionScopeArg].(map[string]any)
	if secondScope["env_fingerprint"] == firstFingerprint {
		t.Fatal("environment value change did not change permission identity")
	}
}

func TestPermissionArgsForBashIncludeRuntimeSecurityBoundary(t *testing.T) {
	bash := NewBashTool(t.TempDir())
	bash.SetSandboxEnabled(true)
	args := map[string]any{"command": "go test ./..."}
	before := PermissionArgsForTool(bash, args)
	beforeScope := before[permissionExecutionScopeArg].(map[string]any)
	if got := beforeScope["sandbox_enabled"]; got != true {
		t.Fatalf("sandbox scope = %#v, want true", got)
	}

	bash.SetSandboxEnabled(false)
	after := PermissionArgsForTool(bash, args)
	afterScope := after[permissionExecutionScopeArg].(map[string]any)
	if got := afterScope["sandbox_enabled"]; got != false {
		t.Fatalf("sandbox scope after toggle = %#v, want false", got)
	}
	if beforeScope["policy_revision"] == afterScope["policy_revision"] {
		t.Fatal("security-mode toggle did not change permission generation")
	}
}

func TestExecutorRejectsBashWhenSecurityModeChangesDuringApproval(t *testing.T) {
	workDir := t.TempDir()
	bash := NewBashTool(workDir)
	bash.SetSandboxEnabled(true)
	registry := NewRegistry()
	if err := registry.Register(bash); err != nil {
		t.Fatal(err)
	}

	rules := permission.DefaultRules()
	rules.SetPolicy("bash", permission.LevelAsk)
	permissions := permission.NewManager(rules, true)
	promptStarted := make(chan struct{})
	releasePrompt := make(chan struct{})
	permissions.SetPromptHandler(func(context.Context, *permission.Request) (permission.Decision, error) {
		close(promptStarted)
		<-releasePrompt
		return permission.DecisionAllow, nil
	})

	executor := NewExecutor(registry, nil, 2*time.Second)
	executor.SetPermissions(permissions)
	target := filepath.Join(workDir, "must-not-exist.txt")
	resultCh := make(chan ToolResult, 1)
	go func() {
		resultCh <- executor.doExecuteTool(context.Background(), testFunctionCall("bash-scope", "bash", map[string]any{
			"command": "printf escaped > must-not-exist.txt",
		}))
	}()

	<-promptStarted
	// The user saw/approved sandbox=on. Switching to direct execution while the
	// prompt is pending must invalidate that decision rather than run unsandboxed.
	bash.SetSandboxEnabled(false)
	close(releasePrompt)
	result := <-resultCh
	if result.Success || !strings.Contains(result.Error, "Permission scope changed before execution") {
		t.Fatalf("stale sandbox approval executed: %#v", result)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("command ran after scope invalidation; stat error=%v", err)
	}
}

func TestExecutorPermissionHandlerCannotRewriteRetainedBashScope(t *testing.T) {
	workDir := t.TempDir()
	bash := NewBashTool(workDir)
	bash.SetSandboxEnabled(true)
	registry := NewRegistry()
	if err := registry.Register(bash); err != nil {
		t.Fatal(err)
	}

	rules := permission.DefaultRules()
	rules.SetPolicy("bash", permission.LevelAsk)
	permissions := permission.NewManager(rules, true)
	args := map[string]any{"command": "printf escaped > must-not-exist.txt"}
	permissions.SetPromptHandler(func(_ context.Context, req *permission.Request) (permission.Decision, error) {
		// Simulate an untrusted/future prompt adapter rewriting the request it was
		// handed to the newly-current policy. This must not mutate the retained
		// authorization snapshot used by actual execution.
		bash.SetSandboxEnabled(false)
		current := PermissionArgsForTool(bash, args)
		req.Args[permissionExecutionScopeArg] = current[permissionExecutionScopeArg]
		return permission.DecisionAllow, nil
	})

	executor := NewExecutor(registry, nil, 2*time.Second)
	executor.SetPermissions(permissions)
	result := executor.doExecuteTool(context.Background(), testFunctionCall("bash-handler-scope", "bash", args))
	if result.Success || !strings.Contains(result.Error, "Permission scope changed before execution") {
		t.Fatalf("prompt handler rewrote retained authorization scope: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(workDir, "must-not-exist.txt")); !os.IsNotExist(err) {
		t.Fatalf("command ran after prompt scope rewrite; stat error=%v", err)
	}
}

func TestExecuteWithPermissionScopeRejectsArgumentMutationAfterAuthorization(t *testing.T) {
	workDir := t.TempDir()
	bash := NewBashTool(workDir)
	args := map[string]any{"command": "printf approved"}
	authorized := PermissionArgsForTool(bash, args)

	args["command"] = "printf escaped > must-not-exist.txt"
	result, err := ExecuteWithPermissionScope(context.Background(), bash, args, authorized)
	if err != nil {
		t.Fatalf("ExecuteWithPermissionScope returned error: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "arguments no longer match") {
		t.Fatalf("mutated invocation executed: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(workDir, "must-not-exist.txt")); !os.IsNotExist(err) {
		t.Fatalf("mutated command ran; stat error=%v", err)
	}
}

func TestBashPolicyToggleDoesNotWaitForActiveCommand(t *testing.T) {
	workDir := t.TempDir()
	newBoundary := t.TempDir()
	bash := NewBashTool(workDir)
	bash.SetTimeout(5 * time.Second)

	started := make(chan struct{})
	var startedOnce sync.Once
	ctx, cancel := context.WithCancel(context.Background())
	ctx = ContextWithProgressCallback(ctx, func(_ float64, output string) {
		if strings.Contains(output, "started") {
			startedOnce.Do(func() { close(started) })
		}
	})

	executionDone := make(chan ToolResult, 1)
	go func() {
		result, err := bash.Execute(ctx, map[string]any{
			"command": "printf 'started\\n'; while [ ! -f .release-command ]; do sleep 0.02; done; pwd",
		})
		if err != nil {
			result = NewErrorResult(err.Error())
		}
		executionDone <- result
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		<-executionDone
		t.Fatal("bash command did not start")
	}

	toggleDone := make(chan struct{})
	go func() {
		bash.SetWorkspaceBoundary(newBoundary)
		close(toggleDone)
	}()

	toggleWasPrompt := false
	select {
	case <-toggleDone:
		toggleWasPrompt = true
	case <-time.After(500 * time.Millisecond):
	}

	if err := os.WriteFile(filepath.Join(workDir, ".release-command"), []byte("release"), 0o600); err != nil {
		cancel()
		t.Fatalf("release active bash command: %v", err)
	}
	result := <-executionDone
	cancel()
	if !toggleWasPrompt {
		// Ensure an implementation that blocks the toggle is released before the
		// test exits; this also makes the failure deterministic instead of leaking.
		<-toggleDone
		t.Fatal("workspace/sandbox policy toggle waited for the active command")
	}
	if !result.Success || !strings.Contains(result.Content, workDir) {
		t.Fatalf("in-flight command did not retain its starting session: %#v", result)
	}
	if got := bash.session.WorkDir(); got != newBoundary {
		t.Fatalf("stale command session overwrote newer workspace policy: cwd=%q want=%q", got, newBoundary)
	}
}

func TestBashQueuedExecutionHonorsContextCancellation(t *testing.T) {
	bash := NewBashTool(t.TempDir())
	bash.SetTimeout(5 * time.Second)

	started := make(chan struct{})
	var startedOnce sync.Once
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstCtx = ContextWithProgressCallback(firstCtx, func(_ float64, output string) {
		if strings.Contains(output, "started") {
			startedOnce.Do(func() { close(started) })
		}
	})
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = bash.Execute(firstCtx, map[string]any{
			"command": "printf 'started\\n'; while [ ! -f .release-first ]; do sleep 0.02; done",
		})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancelFirst()
		<-firstDone
		t.Fatal("first bash command did not start")
	}

	type queuedOutcome struct {
		result ToolResult
		err    error
	}
	queuedCtx, cancelQueued := context.WithTimeout(context.Background(), 100*time.Millisecond)
	queuedDone := make(chan queuedOutcome, 1)
	go func() {
		result, err := bash.Execute(queuedCtx, map[string]any{"command": "printf should-not-run"})
		queuedDone <- queuedOutcome{result: result, err: err}
	}()

	queuedWasPrompt := false
	var outcome queuedOutcome
	select {
	case outcome = <-queuedDone:
		queuedWasPrompt = true
	case <-time.After(500 * time.Millisecond):
	}
	if err := os.WriteFile(filepath.Join(bash.workDir, ".release-first"), []byte("release"), 0o600); err != nil {
		cancelFirst()
		t.Fatalf("release first bash command: %v", err)
	}
	if !queuedWasPrompt {
		outcome = <-queuedDone
	}
	cancelQueued()
	cancelFirst()
	<-firstDone

	if !queuedWasPrompt {
		t.Fatal("queued execution ignored its context until the active command completed")
	}
	if outcome.err != nil {
		t.Fatalf("queued execution returned transport error: %v", outcome.err)
	}
	if outcome.result.Success || !strings.Contains(outcome.result.Error, "timeout") {
		t.Fatalf("queued execution result = %#v, want timeout failure", outcome.result)
	}
}

func TestExecutorUnrestrictedModeConcurrentToggle(t *testing.T) {
	executor := NewExecutor(NewRegistry(), nil, time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				executor.SetUnrestrictedMode((seed+j)%2 == 0)
				_ = executor.IsUnrestrictedMode()
			}
		}(i)
	}
	wg.Wait()
}
