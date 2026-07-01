package agent

import (
	"context"
	"testing"
	"time"

	"gokin/internal/tools"

	"google.golang.org/genai"
)

// slowTestTool ignores ctx cancellation entirely (mirrors a blocking syscall
// with no ctx-awareness, e.g. a plain os.ReadFile on a hung mount) and only
// returns after finishAfter, signaling doneCh when it does.
type slowTestTool struct {
	name        string
	finishAfter time.Duration
	doneCh      chan struct{}
}

func (s *slowTestTool) Name() string        { return s.name }
func (s *slowTestTool) Description() string { return "test-only slow tool" }
func (s *slowTestTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: s.name}
}
func (s *slowTestTool) Validate(args map[string]any) error { return nil }
func (s *slowTestTool) Execute(ctx context.Context, args map[string]any) (tools.ToolResult, error) {
	time.Sleep(s.finishAfter)
	close(s.doneCh)
	return tools.NewSuccessResult("slow tool finished"), nil
}

type fastTestTool struct{ name string }

func (f *fastTestTool) Name() string        { return f.name }
func (f *fastTestTool) Description() string { return "test-only fast tool" }
func (f *fastTestTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: f.name}
}
func (f *fastTestTool) Validate(args map[string]any) error { return nil }
func (f *fastTestTool) Execute(ctx context.Context, args map[string]any) (tools.ToolResult, error) {
	return tools.NewSuccessResult("fast tool done"), nil
}

// TestExecuteToolsParallel_StragglerLeavesValidPlaceholder pins the fix for
// the nil-deref+race bug: when ctx is cancelled while a tool goroutine is
// stuck deep inside a non-ctx-aware call, executeToolsParallel must NOT
// leave that call's results[] slot at the zero value (Response == nil) —
// the caller (executeLoop) dereferences result.Response.Name unconditionally
// on every slot. It must also never let the straggler's eventual late write
// land in `results` after the caller has moved on (a genuine data race,
// since the caller reads `results` unsynchronized).
func TestExecuteToolsParallel_StragglerLeavesValidPlaceholder(t *testing.T) {
	origGrace := parallelToolCleanupGrace
	parallelToolCleanupGrace = 30 * time.Millisecond
	defer func() { parallelToolCleanupGrace = origGrace }()

	reg := tools.NewRegistry()
	slowDone := make(chan struct{})
	if err := reg.Register(&slowTestTool{name: "slow_tool", finishAfter: 150 * time.Millisecond, doneCh: slowDone}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(&fastTestTool{name: "fast_tool"}); err != nil {
		t.Fatal(err)
	}

	a := &Agent{registry: reg}

	calls := []*genai.FunctionCall{
		{ID: "1", Name: "slow_tool", Args: map[string]any{}},
		{ID: "2", Name: "fast_tool", Args: map[string]any{}},
	}
	results := make([]toolCallResult, len(calls))
	indexMap := map[*genai.FunctionCall]int{calls[0]: 0, calls[1]: 1}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel shortly after the goroutines have started (letting the slow
	// tool's Execute begin its ctx-ignoring sleep) so the cleanup grace
	// (30ms) fires while it's still stuck — reproducing the straggler case.
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	a.executeToolsParallel(ctx, calls, results, indexMap)

	// Immediately after return: neither slot may be nil. Pre-fix, the slow
	// slot stayed at the zero-valued toolCallResult{} (Response == nil)
	// until the straggler goroutine eventually wrote — which hadn't
	// happened yet at this point (~40ms in, tool finishes at 150ms).
	if results[0].Response == nil {
		t.Fatal("slow tool's result slot is nil immediately after executeToolsParallel returns — the caller's unconditional result.Response.Name dereference would panic")
	}
	if results[1].Response == nil {
		t.Fatal("fast tool's result slot is nil")
	}
	if results[0].Response.Response["error"] != "cancelled" {
		t.Errorf("slow tool's slot should hold the cancelled placeholder immediately after return, got %+v", results[0].Response.Response)
	}

	// Read results[0] repeatedly WITHOUT synchronization for the remainder
	// of the slow tool's runtime — mirrors the real caller's unsynchronized
	// read in executeLoop. Run this test with -race: a genuine late write
	// from the straggler goroutine into this slice would be flagged.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = results[0].Response.Name
		time.Sleep(time.Millisecond)
	}

	<-slowDone // the straggler did eventually finish its (ignored) work

	// Even after the straggler completed, its result must NOT have been
	// written into `results` post-abandonment — the slot stays frozen at
	// the placeholder from the moment executeToolsParallel returned.
	if results[0].Response.Response["error"] != "cancelled" {
		t.Errorf("straggler's late result leaked into `results` after abandonment: %+v", results[0].Response.Response)
	}
}

// TestExecuteToolsParallel_NormalCompletionUnaffected: the fix must not
// change the happy path — all tools finish well within the grace period.
func TestExecuteToolsParallel_NormalCompletionUnaffected(t *testing.T) {
	reg := tools.NewRegistry()
	if err := reg.Register(&fastTestTool{name: "fast_tool"}); err != nil {
		t.Fatal(err)
	}

	a := &Agent{registry: reg}

	calls := []*genai.FunctionCall{{ID: "1", Name: "fast_tool", Args: map[string]any{}}}
	results := make([]toolCallResult, len(calls))
	indexMap := map[*genai.FunctionCall]int{calls[0]: 0}

	a.executeToolsParallel(context.Background(), calls, results, indexMap)

	if results[0].Response == nil {
		t.Fatal("result slot is nil")
	}
	if results[0].Response.Response["content"] != "fast tool done" {
		t.Errorf("got %+v, want the real tool result", results[0].Response.Response)
	}
}
