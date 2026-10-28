package tools

import (
	"runtime"
	"testing"
)

func TestSandboxedBashExecutesDeclaredStdinAndUsesExistingTempDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sandbox requires bash")
	}
	tool := NewBashTool(t.TempDir())
	tool.SetSandboxEnabled(true)
	// An unavailable backend runs on the host with the host environment, so
	// this sandbox contract does not apply (e.g. no unprivileged userns).
	if status := tool.sandboxPolicy.Status(t.Context()); status.Status == "unavailable" {
		t.Skipf("OS sandbox unavailable: %s", status.Reason)
	}
	input := "literal tool input\n"
	result, err := tool.Execute(t.Context(), map[string]any{
		"command": `test -d "$TMPDIR" && cat`, "stdin": input,
	})
	if err != nil || !result.Success || result.Content != input {
		t.Fatalf("declared sandbox stdin was not delivered: result=%+v error=%v", result, err)
	}
}
