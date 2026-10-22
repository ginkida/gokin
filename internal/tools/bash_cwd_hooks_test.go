package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gokin/internal/hooks"
)

func TestBashCwdNotificationsUseCommittedState(t *testing.T) {
	for _, sandbox := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("sandbox=%t/stream=%t", sandbox, stream), func(t *testing.T) {
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				child := filepath.Join(root, "child with 'quotes\nand newline\n")
				if err := os.MkdirAll(child, 0700); err != nil {
					t.Fatal(err)
				}
				for _, test := range []struct {
					command          string
					changed, success bool
				}{
					{"pwd", false, true},
					{"cd missing", false, false},
					{"(cd " + quoteBashTest(child) + "; pwd)", false, true},
					{"printf '%s\\n' " + quoteBashTest(pwdMarker+child) + "; exit 0", false, true},
					{"cd " + quoteBashTest(child), true, true},
					{"cd " + quoteBashTest(child) + "; false", true, false},
					{"cd " + quoteBashTest(child) + "; exit 7", true, false},
					{"cd " + quoteBashTest(child) + "; exec true", true, true},
					{"set -T; (cd " + quoteBashTest(child) + "; true); exec true", false, true},
				} {
					tool := NewBashTool(root)
					tool.SetSandboxEnabled(sandbox)
					count := 0
					ctx := hooks.ContextWithCwdChanges(t.Context(), func(_ context.Context, old, new string) {
						count++
						if old != root || new != child || tool.WorkingDirectory() != child {
							t.Errorf("invalid cwd callback: %q -> %q", old, new)
						}
					})
					if stream {
						ctx = ContextWithProgressCallback(ctx, func(float64, string) {})
					}
					result, err := tool.Execute(ctx, map[string]any{"command": test.command})
					if sandbox && runtime.GOOS == "linux" && strings.Contains(result.Error, "operation not permitted") {
						t.Skip("host cannot create sandbox namespaces")
					}
					if err != nil || result.Success != test.success || (count == 1) != test.changed || count > 1 {
						t.Fatalf("bad cwd/status for %q: result=%+v error=%v callbacks=%d", test.command, result, err, count)
					}
				}
			})
		}
	}
}

func quoteBashTest(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func TestBashCwdDoesNotPublishRejectedPolicyView(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	tool := NewBashTool(root)
	tool.policyMu.RLock()
	view, revision := tool.executionViewLocked()
	tool.policyMu.RUnlock()
	view.session.SetWorkDir(child)
	tool.policyMu.RLock()
	sandbox := tool.sandboxEnabled
	tool.policyMu.RUnlock()
	tool.SetSandboxEnabled(!sandbox)
	called := false
	ctx := hooks.ContextWithCwdChanges(t.Context(), func(context.Context, string, string) { called = true })
	tool.commitExecutionSessionWithContext(ctx, view, revision)
	if called || tool.WorkingDirectory() != root {
		t.Fatal("stale policy changed accepted cwd")
	}
}
