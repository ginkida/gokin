package hooks

import (
	"context"
	"testing"
	"time"
)

func TestHookMatches(t *testing.T) {
	tests := []struct {
		name     string
		hook     Hook
		hookType Type
		toolName string
		want     bool
	}{
		{"disabled", Hook{Enabled: false, Type: PreTool}, PreTool, "bash", false},
		{"wrong type", Hook{Enabled: true, Type: PostTool}, PreTool, "bash", false},
		{"match all tools", Hook{Enabled: true, Type: PreTool}, PreTool, "bash", true},
		{"match specific tool", Hook{Enabled: true, Type: PreTool, ToolName: "bash"}, PreTool, "bash", true},
		{"wrong tool", Hook{Enabled: true, Type: PreTool, ToolName: "read"}, PreTool, "bash", false},
		{"invalid condition", Hook{Enabled: true, Type: PreTool, Condition: "invalid"}, PreTool, "bash", false},
		{"valid condition always", Hook{Enabled: true, Type: PreTool, Condition: ConditionAlways}, PreTool, "bash", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.hook.Matches(tt.hookType, tt.toolName)
			if got != tt.want {
				t.Errorf("Matches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHookShouldRun(t *testing.T) {
	t.Run("disabled hook", func(t *testing.T) {
		h := &Hook{Enabled: false, Type: PreTool}
		ctx := &Context{}
		if h.ShouldRun(ctx, nil) {
			t.Error("disabled hook should not run")
		}
	})

	t.Run("condition always", func(t *testing.T) {
		h := &Hook{Enabled: true, Type: PreTool, Condition: ConditionAlways}
		ctx := &Context{}
		if !h.ShouldRun(ctx, nil) {
			t.Error("condition always should run")
		}
	})

	t.Run("condition if_previous_success - success", func(t *testing.T) {
		h := &Hook{Enabled: true, Type: PostTool, Condition: ConditionIfPreviousSuccess}
		ctx := &Context{previousSuccess: true}
		if !h.ShouldRun(ctx, nil) {
			t.Error("should run when previous succeeded")
		}
	})

	t.Run("condition if_previous_success - failure", func(t *testing.T) {
		h := &Hook{Enabled: true, Type: PostTool, Condition: ConditionIfPreviousSuccess}
		ctx := &Context{previousSuccess: false}
		if h.ShouldRun(ctx, nil) {
			t.Error("should not run when previous failed")
		}
	})

	t.Run("condition if_previous_failure - failure", func(t *testing.T) {
		h := &Hook{Enabled: true, Type: OnError, Condition: ConditionIfPreviousFailure}
		ctx := &Context{previousSuccess: false}
		if !h.ShouldRun(ctx, nil) {
			t.Error("should run when previous failed")
		}
	})

	t.Run("condition if_previous_failure - success", func(t *testing.T) {
		h := &Hook{Enabled: true, Type: OnError, Condition: ConditionIfPreviousFailure}
		ctx := &Context{previousSuccess: true}
		if h.ShouldRun(ctx, nil) {
			t.Error("should not run when previous succeeded")
		}
	})

	t.Run("depends on completed", func(t *testing.T) {
		h := &Hook{Enabled: true, Type: PreTool, DependsOn: "lint"}
		ctx := &Context{}
		completed := map[string]bool{"lint": true}
		if !h.ShouldRun(ctx, completed) {
			t.Error("should run when dependency completed")
		}
	})

	t.Run("depends on not completed", func(t *testing.T) {
		h := &Hook{Enabled: true, Type: PreTool, DependsOn: "lint"}
		ctx := &Context{}
		if h.ShouldRun(ctx, nil) {
			t.Error("should not run when dependency not completed")
		}
		completed := map[string]bool{"other": true}
		if h.ShouldRun(ctx, completed) {
			t.Error("should not run when specific dependency not completed")
		}
	})
}

func TestContextNewAndSetters(t *testing.T) {
	ctx := NewContext("bash", map[string]any{"command": "ls"}, "/tmp")
	if ctx.ToolName != "bash" {
		t.Errorf("ToolName = %q", ctx.ToolName)
	}
	if ctx.WorkDir != "/tmp" {
		t.Errorf("WorkDir = %q", ctx.WorkDir)
	}

	ctx.SetResult("output")
	if ctx.ToolResult != "output" {
		t.Errorf("ToolResult = %q", ctx.ToolResult)
	}

	ctx.SetError("error msg")
	if ctx.ToolError != "error msg" {
		t.Errorf("ToolError = %q", ctx.ToolError)
	}
}

func TestContextExpandCommand(t *testing.T) {
	ctx := NewContext("bash", map[string]any{
		"command":   "echo hello",
		"file_path": "/src/main.go",
	}, "/workspace")
	ctx.SetResult("success")
	ctx.SetError("some error")

	// Variables are shell-escaped (quoted)
	got := ctx.ExpandCommand("echo ${TOOL_NAME}")
	if got != "echo 'bash'" {
		t.Errorf("ExpandCommand tool_name = %q", got)
	}

	got = ctx.ExpandCommand("cd ${WORK_DIR}")
	if got != "cd '/workspace'" {
		t.Errorf("ExpandCommand work_dir = %q", got)
	}

	got = ctx.ExpandCommand("cat ${FILE_PATH}")
	if got != "cat '/src/main.go'" {
		t.Errorf("ExpandCommand file_path = %q", got)
	}
}

func TestManagerBasics(t *testing.T) {
	m := NewManager(true, "/tmp")

	if !m.IsEnabled() {
		t.Error("should be enabled")
	}

	m.SetEnabled(false)
	if m.IsEnabled() {
		t.Error("should be disabled")
	}
	m.SetEnabled(true)

	m.SetTimeout(10 * time.Second)

	// Add hooks
	h1 := &Hook{Name: "lint", Type: PreTool, ToolName: "write", Enabled: true, Command: "echo lint"}
	h2 := &Hook{Name: "test", Type: PostTool, Enabled: true, Command: "echo test"}
	m.AddHook(h1)
	m.AddHook(h2)

	hooks := m.GetHooks()
	if len(hooks) != 2 {
		t.Errorf("hooks count = %d, want 2", len(hooks))
	}

	// HasHooksFor
	if !m.HasHooksFor(PreTool, "write") {
		t.Error("should have pre_tool hook for write")
	}
	if m.HasHooksFor(PreTool, "read") {
		t.Error("should not have pre_tool hook for read")
	}
	if !m.HasHooksFor(PostTool, "anything") {
		t.Error("should have post_tool hook for any tool")
	}

	// Clear
	m.ClearHooks()
	if len(m.GetHooks()) != 0 {
		t.Error("should be empty after clear")
	}
}

func TestManagerAddHooks(t *testing.T) {
	m := NewManager(true, "/tmp")
	hooks := []*Hook{
		{Name: "a", Type: PreTool, Enabled: true, Command: "echo a"},
		{Name: "b", Type: PreTool, Enabled: true, Command: "echo b"},
	}
	m.AddHooks(hooks)
	if len(m.GetHooks()) != 2 {
		t.Errorf("hooks count = %d, want 2", len(m.GetHooks()))
	}
}

func TestManagerRunDisabled(t *testing.T) {
	m := NewManager(false, "/tmp")
	m.AddHook(&Hook{Name: "test", Type: PreTool, Enabled: true, Command: "echo test"})

	results := m.Run(context.Background(), PreTool, &Context{WorkDir: "/tmp", Extra: make(map[string]string)})
	if results != nil {
		t.Error("disabled manager should return nil")
	}
}

func TestManagerRunSimpleHook(t *testing.T) {
	m := NewManager(true, "/tmp")
	m.SetTimeout(5 * time.Second)
	m.AddHook(&Hook{
		Name:    "echo-test",
		Type:    PreTool,
		Enabled: true,
		Command: "echo hello",
	})

	results := m.RunPreTool(context.Background(), "bash", nil)
	if len(results) != 1 {
		t.Fatalf("results count = %d, want 1", len(results))
	}
	if results[0].Error != nil {
		t.Errorf("hook error: %v", results[0].Error)
	}
	if results[0].Output == "" {
		t.Error("output should not be empty")
	}
}

func TestManagerRunFailOnError(t *testing.T) {
	m := NewManager(true, "/tmp")
	m.SetTimeout(5 * time.Second)
	m.AddHook(&Hook{
		Name:        "fail",
		Type:        PreTool,
		Enabled:     true,
		Command:     "exit 1",
		FailOnError: true,
	})
	m.AddHook(&Hook{
		Name:    "second",
		Type:    PreTool,
		Enabled: true,
		Command: "echo second",
	})

	results := m.RunPreTool(context.Background(), "bash", nil)
	// Should stop after first hook fails
	if len(results) != 1 {
		t.Errorf("should stop after FailOnError, got %d results", len(results))
	}
	if results[0].Error == nil {
		t.Error("first hook should have error")
	}
}

func TestManagerRunHandler(t *testing.T) {
	m := NewManager(true, "/tmp")
	m.SetTimeout(5 * time.Second)

	var called bool
	m.SetHandler(func(hook *Hook, output string, err error) {
		called = true
	})
	m.AddHook(&Hook{
		Name:    "test",
		Type:    PreTool,
		Enabled: true,
		Command: "echo handler",
	})

	m.RunPreTool(context.Background(), "bash", nil)
	if !called {
		t.Error("handler should be called")
	}
}

func TestManagerRunChaining(t *testing.T) {
	m := NewManager(true, "/tmp")
	m.SetTimeout(5 * time.Second)
	m.AddHook(&Hook{
		Name:    "first",
		Type:    PreTool,
		Enabled: true,
		Command: "echo first",
	})
	m.AddHook(&Hook{
		Name:      "second",
		Type:      PreTool,
		Enabled:   true,
		Command:   "echo second",
		DependsOn: "first",
	})

	results := m.RunPreTool(context.Background(), "bash", nil)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if results[0].Error != nil {
		t.Errorf("first hook error: %v", results[0].Error)
	}
	if results[1].Error != nil {
		t.Errorf("second hook error: %v", results[1].Error)
	}
}

func TestManagerHasHooksForDisabled(t *testing.T) {
	m := NewManager(false, "/tmp")
	m.AddHook(&Hook{Name: "test", Type: PreTool, Enabled: true, Command: "echo"})
	if m.HasHooksFor(PreTool, "bash") {
		t.Error("disabled manager should return false")
	}
}
