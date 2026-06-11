package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ============================================================
// BashSession Tests
// ============================================================

func TestNewBashSession(t *testing.T) {
	session := NewBashSession("/home/user")

	if session == nil {
		t.Fatal("NewBashSession() returned nil")
	}
	if session.workDir != "/home/user" {
		t.Errorf("workDir = %v, want %v", session.workDir, "/home/user")
	}
	if session.env == nil {
		t.Error("env map is nil")
	}
}

func TestBashSession_WorkDir(t *testing.T) {
	session := NewBashSession("/tmp")

	wd := session.WorkDir()
	if wd != "/tmp" {
		t.Errorf("WorkDir() = %v, want %v", wd, "/tmp")
	}
}

func TestBashSession_SetWorkDir(t *testing.T) {
	session := NewBashSession("/tmp")

	session.SetWorkDir("/var")

	wd := session.WorkDir()
	if wd != "/var" {
		t.Errorf("WorkDir() = %v, want %v", wd, "/var")
	}
}

func TestBashSession_SetEnv(t *testing.T) {
	session := NewBashSession("/tmp")

	err := session.SetEnv("MY_VAR", "my_value")
	if err != nil {
		t.Errorf("SetEnv() unexpected error: %v", err)
	}

	env := session.Env()
	if env["MY_VAR"] != "my_value" {
		t.Errorf("env[MY_VAR] = %v, want %v", env["MY_VAR"], "my_value")
	}
}

func TestBashSession_SetEnv_Dangerous(t *testing.T) {
	session := NewBashSession("/tmp")

	dangerousVars := []string{
		"LD_PRELOAD",
		"LD_LIBRARY_PATH",
		"DYLD_INSERT_LIBRARIES",
		"BASH_ENV",
		"ENV",
	}

	for _, varName := range dangerousVars {
		err := session.SetEnv(varName, "evil_value")
		if err == nil {
			t.Errorf("SetEnv(%q) should return error for dangerous variable", varName)
		}
	}
}

func TestBashSession_SetEnv_BASH_FUNC_Prefix(t *testing.T) {
	session := NewBashSession("/tmp")

	// BASH_FUNC_* prefix should be blocked
	err := session.SetEnv("BASH_FUNC_TEST", "evil")
	if err == nil {
		t.Error("SetEnv(BASH_FUNC_TEST) should return error")
	}
}

func TestBashSession_Env(t *testing.T) {
	session := NewBashSession("/tmp")

	session.SetEnv("VAR1", "value1")
	session.SetEnv("VAR2", "value2")

	env := session.Env()

	if len(env) != 2 {
		t.Errorf("len(env) = %v, want %v", len(env), 2)
	}
	if env["VAR1"] != "value1" || env["VAR2"] != "value2" {
		t.Errorf("env = %v, want {VAR1: value1, VAR2: value2}", env)
	}

	// Verify it's a copy (modifying env doesn't affect session)
	env["VAR1"] = "modified"
	sessionEnv := session.Env()
	if sessionEnv["VAR1"] != "value1" {
		t.Error("Env() should return a copy, not modify internal state")
	}
}

// ============================================================
// BashTool Tests
// ============================================================

func TestNewBashTool(t *testing.T) {
	tool := NewBashTool("/tmp")

	if tool == nil {
		t.Fatal("NewBashTool() returned nil")
	}
	if tool.workDir != "/tmp" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/tmp")
	}
	if tool.session == nil {
		t.Error("session is nil")
	}
	if tool.timeout != DefaultBashTimeout {
		t.Errorf("timeout = %v, want %v", tool.timeout, DefaultBashTimeout)
	}
}

func TestBashTool_Name(t *testing.T) {
	tool := NewBashTool("/tmp")
	if tool.Name() != "bash" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "bash")
	}
}

func TestBashTool_Description(t *testing.T) {
	tool := NewBashTool("/tmp")
	desc := tool.Description()

	if desc == "" {
		t.Error("Description() is empty")
	}
}

func TestBashTool_Declaration(t *testing.T) {
	tool := NewBashTool("/tmp")
	decl := tool.Declaration()

	if decl == nil {
		t.Fatal("Declaration() is nil")
	}
	if decl.Name != "bash" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "bash")
	}
}

func TestBashTool_Validate(t *testing.T) {
	tool := NewBashTool("/tmp")

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"valid command", map[string]any{"command": "ls"}, false},
		{"missing command", map[string]any{}, true},
		{"empty command", map[string]any{"command": ""}, true},
		{"nil args", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tool.Validate(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// ============================================================
// SafeEnvVars Tests
// ============================================================

func TestSafeEnvVars_ContainsExpected(t *testing.T) {
	expected := []string{"PATH", "HOME", "USER", "SHELL", "TERM"}

	for _, key := range expected {
		found := false
		for _, safe := range SafeEnvVars {
			if safe == key {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected %q in SafeEnvVars", key)
		}
	}
}

func TestSafeEnvVars_Len(t *testing.T) {
	if len(SafeEnvVars) < 10 {
		t.Error("SafeEnvVars seems too short")
	}
}

// ============================================================
// DefaultBashTimeout Tests
// ============================================================

func TestDefaultBashTimeout(t *testing.T) {
	if DefaultBashTimeout < time.Second {
		t.Error("DefaultBashTimeout seems too short")
	}
	if DefaultBashTimeout > 10*time.Minute {
		t.Error("DefaultBashTimeout seems too long")
	}
}

// ============================================================
// BashTool Execute Tests
// ============================================================

func TestBashTool_Execute_SimpleCommand(t *testing.T) {
	tool := NewBashTool("/tmp")

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"command": "echo hello",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestBashTool_Execute_WithTimeout(t *testing.T) {
	tool := NewBashTool("/tmp")
	tool.timeout = 1 * time.Second

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"command": "sleep 0.5 && echo done",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestBashTool_Execute_TimeoutExceeded(t *testing.T) {
	tool := NewBashTool("/tmp")
	tool.timeout = 100 * time.Millisecond

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"command": "sleep 10",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Command should timeout
	if result.Success {
		t.Error("Execute() should fail for timeout")
	}
}

func TestBashTool_Execute_ExitCode(t *testing.T) {
	tool := NewBashTool("/tmp")

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"command": "exit 1",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Exit code 1 should result in failed result
	if result.Success {
		t.Error("Execute() should return failure for exit 1")
	}
	if !strings.Contains(result.Content, "Actionable summary:") {
		t.Fatalf("failure content missing actionable summary: %q", result.Content)
	}
	if !strings.Contains(result.Content, "exit code 1") {
		t.Fatalf("failure content missing exit code: %q", result.Content)
	}
}

func TestBashTool_Execute_TestFailureSuggestsFocusedRerun(t *testing.T) {
	tool := NewBashTool("/tmp")

	result, err := tool.Execute(context.Background(), map[string]any{
		"command": "go test ./definitely-not-a-package",
	})
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("Execute() should fail for invalid go test package")
	}
	if !strings.Contains(result.Content, "rerun this same validation command") {
		t.Fatalf("failure content missing validation next step: %q", result.Content)
	}
}

func TestBashTool_Execute_Pwd(t *testing.T) {
	tool := NewBashTool("/tmp")

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"command": "pwd",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
	if result.Content == "" {
		t.Error("Execute() should return content from pwd")
	}
}

func TestBashTool_Execute_EnvVar(t *testing.T) {
	tool := NewBashTool("/tmp")

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"command": "env",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

// ============================================================
// BashTool Configuration Tests
// ============================================================

func TestBashTool_SessionPersist(t *testing.T) {
	tool := NewBashTool("/tmp")

	// Verify session exists and can be used
	if tool.session == nil {
		t.Error("session is nil")
	}

	// Set work dir via session
	tool.session.SetWorkDir("/tmp")

	if tool.session.WorkDir() != "/tmp" {
		t.Error("Session work dir not set correctly")
	}
}

func TestBashTool_SetTimeout(t *testing.T) {
	tool := NewBashTool("/tmp")

	tool.timeout = 5 * time.Second

	if tool.timeout != 5*time.Second {
		t.Errorf("timeout = %v, want %v", tool.timeout, 5*time.Second)
	}
}

// ============================================================
// Edge Cases
// ============================================================

func TestBashTool_Execute_EmptyResult(t *testing.T) {
	tool := NewBashTool("/tmp")

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"command": "true",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestBashTool_Execute_CommandNotFound(t *testing.T) {
	tool := NewBashTool("/tmp")

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"command": "nonexistent_command_xyz",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Command not found should fail
	if result.Success {
		t.Error("Execute() should fail for nonexistent command")
	}
}

func TestBashTool_Execute_SyntaxError(t *testing.T) {
	tool := NewBashTool("/tmp")

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"command": "echo $(unclosed",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Syntax error should result in failure
	if result.Success {
		t.Error("Execute() should fail for syntax error")
	}
}

// TestStartCommandErrorResult_ClassifiesContextErrors pins that a cmd.Start()
// failure caused by an expired/cancelled context is reported honestly, NOT as
// "could not run command (check that the command exists)". A trivial command
// (git stash list) failing in ~5ms with "context deadline exceeded" used to
// surface that misleading message, sending users and the model chasing a
// phantom bad-command problem.
func TestStartCommandErrorResult_ClassifiesContextErrors(t *testing.T) {
	bg := context.Background()

	// Deadline exceeded — surfaced as a timeout, with a config hint, never the
	// misleading "check that the command exists" suffix.
	r := startCommandErrorResult(bg, context.DeadlineExceeded)
	if !strings.Contains(r.Error, "timeout") {
		t.Errorf("deadline error should mention timeout, got: %q", r.Error)
	}
	if strings.Contains(r.Error, "check that the command exists") {
		t.Errorf("deadline error must not blame the command, got: %q", r.Error)
	}

	// Cancelled.
	r = startCommandErrorResult(bg, context.Canceled)
	if !strings.Contains(strings.ToLower(r.Error), "cancel") {
		t.Errorf("cancel error should mention cancellation, got: %q", r.Error)
	}
	if strings.Contains(r.Error, "check that the command exists") {
		t.Errorf("cancel error must not blame the command, got: %q", r.Error)
	}

	// An already-done context is detected even when err itself is generic.
	cancelledCtx, cancel := context.WithCancel(bg)
	cancel()
	r = startCommandErrorResult(cancelledCtx, errors.New("signal: killed"))
	if strings.Contains(r.Error, "check that the command exists") {
		t.Errorf("done context must be classified as cancellation, got: %q", r.Error)
	}

	// A genuine exec failure (live context) keeps the actionable command hint.
	r = startCommandErrorResult(bg, errors.New("exec: \"nope\": executable file not found in $PATH"))
	if !strings.Contains(r.Error, "check that the command exists") {
		t.Errorf("genuine exec failure should keep the command-exists hint, got: %q", r.Error)
	}
}
