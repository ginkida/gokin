package loops

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSpawner records calls and returns canned responses. Lets tests
// drive the Runner without spinning up a real LLM call.
type fakeSpawner struct {
	mu       sync.Mutex
	calls    []string // captured prompts
	output   string
	ok       bool
	err      error
	delay    time.Duration
	callback func(prompt string) // optional per-call hook
}

func (f *fakeSpawner) spawn(ctx context.Context, prompt string) (string, bool, error) {
	f.mu.Lock()
	f.calls = append(f.calls, prompt)
	cb := f.callback
	out := f.output
	ok := f.ok
	err := f.err
	delay := f.delay
	f.mu.Unlock()

	if cb != nil {
		cb(prompt)
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return "", false, ctx.Err()
		}
	}
	return out, ok, err
}

func (f *fakeSpawner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// fakeIdle returns whatever the test sets via .set(true/false).
type fakeIdle struct{ idle atomic.Bool }

func (f *fakeIdle) check() bool { return f.idle.Load() }
func (f *fakeIdle) set(v bool)  { f.idle.Store(v) }

func TestRunner_FiresDueLoop(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, err := mgr.Add("test task", ModeSelfPaced, 0)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &fakeSpawner{output: "Did the thing.", ok: true}
	idle := &fakeIdle{}
	idle.set(true)

	r := NewRunner(mgr, spawner.spawn, idle.check)
	r.SetPeriod(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	r.Start(ctx)
	defer r.Stop()

	// Wait for at least one tick to fire the iteration.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if spawner.callCount() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if spawner.callCount() == 0 {
		t.Fatal("Spawner was never called for due loop")
	}

	got, _ := mgr.Get(l.ID)
	if got.IterationCount != 1 {
		t.Errorf("IterationCount = %d, want 1", got.IterationCount)
	}
	if !strings.Contains(got.Iterations[0].Summary, "Did the thing") {
		t.Errorf("Summary not captured: %q", got.Iterations[0].Summary)
	}
}

func TestRunner_SkipsWhenBusy(t *testing.T) {
	mgr := NewManager(newMemStorage())
	_, _ = mgr.Add("test task", ModeSelfPaced, 0)

	spawner := &fakeSpawner{output: "summary", ok: true}
	idle := &fakeIdle{}
	idle.set(false) // not idle — runner must skip

	r := NewRunner(mgr, spawner.spawn, idle.check)
	r.SetPeriod(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	r.Start(ctx)
	defer r.Stop()

	time.Sleep(250 * time.Millisecond)

	if spawner.callCount() != 0 {
		t.Errorf("Spawner fired %d times despite not-idle, want 0", spawner.callCount())
	}
}

func TestRunner_OnePerTick(t *testing.T) {
	// Even with 5 due loops, tick should fire only one to leave room
	// for foreground work. Repeated ticks pick up the rest.
	mgr := NewManager(newMemStorage())
	for range 5 {
		_, _ = mgr.Add("task", ModeSelfPaced, 0)
	}

	spawner := &fakeSpawner{output: "ok", ok: true}
	idle := &fakeIdle{}
	idle.set(true)

	r := NewRunner(mgr, spawner.spawn, idle.check)
	r.SetPeriod(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	r.Start(ctx)
	defer r.Stop()

	time.Sleep(70 * time.Millisecond)

	// Within 1-2 ticks we expect at most 2 iterations to have fired.
	count := spawner.callCount()
	if count > 2 {
		t.Errorf("Spawner fired %d times in ~1.5 ticks; expected ≤2 (one-per-tick policy)", count)
	}
	if count == 0 {
		t.Error("Spawner should have fired at least once in 1.5 ticks")
	}
}

func TestRunner_ReChecksIdleBeforeFire(t *testing.T) {
	// Race scenario: runner sees idle=true at start of tick, but a
	// user message arrives before the spawn call. Re-check should
	// catch this and skip.
	mgr := NewManager(newMemStorage())
	_, _ = mgr.Add("task", ModeSelfPaced, 0)

	spawner := &fakeSpawner{output: "ok", ok: true}
	idle := &fakeIdle{}
	flipped := atomic.Bool{}

	r := NewRunner(mgr, spawner.spawn, func() bool {
		// First call (initial check) returns true.
		// Second call (re-check) returns false — simulating user input arrival.
		current := idle.idle.Load()
		if !flipped.Load() {
			flipped.Store(true)
			idle.set(false)
		}
		return current
	})
	idle.set(true)
	r.SetPeriod(40 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r.Start(ctx)
	defer r.Stop()

	time.Sleep(150 * time.Millisecond)

	if spawner.callCount() != 0 {
		t.Errorf("Spawner fired despite re-check showing not-idle; count=%d", spawner.callCount())
	}
}

func TestRunner_RecordsErrorIteration(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("task", ModeSelfPaced, 0)

	spawner := &fakeSpawner{err: errors.New("simulated transport failure")}
	idle := &fakeIdle{}
	idle.set(true)

	r := NewRunner(mgr, spawner.spawn, idle.check)
	r.SetPeriod(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	r.Start(ctx)
	defer r.Stop()

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if spawner.callCount() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	got, _ := mgr.Get(l.ID)
	if len(got.Iterations) == 0 {
		t.Fatal("error iteration not recorded")
	}
	if got.Iterations[0].OK {
		t.Error("error iteration marked OK=true")
	}
	if !strings.Contains(got.Iterations[0].Summary, "simulated transport failure") {
		t.Errorf("error not captured in summary: %q", got.Iterations[0].Summary)
	}
}

func TestRunner_HooksFired(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("task", ModeSelfPaced, 0)

	spawner := &fakeSpawner{output: "result text", ok: true}
	idle := &fakeIdle{}
	idle.set(true)

	r := NewRunner(mgr, spawner.spawn, idle.check)
	r.SetPeriod(50 * time.Millisecond)

	var startedID, doneID atomic.Value
	r.SetIterationStartHook(func(id string) { startedID.Store(id) })
	r.SetIterationDoneHook(func(id string, _ Iteration) { doneID.Store(id) })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	r.Start(ctx)
	defer r.Stop()

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if doneID.Load() != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if startedID.Load() != l.ID {
		t.Errorf("start hook got %v, want %s", startedID.Load(), l.ID)
	}
	if doneID.Load() != l.ID {
		t.Errorf("done hook got %v, want %s", doneID.Load(), l.ID)
	}
}

func TestRunner_StopIdempotent(t *testing.T) {
	r := NewRunner(NewManager(newMemStorage()), (&fakeSpawner{}).spawn, (&fakeIdle{}).check)
	// Stop before Start — should not panic.
	r.Stop()
	r.Stop() // double-stop must be safe via sync.Once.
}

func TestRunner_StartIdempotent(t *testing.T) {
	r := NewRunner(NewManager(newMemStorage()), (&fakeSpawner{}).spawn, (&fakeIdle{}).check)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	r.Start(ctx) // second Start must be no-op, not spawn another goroutine
	r.Stop()
}

func TestRunner_PanicsOnNilSpawner(t *testing.T) {
	r := NewRunner(NewManager(newMemStorage()), nil, (&fakeIdle{}).check)
	defer func() {
		if rec := recover(); rec == nil {
			t.Error("expected panic for nil Spawner")
		}
	}()
	r.Start(context.Background())
}

func TestRunner_PanicsOnNilIdleChecker(t *testing.T) {
	r := NewRunner(NewManager(newMemStorage()), (&fakeSpawner{}).spawn, nil)
	defer func() {
		if rec := recover(); rec == nil {
			t.Error("expected panic for nil IdleChecker")
		}
	}()
	r.Start(context.Background())
}

// TestBuildIterationPrompt covers the prompt-shape contract: includes
// task, iteration counter, and recent context summaries.
func TestBuildIterationPrompt(t *testing.T) {
	l := &Loop{
		ID:             "loop-x",
		Task:           "Fix the failing test",
		IterationCount: 2, // already 2 done
		Iterations: []Iteration{
			{N: 1, Summary: "Reproduced the failure", StartedAt: time.Now()},
			{N: 2, Summary: "Identified the cause", StartedAt: time.Now()},
		},
	}
	prompt := BuildIterationPrompt(l)

	if !strings.Contains(prompt, "iteration 3") {
		t.Errorf("prompt missing iteration counter (next is 3): %q", prompt)
	}
	if !strings.Contains(prompt, "Fix the failing test") {
		t.Errorf("prompt missing task: %q", prompt)
	}
	if !strings.Contains(prompt, "Reproduced the failure") {
		t.Errorf("prompt missing prior summary: %q", prompt)
	}
	if !strings.Contains(prompt, "Identified the cause") {
		t.Errorf("prompt missing recent summary: %q", prompt)
	}
}

func TestBuildIterationPrompt_MaxIterations(t *testing.T) {
	l := &Loop{
		ID: "loop-x", Task: "task", IterationCount: 4, MaxIterations: 5,
	}
	prompt := BuildIterationPrompt(l)
	if !strings.Contains(prompt, "iteration 5/5") {
		t.Errorf("prompt should show 5/5 progress: %q", prompt)
	}
}

// TestParseDoneSignal: only the LAST non-empty line counts and only
// if it's an exact match for the registered terminator. Pinned in
// tests so a future "be more lenient" change can't accidentally
// terminate loops on prose like "I'm done with that file" buried in
// the middle of the response.
func TestParseDoneSignal(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"done.", true},
		{"DONE", true},
		{"Loop done.", true},
		{"loop_done", true},
		{"Made progress.\n\ndone.", true},
		{"Made progress.\ndone.\n\n", true}, // trailing newlines OK
		{"", false},
		{"Made progress.", false},
		{"I'm done with that file.\nMore work to do.", false}, // not last line
		{"done. but also more stuff", false},                  // not exact match
		{"work in progress", false},
	}
	for _, tc := range cases {
		got := parseDoneSignal(tc.in)
		if got != tc.want {
			t.Errorf("parseDoneSignal(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestRunner_SelfTerminatesOnDoneSignal: when the spawned agent ends
// its iteration with "done.", the runner must call Stop on the loop
// so it doesn't keep firing. This is the autonomous self-improvement
// use case — agent decides when the task is genuinely complete.
func TestRunner_SelfTerminatesOnDoneSignal(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("Fix all bugs", ModeSelfPaced, 0)

	spawnFn := func(ctx context.Context, prompt string) (string, bool, error) {
		return "Fixed everything.\n\ndone.", true, nil
	}

	r := NewRunner(mgr, spawnFn, func() bool { return true })
	r.fireOne(t.Context(), l)

	got, _ := mgr.Get(l.ID)
	if got.Status != StatusStopped {
		t.Errorf("after agent emitted 'done.', status = %s, want stopped", got.Status)
	}
	if got.IterationCount != 1 {
		t.Errorf("expected 1 iteration recorded before self-terminate, got %d", got.IterationCount)
	}
}

// TestBuildIterationPrompt_MarkdownPointer: when UpdateMemory is on
// AND the loop has at least one iteration, the prompt must point the
// agent at the persisted markdown so it can read full history beyond
// the inline 3-summary preview. Without this hint, multi-iteration
// "improve X" loops drift in direction every iteration because each
// sub-agent re-derives prior work from 200-rune summaries.
func TestBuildIterationPrompt_MarkdownPointer(t *testing.T) {
	l := &Loop{
		ID:             "loop-md",
		Task:           "Fix bugs",
		IterationCount: 5,
		UpdateMemory:   true,
		Iterations: []Iteration{
			{N: 5, Summary: "Last", StartedAt: time.Now()},
		},
	}
	prompt := BuildIterationPrompt(l)
	if !strings.Contains(prompt, ".gokin/loops/loop-md.md") {
		t.Errorf("prompt missing markdown pointer: %q", prompt)
	}

	// Without UpdateMemory the file isn't written, so don't tell the
	// agent to read it.
	l.UpdateMemory = false
	prompt = BuildIterationPrompt(l)
	if strings.Contains(prompt, ".gokin/loops/") {
		t.Errorf("prompt should not point at non-existent file when UpdateMemory=false: %q", prompt)
	}
}

// TestBuildIterationPrompt_SelfPacedHintsCadence: self-paced loops
// must teach the agent the "next: 30m" hint format so it can extend
// the cadence when there's nothing to do. Without this, the runner
// fires every 5 min (DefaultMinDelaySeconds) regardless of progress
// possibility — wastes credits on no-op iterations.
func TestBuildIterationPrompt_SelfPacedHintsCadence(t *testing.T) {
	l := &Loop{
		ID: "loop-sp", Task: "Watch CI", Mode: ModeSelfPaced,
		IterationCount: 0,
	}
	prompt := BuildIterationPrompt(l)
	if !strings.Contains(prompt, "next:") || !strings.Contains(prompt, "wait") {
		t.Errorf("self-paced prompt missing cadence-control hint: %q", prompt)
	}

	// Interval-mode loops have a fixed cadence, so don't show the hint.
	l.Mode = ModeInterval
	l.IntervalSeconds = 600
	prompt = BuildIterationPrompt(l)
	if strings.Contains(prompt, "next:") {
		t.Errorf("interval-mode prompt shouldn't include self-paced cadence hint: %q", prompt)
	}
}

func TestSummarizeOutput(t *testing.T) {
	cases := map[string]string{
		"":                            "iteration completed",
		"Single line":                 "Single line",
		"First.\n\nLast.":             "Last.",
		"```\ncode\n```\n\nReal text.": "Real text.", // double newline separates code block from real text
		// ATX header should be skipped — the substance is the next paragraph.
		// Without the looksLikeMetadata header guard, the summary would be
		// "## Summary" instead of the actual content.
		"## Summary\n\nFinal substance text.": "Final substance text.",
		// A paragraph that incidentally starts with `#` but contains content
		// after a newline (commented shell example) is NOT a metadata header
		// and must NOT be skipped.
		"# real prose\nthat continues across lines.": "# real prose\nthat continues across lines.",
	}
	for input, want := range cases {
		got := summarizeOutput(input, "iteration completed")
		if got != want {
			t.Errorf("summarizeOutput(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseNextHint(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"normal text without hint", ""},
		{"foo\nnext: 30m\nbar", "30m"},
		{"foo\nNext check in 1h", "1h"},
		{"wait 5m", "wait 5m"},
		{"prose mentioning wait for stuff to complete", ""},
	}
	for _, tc := range cases {
		got := parseNextHint(tc.in)
		if got != tc.want {
			t.Errorf("parseNextHint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
