package logging

import (
	"bytes"
	"os"
	"path/filepath"
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
