package tools

import (
	"context"
	"strings"
	"testing"
)

func TestBashBackgroundPolicyDisablesSchemaValidationAndExecution(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	if _, ok := tool.Declaration().Parameters.Properties["run_in_background"]; !ok {
		t.Fatal("interactive/default bash declaration lost background option")
	}

	tool.SetBackgroundAllowed(false)
	if _, ok := tool.Declaration().Parameters.Properties["run_in_background"]; ok {
		t.Fatal("foreground-only bash declaration still advertises background execution")
	}
	if strings.Contains(tool.Description(), "Use run_in_background=true") {
		t.Fatalf("foreground-only description still recommends background mode: %q", tool.Description())
	}
	args := map[string]any{"command": "echo safe", "run_in_background": true}
	if err := tool.Validate(args); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("Validate error = %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success || result.PolicyBlock == nil || result.PolicyBlock.Kind != PolicyBlockSafety {
		t.Fatalf("background execution was not fail-closed: %+v", result)
	}

	tool.SetBackgroundAllowed(true)
	if _, ok := tool.Declaration().Parameters.Properties["run_in_background"]; !ok {
		t.Fatal("re-enabled bash declaration did not restore background option")
	}
}
