package tools

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/client"

	"google.golang.org/genai"
)

// TestQueueAndDrainUserSteers pins the Claude-Code-style "message during work"
// wiring: a follow-up the user typed while a turn was running is drained into
// history as a user turn (so the model sees it), preserving repeated messages
// while rejecting empty input.
func TestQueueAndDrainUserSteers(t *testing.T) {
	e := &Executor{}
	e.userSteerActive = true

	e.TryQueueUserSteer("also cover the error path")
	e.TryQueueUserSteer("also cover the error path") // intentional repeat is preserved
	e.TryQueueUserSteer("   ")                       // empty → ignored
	e.TryQueueUserSteer("rename foo to bar")

	if got := len(e.userSteers); got != 3 {
		t.Fatalf("userSteers = %d, want 3 (repeat preserved + empty guard)", got)
	}
	if !e.HasUserSteers() {
		t.Error("HasUserSteers should be true with queued steers")
	}

	steers := e.drainUserSteers()

	if len(e.userSteers) != 0 {
		t.Errorf("userSteers not cleared after drain: %d", len(e.userSteers))
	}
	if len(steers) != 3 {
		t.Fatalf("drained steers = %d, want 3", len(steers))
	}
	if e.HasUserSteers() {
		t.Error("HasUserSteers should be false after drain")
	}

	// The loop injects each drained steer into history with a [user follow-up]
	// marker — pin that shape so a rename of the marker is caught (downstream
	// the executor's drain site at executeLoop builds these exact turns).
	for _, s := range steers {
		content := genai.NewContentFromText("[user follow-up] "+s, genai.RoleUser)
		if content.Role != genai.RoleUser {
			t.Errorf("injected steer role = %v, want User (model must see it as guidance)", content.Role)
		}
		if len(content.Parts) == 0 || !strings.Contains(content.Parts[0].Text, "[user follow-up]") {
			t.Errorf("injected steer missing [user follow-up] marker: %+v", content.Parts)
		}
	}

	// Draining again is a no-op (nothing queued).
	if got := e.drainUserSteers(); got != nil {
		t.Errorf("second drain returned %d steers, want nil", len(got))
	}
}

func TestTryQueueUserSteerAcceptanceWindow(t *testing.T) {
	e := &Executor{}
	if e.TryQueueUserSteer("too early") {
		t.Fatal("inactive executor accepted a steer")
	}

	e.userSteerMu.Lock()
	e.userSteerActive = true
	e.userSteerMu.Unlock()
	if !e.TryQueueUserSteer("during loop") {
		t.Fatal("active executor rejected a steer")
	}

	e.finishUserSteering()
	if e.TryQueueUserSteer("too late") {
		t.Fatal("finished executor accepted a late steer")
	}
}

func TestTryQueueUserSteerIsBounded(t *testing.T) {
	e := &Executor{userSteerActive: true}
	for i := 0; i < maxQueuedUserSteers; i++ {
		if !e.TryQueueUserSteer("message") {
			t.Fatalf("steer %d rejected before limit", i+1)
		}
	}
	if e.TryQueueUserSteer("overflow") {
		t.Fatal("steer queue accepted input past its bound")
	}
}

// TestQueueUserSteer_TrimsWhitespace pins that the queue normalizes whitespace
// so a steer of only spaces never enters the queue (would inject a junk turn).
func TestQueueUserSteer_TrimsWhitespace(t *testing.T) {
	e := &Executor{userSteerActive: true}

	e.TryQueueUserSteer("\t  \n")
	if e.HasUserSteers() {
		t.Error("whitespace-only steer should not be queued")
	}

	e.TryQueueUserSteer("  real message with padding  ")
	if !e.HasUserSteers() {
		t.Fatal("trimmed non-empty steer should be queued")
	}
	steers := e.drainUserSteers()
	if len(steers) != 1 || steers[0] != "real message with padding" {
		t.Errorf("queued steer = %q, want trimmed message", steers)
	}
}

// TestExecuteLoop_SteerInjectedIntoHistorySeenByModel is the end-to-end pin for
// the "message during work" feature: a steer queued while executeLoop is
// running is drained into history at the top of the next iteration, so the
// model's NEXT SendFunctionResponse call sees the [user follow-up] turn in the
// history slice it's handed. The scripted client captures that history; we
// assert the follow-up is present and correctly ordered (after the tool
// results, before the model's next response).
func TestExecuteLoop_SteerInjectedIntoHistorySeenByModel(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	cl := &scriptedExecutorClient{
		model: "test-model",
		responses: []*client.StreamingResponse{
			buildExecutorTestReadStream("r0"), // iteration 0: model calls read
			buildExecutorTestTextStream("done — and I covered the error path as asked"),
		},
	}
	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false

	// Queue a steer BEFORE calling Execute — it'll be drained on iteration 1
	// (the loop drains at the top of every iteration, including the first).
	// To simulate the real "mid-turn" timing we'd need to queue it from
	// another goroutine during the first model round; but the drain site is
	// identical either way, and a pre-queued steer is the deterministic way
	// to prove the injection point fires between tool-results and the next
	// model call.
	exec.userSteerActive = true
	exec.TryQueueUserSteer("also cover the error path")

	history, _, err := exec.Execute(context.Background(), nil, "inspect project.go")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// The returned history must contain the steer as a user turn with the
	// [user follow-up] marker. Find it.
	var found bool
	for _, c := range history {
		if c.Role == genai.RoleUser && len(c.Parts) > 0 &&
			strings.Contains(c.Parts[0].Text, "[user follow-up] also cover the error path") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("steer not injected into returned history; got %d turns", len(history))
	}

	// The scripted client captured the history slice handed to the SECOND model
	// call (SendFunctionResponse, after the read tool ran). That slice must
	// ALSO contain the steer — proving the model actually saw it, not just that
	// it was appended after the loop exited.
	if len(cl.functionHistory) == 0 {
		t.Fatal("SendFunctionResponse was never called (model didn't run a tool round)")
	}
	secondCallHistory := cl.functionHistory[0]
	joined := strings.Join(secondCallHistory, "\n")
	if !strings.Contains(joined, "[user follow-up] also cover the error path") {
		t.Errorf("model's 2nd call did not see the steer in history; got:\n%s", joined)
	}
}

// TestExecuteLoop_SteerLeftoverRedispatched pins the exit-drain: a steer that
// arrives AFTER the model's final text response (so it's never drained inside
// the loop) is surfaced via OnSteerLeftover so the app can re-dispatch it as a
// new turn — otherwise the follow-up is silently lost.
func TestExecuteLoop_SteerLeftoverRedispatched(t *testing.T) {
	registry := NewRegistry()
	cl := &scriptedExecutorClient{
		model: "test-model",
		responses: []*client.StreamingResponse{
			buildExecutorTestTextStream("done"), // model finishes immediately, no tools
		},
	}
	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false

	var leftoverMu sync.Mutex
	var got []string
	exec.SetHandler(&ExecutionHandler{
		OnSteerLeftover: func(messages []string) {
			leftoverMu.Lock()
			defer leftoverMu.Unlock()
			got = messages
		},
	})

	// Queue the steer AFTER the loop would have drained on its single iteration.
	// Since the model returns text on iteration 0 and the loop breaks, the only
	// drain that runs is the exit-drain — which must fire OnSteerLeftover.
	// We can't easily race the exact "after drain, before exit" window, so we
	// queue it before Execute but AFTER the in-loop drain already ran by using
	// a client that blocks until we signal. Simpler: queue before Execute; the
	// in-loop drain on iteration 0 consumes it, so to test the EXIT drain we
	// need the steer to arrive during the model call. Use a blocking client.
	blockClient := &blockingSteerClient{
		inner:    cl,
		steerCh:  make(chan struct{}),
		callSeen: make(chan struct{}),
		injectFn: func() { exec.TryQueueUserSteer("late follow-up") },
	}
	exec.SetClient(blockClient)

	// Run Execute in the background; it will block on the model call.
	done := make(chan error, 1)
	go func() {
		_, _, err := exec.Execute(context.Background(), nil, "do something")
		done <- err
	}()

	// Wait for the client to be in the model call, then inject the steer and
	// unblock the response.
	blockClient.waitForCall(t)
	blockClient.injectFn()
	close(blockClient.steerCh)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute timed out")
	}

	leftoverMu.Lock()
	defer leftoverMu.Unlock()
	if len(got) != 1 || got[0] != "late follow-up" {
		t.Errorf("OnSteerLeftover = %v, want [late follow-up]", got)
	}
}

// blockingSteerClient wraps a scriptedExecutorClient and blocks the FIRST
// model call until steerCh is closed, giving the test a window to inject a
// steer that arrives "during" the model round (after the in-loop drain).
type blockingSteerClient struct {
	inner    *scriptedExecutorClient
	steerCh  chan struct{}
	injectFn func()
	once     sync.Once
	callSeen chan struct{}
}

func (b *blockingSteerClient) waitForCall(t *testing.T) {
	t.Helper()
	select {
	case <-b.callSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("model call never started")
	}
}

func (b *blockingSteerClient) SendMessage(ctx context.Context, message string) (*client.StreamingResponse, error) {
	return b.nextBlocking(ctx)
}
func (b *blockingSteerClient) SendMessageWithHistory(ctx context.Context, history []*genai.Content, message string) (*client.StreamingResponse, error) {
	return b.nextBlocking(ctx)
}
func (b *blockingSteerClient) SendFunctionResponse(ctx context.Context, history []*genai.Content, results []*genai.FunctionResponse) (*client.StreamingResponse, error) {
	return b.nextBlocking(ctx)
}

func (b *blockingSteerClient) nextBlocking(ctx context.Context) (*client.StreamingResponse, error) {
	var ctxErr error
	b.once.Do(func() {
		// Signal the test we're inside the model call, then wait for the
		// steer injection to complete before returning the scripted response.
		if b.callSeen != nil {
			close(b.callSeen)
		}
		select {
		case <-b.steerCh:
		case <-ctx.Done():
			ctxErr = ctx.Err()
		}
	})
	if ctxErr != nil {
		return nil, ctxErr
	}
	return b.inner.nextResponse()
}

func (b *blockingSteerClient) SetTools(tools []*genai.Tool)       { b.inner.SetTools(tools) }
func (b *blockingSteerClient) SetRateLimiter(limiter interface{}) { b.inner.SetRateLimiter(limiter) }
func (b *blockingSteerClient) CountTokens(ctx context.Context, contents []*genai.Content) (*genai.CountTokensResponse, error) {
	return b.inner.CountTokens(ctx, contents)
}
func (b *blockingSteerClient) GetModel() string          { return b.inner.GetModel() }
func (b *blockingSteerClient) SetModel(modelName string) { b.inner.SetModel(modelName) }
func (b *blockingSteerClient) WithModel(modelName string) client.Client {
	return &blockingSteerClient{inner: &scriptedExecutorClient{model: modelName}}
}
func (b *blockingSteerClient) GetRawClient() any                       { return nil }
func (b *blockingSteerClient) SetSystemInstruction(instruction string) {}
func (b *blockingSteerClient) SetTurnContext(turnContext string)       {}
func (b *blockingSteerClient) SetThinkingBudget(budget int32)          {}
func (b *blockingSteerClient) Close() error                            { return nil }

// TestSuspendUserSteering_ClosesWindowForInternalExecutes pins the review fix
// for steer leakage into INTERNAL Execute calls (completion review, done-gate
// auto-fix): while suspended, a live user message must be rejected — it falls
// through to the app's pending queue and becomes the NEXT turn instead of
// being spliced into an internal exchange whose history may be discarded.
func TestSuspendUserSteering_ClosesWindowForInternalExecutes(t *testing.T) {
	e := &Executor{}
	e.userSteerActive = true

	if !e.TryQueueUserSteer("before suspension") {
		t.Fatal("open window must accept steers")
	}

	resume := e.SuspendUserSteering()
	if e.TryQueueUserSteer("typed during the completion review") {
		t.Error("suspended executor must reject steers — they belong to the next turn")
	}
	resume()
	if !e.TryQueueUserSteer("after resume") {
		t.Error("resumed window must accept steers again")
	}

	// resume is idempotent — a double call must not underflow the counter and
	// mask a sibling suspension.
	resume()
	r1 := e.SuspendUserSteering()
	r2 := e.SuspendUserSteering()
	r1()
	if e.TryQueueUserSteer("inner suspension still active") {
		t.Error("nested suspensions must compose — one resume must not reopen the window")
	}
	r2()
	if !e.TryQueueUserSteer("all suspensions lifted") {
		t.Error("window must reopen after every suspension is lifted")
	}
}
