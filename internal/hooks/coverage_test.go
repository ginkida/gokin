package hooks

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// --- Context accessors -------------------------------------------------------

// TestContextPreviousSuccessRoundTrip covers SetPreviousSuccess, which has no
// direct getter — the only observable signal is whether a Condition hook
// fires. We assert both the success and failure states via ShouldRun.
func TestContextPreviousSuccessRoundTrip(t *testing.T) {
	ctx := &Context{}
	ctx.SetPreviousSuccess(true)
	if !ctx.previousSuccess {
		t.Error("SetPreviousSuccess(true) did not set the field")
	}

	// A ConditionIfPreviousSuccess hook must run after success.
	h := &Hook{Enabled: true, Type: PostTool, Condition: ConditionIfPreviousSuccess}
	if !h.ShouldRun(ctx, nil) {
		t.Error("if_previous_success hook did not run after SetPreviousSuccess(true)")
	}

	ctx.SetPreviousSuccess(false)
	if ctx.previousSuccess {
		t.Error("SetPreviousSuccess(false) did not clear the field")
	}
	// A ConditionIfPreviousFailure hook must run after failure.
	h2 := &Hook{Enabled: true, Type: OnError, Condition: ConditionIfPreviousFailure}
	if !h2.ShouldRun(ctx, nil) {
		t.Error("if_previous_failure hook did not run after SetPreviousSuccess(false)")
	}
}

// TestContextGetCapturedOutput pins the getter used by downstream consumers to
// read the stdout+stderr the previous hook emitted. Initially empty; reflects
// whatever was assigned (the manager sets this during Run).
func TestContextGetCapturedOutput(t *testing.T) {
	ctx := &Context{}
	if got := ctx.GetCapturedOutput(); got != "" {
		t.Errorf("fresh CapturedOutput = %q, want empty", got)
	}
	ctx.CapturedOutput = "line one\nline two"
	if got := ctx.GetCapturedOutput(); got != "line one\nline two" {
		t.Errorf("GetCapturedOutput = %q, want the assigned value", got)
	}
}

// --- ExpandCommand branches not covered by existing tests --------------------

// TestExpandCommand_BuiltinVars covers the ${COMMAND}, ${PATTERN}, ${ERROR},
// and ${CONTENT} (incl. truncation) branches of ExpandCommand that the current
// TestContextExpandCommand doesn't reach. ${RESULT}/${TOOL_NAME}/${WORK_DIR}/
// ${FILE_PATH} are already exercised there.
func TestExpandCommand_BuiltinVars(t *testing.T) {
	t.Run("COMMAND arg", func(t *testing.T) {
		ctx := NewContext("bash", map[string]any{"command": "ls -la"}, "/w")
		got := ctx.ExpandCommand("run ${COMMAND}")
		if got != "run 'ls -la'" {
			t.Errorf("COMMAND = %q, want run 'ls -la'", got)
		}
	})
	t.Run("COMMAND missing → empty substitution", func(t *testing.T) {
		ctx := NewContext("bash", map[string]any{}, "/w")
		got := ctx.ExpandCommand("run ${COMMAND} after")
		if got != "run  after" { // empty value, unquoted because Expand returns ""
			t.Errorf("missing COMMAND = %q", got)
		}
	})
	t.Run("PATTERN arg", func(t *testing.T) {
		ctx := NewContext("grep", map[string]any{"pattern": "func.*Run"}, "/w")
		got := ctx.ExpandCommand("rg ${PATTERN}")
		if got != "rg 'func.*Run'" {
			t.Errorf("PATTERN = %q", got)
		}
	})
	t.Run("ERROR var", func(t *testing.T) {
		ctx := NewContext("bash", map[string]any{}, "/w")
		ctx.SetError("boom")
		got := ctx.ExpandCommand("report ${ERROR}")
		if got != "report 'boom'" {
			t.Errorf("ERROR = %q", got)
		}
	})
	t.Run("CONTENT short — no truncation", func(t *testing.T) {
		ctx := NewContext("write", map[string]any{"content": "hello"}, "/w")
		got := ctx.ExpandCommand("cat << ${CONTENT}")
		if !strings.Contains(got, "'hello'") {
			t.Errorf("short CONTENT not passed through: %q", got)
		}
	})
	t.Run("CONTENT long — truncated to 100 runes + ellipsis", func(t *testing.T) {
		long := strings.Repeat("x", 250)
		ctx := NewContext("write", map[string]any{"content": long}, "/w")
		got := ctx.ExpandCommand("${CONTENT}")
		// The escaped value should contain exactly 100 x's followed by "...".
		wantInner := strings.Repeat("x", 100) + "..."
		if !strings.Contains(got, wantInner) {
			t.Errorf("long CONTENT not truncated to 100 runes + '...': %q", got)
		}
		// And must NOT contain the full 250-run input.
		if strings.Contains(got, strings.Repeat("x", 250)) {
			t.Error("truncation did not trim the content; full 250-rune run present")
		}
	})
	t.Run("CONTENT wrong type → empty", func(t *testing.T) {
		ctx := NewContext("write", map[string]any{"content": 42}, "/w")
		got := ctx.ExpandCommand("[${CONTENT}]")
		if got != "[]" {
			t.Errorf("non-string CONTENT = %q, want []", got)
		}
	})
	t.Run("unknown var resolves from Extra", func(t *testing.T) {
		ctx := NewContext("bash", map[string]any{}, "/w")
		ctx.Extra["MY_VAR"] = "extra-value"
		got := ctx.ExpandCommand("echo ${MY_VAR}")
		if got != "echo 'extra-value'" {
			t.Errorf("Extra-map fallback = %q", got)
		}
	})
}

// --- Run* convenience wrappers ----------------------------------------------

// All five convenience wrappers (RunPreTool/PostTool/OnError/OnStart/OnExit)
// plus RunStop build the right Context and route to Run. We assert each fires
// the matching hook and skips mismatched-type hooks. RunPreTool is already
// exercised elsewhere; the other four are not covered.

func TestManagerRunPostTool(t *testing.T) {
	m := NewManager(true, "")
	m.AddHook(&Hook{Name: "after", Type: PostTool, Enabled: true, Command: "echo done"})
	m.AddHook(&Hook{Name: "wrongtype", Type: PreTool, Enabled: true, Command: "echo no"})

	results := m.RunPostTool(context.Background(), "bash", map[string]any{}, "tool-result-string")
	if len(results) != 1 {
		t.Fatalf("RunPostTool fired %d hooks, want 1", len(results))
	}
	if results[0].Hook.Name != "after" {
		t.Errorf("fired hook = %q, want after", results[0].Hook.Name)
	}
	if results[0].Error != nil {
		t.Errorf("echo hook unexpectedly failed: %v", results[0].Error)
	}
	// The post-tool context must carry the tool result so hooks can expand
	// ${RESULT}. We verify by running a hook that echoes it back.
	m.ClearHooks()
	m.AddHook(&Hook{Name: "verify", Type: PostTool, Enabled: true, Command: "printf %s \"${RESULT}\""})
	results = m.RunPostTool(context.Background(), "bash", map[string]any{}, "PAYLOAD")
	if len(results) != 1 || !strings.Contains(results[0].Output, "PAYLOAD") {
		t.Fatalf("RESULT not threaded to post-tool hook: %+v", results)
	}
}

func TestManagerRunOnError(t *testing.T) {
	m := NewManager(true, "")
	m.AddHook(&Hook{Name: "err", Type: OnError, Enabled: true, Command: "printf %s \"${ERROR}\""})

	results := m.RunOnError(context.Background(), "bash", map[string]any{}, "explosion")
	if len(results) != 1 {
		t.Fatalf("RunOnError fired %d hooks, want 1", len(results))
	}
	if !strings.Contains(results[0].Output, "explosion") {
		t.Errorf("ERROR not threaded to on-error hook: %q", results[0].Output)
	}
}

func TestManagerRunOnStart(t *testing.T) {
	m := NewManager(true, "")
	m.AddHook(&Hook{Name: "boot", Type: OnStart, Enabled: true, Command: "echo start"})

	results := m.RunOnStart(context.Background())
	if len(results) != 1 || results[0].Hook.Name != "boot" {
		t.Fatalf("RunOnStart results = %+v, want one 'boot' hook", results)
	}
}

func TestManagerRunOnExit(t *testing.T) {
	m := NewManager(true, "")
	m.AddHook(&Hook{Name: "shutdown", Type: OnExit, Enabled: true, Command: "echo bye"})

	results := m.RunOnExit(context.Background())
	if len(results) != 1 || results[0].Hook.Name != "shutdown" {
		t.Fatalf("RunOnExit results = %+v, want one 'shutdown' hook", results)
	}
}

// TestManagerRunStop covers the end-of-turn wrapper: ${RESULT} must be expanded
// from finalResponse, and a Stop-type hook must fire while other types are
// skipped.
func TestManagerRunStop(t *testing.T) {
	m := NewManager(true, "")
	m.AddHook(&Hook{Name: "end", Type: Stop, Enabled: true, Command: "printf %s \"${RESULT}\""})
	m.AddHook(&Hook{Name: "notnow", Type: PreTool, Enabled: true, Command: "echo no"})

	results := m.RunStop(context.Background(), "FINAL-RESPONSE")
	if len(results) != 1 {
		t.Fatalf("RunStop fired %d hooks, want 1", len(results))
	}
	if results[0].Hook.Name != "end" {
		t.Errorf("fired hook = %q, want end", results[0].Hook.Name)
	}
	if !strings.Contains(results[0].Output, "FINAL-RESPONSE") {
		t.Errorf("RESULT not threaded to stop hook: %q", results[0].Output)
	}
}

func TestManagerToolOutcomeWrappersDriveHookConditions(t *testing.T) {
	m := NewManager(true, t.TempDir())
	m.AddHooks([]*Hook{
		{Name: "success-only", Type: PostTool, Condition: ConditionIfPreviousSuccess, Enabled: true, Command: "printf success"},
		{Name: "wrong-post", Type: PostTool, Condition: ConditionIfPreviousFailure, Enabled: true, Command: "printf wrong"},
		{Name: "failure-only", Type: OnError, Condition: ConditionIfPreviousFailure, Enabled: true, Command: "printf failure"},
		{Name: "wrong-error", Type: OnError, Condition: ConditionIfPreviousSuccess, Enabled: true, Command: "printf wrong"},
	})

	post := m.RunPostTool(context.Background(), "read", nil, "ok")
	if len(post) != 1 || post[0].Hook.Name != "success-only" {
		t.Fatalf("post-tool conditional hooks = %+v", post)
	}
	onError := m.RunOnError(context.Background(), "read", nil, "boom")
	if len(onError) != 1 || onError[0].Hook.Name != "failure-only" {
		t.Fatalf("on-error conditional hooks = %+v", onError)
	}
}

// --- Blocked ----------------------------------------------------------------

// TestBlocked covers the package-level helper that surfaces the first
// FailOnError hook that failed (callers then block the tool call and feed the
// hook's output back to the model).
func TestBlocked(t *testing.T) {
	t.Run("no results", func(t *testing.T) {
		if _, ok := Blocked(nil); ok {
			t.Error("Blocked(nil) should be false")
		}
	})
	t.Run("no failing FailOnError hook", func(t *testing.T) {
		results := []Result{
			{Hook: &Hook{Name: "a", FailOnError: true}, Error: nil},
			{Hook: &Hook{Name: "b", FailOnError: false}, Error: nil},
		}
		if _, ok := Blocked(results); ok {
			t.Error("Blocked found a block where none should exist")
		}
	})
	t.Run("non-FailOnError failure is not a block", func(t *testing.T) {
		results := []Result{
			{Hook: &Hook{Name: "soft", FailOnError: false}, Error: nil},
		}
		if _, ok := Blocked(results); ok {
			t.Error("a failing hook without FailOnError must not block")
		}
	})
	t.Run("returns first FailOnError failure", func(t *testing.T) {
		results := []Result{
			{Hook: &Hook{Name: "first-fail", FailOnError: true}, Error: nil}, // no error → skipped
			{Hook: nil, Error: nil}, // nil hook guard
			{Hook: &Hook{Name: "blocker", FailOnError: true}, Error: context.DeadlineExceeded, Output: "timeout"},
		}
		got, ok := Blocked(results)
		if !ok {
			t.Fatal("Blocked missed the FailOnError failure")
		}
		if got.Hook.Name != "blocker" {
			t.Errorf("returned hook = %q, want blocker", got.Hook.Name)
		}
		if got.Output != "timeout" {
			t.Errorf("returned Output = %q, want timeout", got.Output)
		}
	})
	t.Run("stops at first block", func(t *testing.T) {
		// A second FailOnError failure later in the slice must be ignored —
		// the contract is "the FIRST result that blocks".
		results := []Result{
			{Hook: &Hook{Name: "first", FailOnError: true}, Error: context.Canceled, Output: "first-out"},
			{Hook: &Hook{Name: "second", FailOnError: true}, Error: context.DeadlineExceeded, Output: "second-out"},
		}
		got, ok := Blocked(results)
		if !ok || got.Hook.Name != "first" {
			t.Errorf("Blocked = %+v ok=%v, want first blocker", got, ok)
		}
	})
}

// --- killHookProcess --------------------------------------------------------

// TestKillHookProcess_NilProcess covers the early-return guard: when
// cmd.Process is nil (Start never called / failed), the function must be a
// no-op and not panic on the nil deref inside killProcessGroup.
func TestKillHookProcess_NilProcess(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("killHookProcess panicked on nil Process: %v", r)
		}
	}()
	// exec.Command leaves cmd.Process == nil until Start() is called, so a
	// freshly-built command is exactly the nil-Process state we need to test.
	cmd := exec.Command("true")
	killHookProcess(cmd, 50*time.Millisecond, nil)
}
