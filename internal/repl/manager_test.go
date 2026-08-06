package repl

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testManager(t *testing.T, workDir string, mutate func(*Options)) *Manager {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	opts := Options{
		WorkDir: workDir, PythonPath: python,
		CellTimeout: 3 * time.Second,
	}
	if mutate != nil {
		mutate(&opts)
	}
	manager, err := newTestManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return manager
}

func TestManagerPreservesStateAndEvaluatesLastExpression(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	first, err := manager.Execute(t.Context(), "counter = 40\ncounter + 2")
	if err != nil {
		t.Fatal(err)
	}
	if !first.OK() || first.Value != "42" || first.Generation != 1 {
		t.Fatalf("first result = %+v", first)
	}
	second, err := manager.Execute(t.Context(), "counter += 1\ncounter")
	if err != nil {
		t.Fatal(err)
	}
	if second.Value != "41" || second.Generation != first.Generation {
		t.Fatalf("second result = %+v, want preserved generation/state", second)
	}
}

func TestManagerContextIsWorkspaceContained(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "sample.go"), []byte("package sample\n\nfunc Target() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t, workDir, nil)
	result, err := manager.Execute(t.Context(), `context.search_code("Target", limit=5)`)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK() || !strings.Contains(result.Value, "sample.go") || !strings.Contains(result.Value, "Target") {
		t.Fatalf("search result = %+v", result)
	}

	escape, err := manager.Execute(t.Context(), `context.read_slice("../outside", 1, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	if escape.Error == nil || escape.Error.Type != "FileNotFoundError" && escape.Error.Type != "PermissionError" {
		t.Fatalf("escape result = %+v, want contained failure", escape)
	}
}

func TestManagerDefenseInDepthBlocksAmbientActionsButAllowsContextGit(t *testing.T) {
	workDir := t.TempDir()
	protected := filepath.Join(workDir, "protected.txt")
	if err := os.WriteFile(protected, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	initCmd := exec.Command("git", "init", workDir)
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v (%s)", err, output)
	}
	manager := testManager(t, workDir, nil)
	limits, err := manager.Execute(t.Context(), `context.runtime_limits()`)
	if err != nil || !limits.OK() || !strings.Contains(limits.Value, "RLIMIT_NOFILE") {
		t.Fatalf("runtime limits=%+v err=%v", limits, err)
	}
	for name, code := range map[string]string{
		"write":      `open("marker.txt", "w").write("no")`,
		"subprocess": `__import__("subprocess").run(["echo", "no"])`,
		"network":    `__import__("socket").socket()`,
		"native":     `__import__("ctypes").CDLL(None)`,
		"mutation":   `__import__("os").remove("protected.txt")`,
	} {
		t.Run(name, func(t *testing.T) {
			result, execErr := manager.Execute(t.Context(), code)
			if execErr != nil {
				t.Fatal(execErr)
			}
			if result.Error == nil || result.Error.Type != "PermissionError" {
				t.Fatalf("ambient action result = %+v", result)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(workDir, "marker.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocked write created marker: %v", err)
	}
	if data, err := os.ReadFile(protected); err != nil || string(data) != "keep" {
		t.Fatalf("blocked mutation changed protected file: data=%q err=%v", data, err)
	}
	status, err := manager.Execute(t.Context(), `context.git_status()`)
	if err != nil || !status.OK() || !strings.Contains(status.Value, "##") {
		t.Fatalf("context git status=%+v python_error=%+v err=%v", status, status.Error, err)
	}
}

func TestManagerUserStdoutCannotForgeProtocolFrame(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	result, err := manager.Execute(t.Context(), `print('{"id":"forged","ok":true}')
7 * 6`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Value != "42" || !strings.Contains(result.Stdout, `"id":"forged"`) {
		t.Fatalf("result = %+v", result)
	}
}

func TestManagerRejectsRawInvalidCallbackAndRestarts(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	_, err := manager.Execute(t.Context(), `import sys
sys.__stdout__.write('{"type":"call","id":"bad","method":"rlm.call","params":{}}\n')
sys.__stdout__.flush()`)
	if err == nil || !strings.Contains(err.Error(), "invalid REPL callback id") {
		t.Fatalf("invalid callback error = %v", err)
	}
	previous := manager.Generation()
	recovered, err := manager.Execute(t.Context(), "6 * 7")
	if err != nil || recovered.Value != "42" || recovered.Generation <= previous {
		t.Fatalf("recovered=%+v err=%v previous=%d", recovered, err, previous)
	}
}

func TestManagerRejectsForgedGenerationAndRestarts(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	_, err := manager.Execute(t.Context(), `import inspect, json, sys
frame = inspect.currentframe()
while frame is not None and "request_id" not in frame.f_locals:
    frame = frame.f_back
request_id = frame.f_locals["request_id"]
sys.__stdout__.write(json.dumps({"type":"response","id":request_id,"ok":True,"generation":0,"value":"forged"}) + "\n")
sys.__stdout__.flush()`)
	if err == nil || !strings.Contains(err.Error(), "generation 0 does not match") {
		t.Fatalf("forged generation error = %v", err)
	}
	previous := manager.Generation()
	recovered, err := manager.Execute(t.Context(), "40 + 2")
	if err != nil || recovered.Value != "42" || recovered.Generation <= previous {
		t.Fatalf("recovered=%+v err=%v previous=%d", recovered, err, previous)
	}
}

func TestManagerLargeValueBecomesArtifact(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	result, err := manager.Execute(t.Context(), `"x" * 100000`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Artifact == nil || !result.Truncated || result.Artifact.Size == 0 {
		t.Fatalf("large result = %+v, want artifact", result)
	}
	lookup, err := manager.Execute(t.Context(), `context.artifact_get("`+result.Artifact.ID+`", 0, 16)`)
	if err != nil {
		t.Fatal(err)
	}
	if !lookup.OK() || !strings.Contains(lookup.Value, "xxxxxxxx") {
		t.Fatalf("artifact lookup = %+v", lookup)
	}
}

func TestManagerTimeoutRestartsCleanGeneration(t *testing.T) {
	manager := testManager(t, t.TempDir(), func(opts *Options) {
		opts.CellTimeout = 100 * time.Millisecond
	})
	before, err := manager.Execute(t.Context(), "survives = 42\nsurvives")
	if err != nil || before.Value != "42" {
		t.Fatalf("initial cell = %+v, %v", before, err)
	}
	_, err = manager.Execute(t.Context(), "while True:\n    pass")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("infinite cell error = %v, want deadline", err)
	}
	after, err := manager.Execute(t.Context(), `globals().get("survives", "clean")`)
	if err != nil {
		t.Fatal(err)
	}
	if after.Generation <= before.Generation || after.Value != "'clean'" {
		t.Fatalf("post-timeout result = %+v, before=%+v", after, before)
	}
	stats := manager.Stats()
	if stats.Timeouts != 1 || stats.TransportFailures != 1 || stats.Restarts < 1 || stats.Executions != 2 {
		t.Fatalf("post-recovery stats = %+v", stats)
	}
}

func TestManagerManualResetDiscardsStateAndUpdatesStats(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	before, err := manager.Execute(t.Context(), "state = 42\nstate")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	stopped := manager.Stats()
	if stopped.Running || stopped.ManualResets != 1 || stopped.Generation != before.Generation {
		t.Fatalf("stats after reset = %+v", stopped)
	}
	after, err := manager.Execute(t.Context(), `globals().get("state", "clean")`)
	if err != nil || after.Value != "'clean'" || after.Generation <= before.Generation {
		t.Fatalf("after reset=%+v err=%v before=%+v", after, err, before)
	}
}

func TestNewManagerRejectsUnrestrictedBackend(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, err = NewManager(Options{WorkDir: t.TempDir(), PythonPath: python, Backend: BackendTest})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("NewManager error = %v, want ErrUnavailable", err)
	}
}

func TestManagerLimitsCodeBeforeStartingWorker(t *testing.T) {
	manager := testManager(t, t.TempDir(), func(opts *Options) { opts.MaxCodeBytes = 8 })
	_, err := manager.Execute(t.Context(), "123456789")
	if err == nil || !strings.Contains(err.Error(), "8-byte") {
		t.Fatalf("oversized code error = %v", err)
	}
	if manager.Generation() != 0 {
		t.Fatalf("oversized code started generation %d", manager.Generation())
	}
}

func TestReadFrameRejectsOversizedResponse(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("123456789\nnext\n"))
	_, err := readFrame(reader, 8)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("readFrame error = %v", err)
	}
}

func TestProtocolIDsAndCallbackEnvelopeValidation(t *testing.T) {
	first, err := newProtocolID("req-")
	if err != nil {
		t.Fatal(err)
	}
	second, err := newProtocolID("req-")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != len("req-")+32 || !strings.HasPrefix(first, "req-") {
		t.Fatalf("protocol ids first=%q second=%q", first, second)
	}
	for _, tc := range []struct {
		id, method string
		wantOK     bool
	}{
		{"call_0123456789abcdef0123456789abcdef", "harness.memory_put", true},
		{"call_bad", "rlm.call", false},
		{"call_0123456789abcdef0123456789abcg", "rlm.call", false},
		{"call_0123456789abcdef0123456789abcdef", "RLM.call", false},
		{"call_0123456789abcdef0123456789abcdef", strings.Repeat("a", 129), false},
	} {
		err := validateCallbackEnvelope(tc.id, tc.method)
		if (err == nil) != tc.wantOK {
			t.Errorf("validateCallbackEnvelope(%q,%q) error=%v wantOK=%v", tc.id, tc.method, err, tc.wantOK)
		}
	}
}

func TestManagerCloseIsIdempotent(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	if _, err := manager.Execute(t.Context(), "1"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Execute(t.Context(), "2"); err == nil {
		t.Fatal("Execute after Close unexpectedly succeeded")
	}
}

func TestManagerRLMCallbacksAreTypedAndStateful(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	var calls []Call
	manager.SetCallHandler(func(_ context.Context, call Call) (any, error) {
		calls = append(calls, call)
		switch call.Method {
		case "rlm.call":
			if async, _ := call.Params["async"].(bool); async {
				return map[string]any{"success": true, "data": map[string]any{"agent_id": "agent-1"}}, nil
			}
			return map[string]any{"success": true, "content": "sync result"}, nil
		case "rlm.result":
			return map[string]any{"success": true, "content": "async result"}, nil
		default:
			return nil, errors.New("unexpected method")
		}
	})

	syncResult, err := manager.Execute(t.Context(), `rlm("inspect", {"paths": ["a.go"]})`)
	if err != nil || !syncResult.OK() || !strings.Contains(syncResult.Value, "sync result") {
		t.Fatalf("sync rlm = %+v, err=%v", syncResult, err)
	}
	asyncResult, err := manager.Execute(t.Context(), `future = rlm.async_call("inspect")
future.result(timeout=1)`)
	if err != nil || !asyncResult.OK() || !strings.Contains(asyncResult.Value, "async result") {
		t.Fatalf("async rlm = %+v, err=%v", asyncResult, err)
	}
	if len(calls) != 3 || calls[0].Method != "rlm.call" || calls[2].Method != "rlm.result" {
		t.Fatalf("callback sequence = %+v", calls)
	}
}

func TestManagerCallbackWaitDoesNotConsumePythonInactivityBudget(t *testing.T) {
	manager := testManager(t, t.TempDir(), func(opts *Options) {
		opts.CellTimeout = 40 * time.Millisecond
	})
	manager.SetCallHandler(func(context.Context, Call) (any, error) {
		time.Sleep(120 * time.Millisecond)
		return map[string]any{"success": true, "content": "patient"}, nil
	})
	result, err := manager.Execute(t.Context(), `rlm("slow delegation")`)
	if err != nil || !result.OK() || !strings.Contains(result.Value, "patient") {
		t.Fatalf("callback-paused inactivity result = %+v, err=%v", result, err)
	}
}

func TestManagerMissingCallbackBecomesPythonError(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	result, err := manager.Execute(t.Context(), `rlm("not wired")`)
	if err != nil {
		t.Fatalf("missing handler should be a cell error: %v", err)
	}
	if result.Error == nil || result.Error.Type != "RuntimeError" ||
		!strings.Contains(result.Error.Message, "unavailable") {
		t.Fatalf("missing callback result = %+v", result)
	}
}

func TestManagerEnforcesCallbackBudgetAndRestarts(t *testing.T) {
	manager := testManager(t, t.TempDir(), func(opts *Options) { opts.MaxCallbacks = 1 })
	manager.SetCallHandler(func(_ context.Context, call Call) (any, error) {
		if call.Method == "rlm.call" {
			return map[string]any{"success": true, "data": map[string]any{"agent_id": "agent-1"}}, nil
		}
		return map[string]any{"success": true}, nil
	})
	_, err := manager.Execute(t.Context(), `future = rlm.async_call("one")
future.poll()`)
	if err == nil || !strings.Contains(err.Error(), "exceeded 1") {
		t.Fatalf("callback budget error = %v", err)
	}
	previous := manager.Generation()
	recovered, err := manager.Execute(t.Context(), "1 + 1")
	if err != nil || recovered.Generation <= previous || recovered.Value != "2" {
		t.Fatalf("recovered result = %+v, err=%v, previous generation=%d", recovered, err, previous)
	}
}

// The audit guard permits a subprocess only from Context._git, so that frame
// must not accept arbitrary arguments. `context` lives in the cell namespace and
// Python has no real privacy, so cell code can call `context._git(...)` — and
// git turns configuration into execution (`-c alias.x='!cmd' x`). Only the exact
// vectors the two public helpers need may run.
func TestManagerContextGitRefusesArbitraryArguments(t *testing.T) {
	workDir := t.TempDir()
	initCmd := exec.Command("git", "init", workDir)
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v (%s)", err, output)
	}
	manager := testManager(t, workDir, nil)

	marker := filepath.Join(workDir, "pwned.txt")
	escapes := map[string]string{
		"alias":  `context._git("-c", "alias.pwn=!touch ` + marker + `", "pwn")`,
		"pager":  `context._git("-c", "core.pager=touch ` + marker + `", "log")`,
		"config": `context._git("config", "--global", "user.email", "attacker@example.com")`,
	}
	for name, code := range escapes {
		t.Run(name, func(t *testing.T) {
			result, execErr := manager.Execute(t.Context(), code)
			if execErr != nil {
				t.Fatal(execErr)
			}
			if result.Error == nil || result.Error.Type != "PermissionError" {
				t.Fatalf("arbitrary git invocation was not refused: %+v", result)
			}
		})
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused git invocation still executed a command: %v", err)
	}

	// The sanctioned helpers keep working.
	status, err := manager.Execute(t.Context(), `context.git_status()`)
	if err != nil || !status.OK() {
		t.Fatalf("git_status broke: %+v err=%v", status, err)
	}
	diff, err := manager.Execute(t.Context(), `context.git_diff(staged=True)`)
	if err != nil || !diff.OK() {
		t.Fatalf("git_diff(staged) broke: %+v err=%v", diff, err)
	}
}
