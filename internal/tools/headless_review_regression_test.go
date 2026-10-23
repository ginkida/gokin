package tools

import (
	"path/filepath"
	"testing"

	"gokin/internal/permission"
	"gokin/internal/testkit"
)

func TestHeadlessSkillPreflightCeilingsStayTerminal(t *testing.T) {
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "dynamic", true: "fork"}[fork], func(t *testing.T) {
			var e *Executor
			name := "dynamic"
			if fork {
				e, _, _, _, _ = forkSkillExecutor(t, "background: false", "Review.")
				name = "review"
			} else {
				e, _, _, _ = dynamicSkillExecutor(t, "!`printf proof`", "Bash", "", true)
			}
			ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "default")
			ctx = ContextWithToolCapabilityCeiling(ctx, []string{"skill"})
			r, err := e.InvokeTool(ctx, "skill", map[string]any{"name": name})
			if err != nil || r.Success || r.PolicyBlock == nil || !r.PolicyBlock.Terminal || len(state.Denials()) != 0 {
				t.Fatalf("terminal ceiling lost: result=%+v err=%v denials=%v", r, err, state.Denials())
			}
		})
	}
}

// Keep a direct dispatcher regression as well as the app hook integration.
func TestStrictInvokeToolStopsBeforeDispatch(t *testing.T) {
	registry := NewRegistry()
	work := testkit.ResolvedTempDir(t)
	registry.MustRegister(NewWriteTool(work))
	e := NewExecutor(registry, nil, 0)
	m := permission.NewManager(nil, true)
	m.SetRunToolRules(nil, []string{"write"})
	e.SetPermissions(m)
	ctx, state := ContextWithHeadlessPermissions(t.Context(), true, "dontAsk")
	_, _ = e.InvokeTool(ctx, "write", map[string]any{"file_path": filepath.Join(work, "first"), "content": "one"})
	m.SetRunToolRules([]string{"write"}, nil)
	result, err := e.InvokeTool(ctx, "write", map[string]any{"file_path": filepath.Join(work, "second"), "content": "two"})
	if err == nil || result.Success || len(state.Denials()) != 1 {
		t.Fatalf("dispatch crossed strict boundary: %+v %v", result, err)
	}
}
