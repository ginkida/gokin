package tools

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gokin/internal/permission"
)

func TestExecutorDontAskBlocksPromptRequiredToolWithoutExecuting(t *testing.T) {
	registry := NewRegistry()
	writeRan := false
	readRan := false
	registry.MustRegister(&scriptedTool{name: "write", ran: &writeRan})
	registry.MustRegister(&scriptedTool{name: "read", ran: &readRan})

	manager := permission.NewManager(permission.DefaultRules(), true)
	manager.SetDontAsk(true)
	executor := NewExecutor(registry, nil, time.Second)
	executor.SetPermissions(manager)

	blocked := executor.doExecuteTool(context.Background(), testFunctionCall(
		"blocked-write", "write", map[string]any{"file_path": "blocked.go"},
	))
	if blocked.Success || blocked.PolicyBlock == nil ||
		blocked.PolicyBlock.Kind != PolicyBlockPermission || writeRan {
		t.Fatalf("dontAsk write result=%+v ran=%v", blocked, writeRan)
	}

	allowed := executor.doExecuteTool(context.Background(), testFunctionCall(
		"allowed-read", "read", map[string]any{"file_path": "main.go"},
	))
	if !allowed.Success || !readRan {
		t.Fatalf("dontAsk read result=%+v ran=%v", allowed, readRan)
	}
}

func TestExecutorBindsScopedPermissionRulesToItsWorkDir(t *testing.T) {
	workDir := t.TempDir()
	registry := NewRegistry()
	writeRan := false
	registry.MustRegister(&scriptedTool{name: "write", ran: &writeRan})
	manager := permission.NewManager(permission.DefaultRules(), true)
	manager.SetDontAsk(true)
	manager.SetRunToolRules([]string{"edit(/allowed/**)"}, nil)
	executor := NewExecutor(registry, nil, time.Second)
	executor.SetPermissions(manager)
	executor.SetWorkDir(workDir)

	allowed := executor.doExecuteTool(context.Background(), testFunctionCall(
		"allowed-write", "write",
		map[string]any{"file_path": filepath.Join(workDir, "allowed", "main.go")},
	))
	if !allowed.Success || !writeRan {
		t.Fatalf("scoped write result=%+v ran=%v", allowed, writeRan)
	}

	writeRan = false
	blocked := executor.doExecuteTool(context.Background(), testFunctionCall(
		"outside-write", "write",
		map[string]any{"file_path": filepath.Join(t.TempDir(), "outside.go")},
	))
	if blocked.Success || blocked.PolicyBlock == nil || writeRan {
		t.Fatalf("outside scoped write result=%+v ran=%v", blocked, writeRan)
	}
}
