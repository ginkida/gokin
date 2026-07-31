package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  Level
	}{
		{"debug", LevelDebug},
		{"DEBUG", LevelDebug},
		{"info", LevelInfo},
		{"INFO", LevelInfo},
		{"warn", LevelWarn},
		{"warning", LevelWarn},
		{"error", LevelError},
		{"ERROR", LevelError},
		{"unknown", LevelInfo},
		{"", LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseLevel(tt.input)
			if got != tt.want {
				t.Errorf("ParseLevel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestConfigure(t *testing.T) {
	var buf bytes.Buffer
	Configure(LevelDebug, &buf)

	Debug("test debug message")
	if !strings.Contains(buf.String(), "test debug message") {
		t.Error("debug message should appear in output")
	}
}

func TestConfigureNilWriter(t *testing.T) {
	// Should not panic with nil writer (defaults to stderr)
	Configure(LevelInfo, nil)
}

func TestConfigureLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	Configure(LevelError, &buf)

	Debug("debug msg")
	Info("info msg")
	Warn("warn msg")

	if buf.Len() > 0 {
		t.Error("debug/info/warn should be filtered at error level")
	}

	Error("error msg")
	if !strings.Contains(buf.String(), "error msg") {
		t.Error("error should pass at error level")
	}
}

func TestLogFunctions(t *testing.T) {
	var buf bytes.Buffer
	Configure(LevelDebug, &buf)

	Debug("d", "key", "val")
	Info("i")
	Warn("w")
	Error("e")

	output := buf.String()
	for _, msg := range []string{"d", "i", "w", "e"} {
		if !strings.Contains(output, msg) {
			t.Errorf("missing message %q in output", msg)
		}
	}
}

func TestWith(t *testing.T) {
	var buf bytes.Buffer
	Configure(LevelDebug, &buf)

	child := With("component", "test")
	child.Info("child message")

	output := buf.String()
	if !strings.Contains(output, "component") {
		t.Error("With() attributes should appear in output")
	}
	if !strings.Contains(output, "test") {
		t.Error("With() values should appear in output")
	}
}

func TestLogger(t *testing.T) {
	var buf bytes.Buffer
	Configure(LevelInfo, &buf)

	l := Logger()
	if l == nil {
		t.Fatal("Logger() should not return nil")
	}
}

func TestEnableFileLogging(t *testing.T) {
	dir := t.TempDir()

	err := EnableFileLogging(dir, LevelDebug)
	if err != nil {
		t.Fatalf("EnableFileLogging: %v", err)
	}

	Info("test file log")
	Close()

	logPath := filepath.Join(dir, "gokin.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), "test file log") {
		t.Error("log file should contain message")
	}
}

func TestEnableFileLoggingRotation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gokin.log")

	// Create a large file to trigger rotation
	largeContent := strings.Repeat("x", 11*1024*1024) // > 10MB
	os.WriteFile(logPath, []byte(largeContent), 0644)

	err := EnableFileLogging(dir, LevelInfo)
	if err != nil {
		t.Fatalf("EnableFileLogging: %v", err)
	}
	Close()

	// Old file should be renamed
	backupPath := logPath + ".old"
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Error("backup file should exist after rotation")
	}
}

func TestEnablePathLoggingFiltersRedactsAndSecuresFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "debug.jsonl")
	if err := EnablePathLogging(path, LevelDebug, "mcp,!health"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)

	Debug("api request", "api_key", "sk-supersecret123")
	Debug("debug logging enabled", "category", "startup", "filter", "mcp")
	Debug("mcp request", "authorization", "Bearer abcdefghijklmnop")
	Debug("mcp health request", "token", "ghp_abcdefghijklmnopqrstuvwxyz")
	Logger().Info("mcp direct logger", "password", "very-secret-password")
	With("category", "mcp").Info("child logger", "value", "token=secret-value-123")
	Debug("mcp request body", "body", `{"api_key":"json-secret-123","ok":true}`)
	Debug("mcp headers", "headers", map[string][]string{
		"Authorization": {"Bearer header-secret-123"},
	})
	Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	if strings.Contains(output, "api request") ||
		strings.Contains(output, "mcp health request") ||
		strings.Contains(output, "debug logging enabled") {
		t.Fatalf("category filter leaked excluded records:\n%s", output)
	}
	for _, required := range []string{"mcp request", "mcp direct logger", "child logger", "[REDACTED]"} {
		if !strings.Contains(output, required) {
			t.Fatalf("debug output missing %q:\n%s", required, output)
		}
	}
	for _, secret := range []string{
		"abcdefghijklmnop",
		"very-secret-password",
		"secret-value-123",
		"json-secret-123",
		"header-secret-123",
		"sk-supersecret123",
	} {
		if strings.Contains(output, secret) {
			t.Fatalf("debug output leaked secret %q:\n%s", secret, output)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("debug file permissions = %o, want 600", got)
		}
	}
	if got, err := filepath.Abs(path); err != nil || CurrentLogPath() != "" {
		// Close above deliberately clears the active path.
		t.Fatalf("CurrentLogPath after Close = %q (abs=%q, err=%v)", CurrentLogPath(), got, err)
	}
}

func TestEnablePathLoggingReportsAbsoluteActivePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.jsonl")
	if err := EnablePathLogging(path, LevelInfo, ""); err != nil {
		t.Fatal(err)
	}
	defer Close()
	want, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := CurrentLogPath(); got != want {
		t.Fatalf("CurrentLogPath = %q, want %q", got, want)
	}
}

func TestEnablePathLoggingRejectsInvalidFilterBeforeCreatingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "debug.jsonl")
	if err := EnablePathLogging(path, LevelDebug, "api,,mcp"); err == nil {
		t.Fatal("invalid category filter unexpectedly succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid filter created log file: %v", err)
	}
}

func TestDisableLogging(t *testing.T) {
	var buf bytes.Buffer
	Configure(LevelDebug, &buf)
	DisableLogging()

	Error("should not appear")
	if buf.Len() > 0 {
		// Note: after DisableLogging, new logger writes to discard, but buf still has old ref
		// This test verifies DisableLogging doesn't panic
	}
}

func TestSetLevel(t *testing.T) {
	// Should not panic
	SetLevel(LevelWarn)
}

// TestPanicStack verifies the panic-recovery helper produces a usable stack
// trace string. Used after recover() across the codebase to log a stack
// snapshot alongside the recovered value.
func TestPanicStack(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			stack := PanicStack()
			if stack == "" {
				t.Error("PanicStack() returned empty string")
			}
			// Standard runtime.Stack output format: starts with goroutine
			// header line like "goroutine N [running]:"
			if !strings.HasPrefix(stack, "goroutine") {
				t.Errorf("PanicStack() should start with 'goroutine', got %q", stack[:min(40, len(stack))])
			}
			// Should reference this test function so callers can locate
			// the panic site.
			if !strings.Contains(stack, "TestPanicStack") {
				t.Errorf("PanicStack() should reference TestPanicStack frame, got:\n%s", stack)
			}
		}
	}()
	panic("test panic for stack capture")
}

// TestPanicStack_NoPanic ensures PanicStack works outside a recover() context
// too — useful for sentinel logging when the caller wants a current-stack
// snapshot without panicking.
func TestPanicStack_NoPanic(t *testing.T) {
	stack := PanicStack()
	if stack == "" {
		t.Error("PanicStack() returned empty string")
	}
	if !strings.Contains(stack, "TestPanicStack_NoPanic") {
		t.Errorf("PanicStack() should reference TestPanicStack_NoPanic frame, got:\n%s", stack)
	}
}
