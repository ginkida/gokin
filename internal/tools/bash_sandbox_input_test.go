package tools

import (
	"runtime"
	"strings"
	"testing"
)

func TestSandboxedBashExecutesDeclaredStdinAndUsesExistingTempDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sandbox requires bash")
	}
	tool := NewBashTool(t.TempDir())
	tool.SetSandboxEnabled(true)
	input := "literal tool input\n"
	result, err := tool.Execute(t.Context(), map[string]any{
		"command": `test -d "$TMPDIR" && cat`, "stdin": input,
	})
	if strings.Contains(result.Error, "operation not permitted") && runtime.GOOS == "linux" {
		t.Skip("host cannot create sandbox namespaces")
	}
	if err != nil || !result.Success || result.Content != input {
		t.Fatalf("declared sandbox stdin was not delivered: result=%+v error=%v", result, err)
	}
}
