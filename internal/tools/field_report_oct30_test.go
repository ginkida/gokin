package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestDeferredMetaToolsHaveSafetyMetadata(t *testing.T) {
	v := NewDefaultSafetyValidator()
	for _, name := range []string{"tool_search", "use_tool"} {
		check, err := v.ValidateSafety(context.Background(), name, map[string]any{"query": "git"})
		if err != nil {
			t.Fatal(err)
		}
		for _, warning := range check.Warnings {
			if strings.Contains(warning, "no safety metadata") {
				t.Errorf("%s: %s", name, warning)
			}
		}
	}
}

func TestHeadlessDenialKeepsSpecificCauseForModelOnly(t *testing.T) {
	ctx, state := ContextWithHeadlessPermissions(context.Background(), false, "dontAsk")
	cause := "Path /home/u/projects/other/x.go is outside the workspace /w and is NOT granted."
	result := ApplyHeadlessPermissionResult(ctx, &genai.FunctionCall{Name: "read", ID: "c1"},
		NewPolicyBlockedResult(PolicyBlockPermission, cause))
	if result.Success || !strings.Contains(result.Error, cause) || !strings.Contains(result.Error, "dontAsk") {
		t.Fatalf("model must see the specific cause and the mode: %q", result.Error)
	}
	denials := state.Denials()
	if len(denials) != 1 || strings.Contains(denials[0].Reason, "/home/u") {
		t.Fatalf("published denial must keep only the generic reason: %+v", denials)
	}
}

func TestOutOfWorkspaceDenialPointsAtRelativePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := registry.Register(NewReadTool(root)); err != nil {
		t.Fatal(err)
	}
	e := NewExecutor(registry, nil, 0)
	e.SetWorkDir(root)
	e.SetDirAccessChecker(func(string) bool { return false })
	e.SetDirGrantHandler(func(context.Context, string, string) (bool, error) { return false, nil })
	outside := filepath.Join(filepath.Dir(root), "sibling", "a.go")
	result := e.doExecuteTool(context.Background(), &genai.FunctionCall{Name: "read", Args: map[string]any{"file_path": outside}})
	if result.Success || !strings.Contains(result.Error, "relative to the workspace") || !strings.Contains(result.Error, root) {
		t.Fatalf("denial must name the workspace and suggest a relative path: %q", result.Error)
	}
	if strings.Contains(result.Error, "rewriting the path will not help") {
		t.Fatalf("denial still tells the model a corrected path cannot help: %q", result.Error)
	}
}
