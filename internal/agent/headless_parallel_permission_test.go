package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/hooks"
	"gokin/internal/permission"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"google.golang.org/genai"
)

// Hold read authorization until its parallel sibling has an in-flight verifier.
type verifierOrderedRead struct {
	tools.Tool
	started <-chan struct{}
}

func (r *verifierOrderedRead) Validate(args map[string]any) error {
	if err := r.Tool.Validate(args); err != nil {
		return err
	}
	select {
	case <-r.started:
		return nil
	case <-time.After(3 * time.Second):
		return fmt.Errorf("parallel verifier did not start")
	}
}

func TestStrictParallelDenialLatchesBeforeWorkerJoin(t *testing.T) {
	work, root := testkit.ResolvedTempDir(t), t.TempDir()
	skillPath := filepath.Join(root, "guard", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0700); err != nil {
		t.Fatal(err)
	}
	document := "---\ndescription: Verify grep\nhooks:\n  PreToolUse:\n    - matcher: Grep\n      hooks:\n        - type: agent\n          prompt: Verify evidence\n          timeout: 5\n---\nInspect evidence."
	if err := os.WriteFile(skillPath, []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce, sentOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	target := filepath.Join(work, "after-denial.txt")
	verifier := testkit.NewMockClient().EnqueueToolCall("write", map[string]any{"file_path": target, "content": "must not be written"}).EnqueueText(`{"ok":true}`)
	verifier.SetModel("glm-5.3-flash")
	verifier.OnSend = func(ctx context.Context) {
		sentOnce.Do(func() {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
	}
	worker := testkit.NewMockClient().EnqueueToolCall("skill", map[string]any{"name": "guard"}).EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{
		{FunctionCalls: []*genai.FunctionCall{
			{ID: "denied-read", Name: "read", Args: map[string]any{"file_path": filepath.Join(work, "denied.txt")}},
			{ID: "parallel-grep", Name: "grep", Args: map[string]any{"pattern": "proof", "path": work}},
		}}, {Done: true, FinishReason: genai.FinishReasonStop},
	}}).EnqueueText("Finished.")
	worker.SetModel("glm-5.3-flash")
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}), work))
	registry.MustRegister(&verifierOrderedRead{tools.NewReadTool(work), started})
	registry.MustRegister(tools.NewGrepTool(work))
	registry.MustRegister(tools.NewWriteTool(work))
	manager := permission.NewManager(nil, true)
	manager.SetRunToolRules([]string{"write", "grep"}, []string{"read(**/denied.txt)"})
	a := NewAgent(AgentTypeGeneral, &denialHookClient{worker, verifier}, registry, work, 5, "", manager, nil)
	a.SetHooks(hooks.NewManager(true, work))
	ctx, state := tools.ContextWithHeadlessPermissions(t.Context(), true, "dontAsk")
	ctx = tools.ContextWithoutSelfReview(ctx)
	type outcome struct {
		result *AgentResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() { result, err := a.Run(ctx, "Inspect evidence."); done <- outcome{result, err} }()
	select {
	case <-started:
	case <-time.After(4 * time.Second):
		t.Fatal("verifier never started")
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	waiting := true
	for waiting {
		select {
		case <-tick.C:
			if len(state.Denials()) == 1 {
				waiting = false
			}
		case <-deadline.C:
			t.Error("permission denial was not latched while the parallel verifier held the join open")
			waiting = false
		}
	}
	// Early permission state must not move reflection or Agent bookkeeping into workers.
	if a.policyBlockSnapshot() != nil {
		t.Error("Agent policy bookkeeping ran before the worker join")
	}
	for _, name := range a.GetToolsUsed() {
		if name != "skill" {
			t.Errorf("worker finalized Agent bookkeeping before join: %s", name)
		}
	}
	unblock()
	select {
	case outcome := <-done:
		if outcome.err == nil || outcome.result.PolicyBlock == nil {
			t.Fatalf("strict outcome=%+v err=%v", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parallel run did not stop")
	}
	if denials := state.Denials(); len(denials) != 1 || denials[0].ToolUseID != "denied-read" {
		t.Errorf("denials=%v", denials)
	}
	if len(verifier.Calls()) != 1 || len(worker.Calls()) != 2 {
		t.Errorf("requests after denial: verifier=%d worker=%d", len(verifier.Calls()), len(worker.Calls()))
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("parallel verifier wrote after denial: %v", err)
	}
}
