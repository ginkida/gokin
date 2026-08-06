package context

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/config"
	"gokin/internal/testkit"
)

// deadlineProbeClient blocks the summarization request until its context is
// cancelled and returns the context cause. Embedding MockClient supplies the
// rest of client.Client without involving a real provider.
type deadlineProbeClient struct {
	*testkit.MockClient
	started chan struct{}
	cause   chan error
	once    sync.Once
}

func (c *deadlineProbeClient) SendMessage(ctx context.Context, _ string) (*client.StreamingResponse, error) {
	c.once.Do(func() { close(c.started) })
	<-ctx.Done()
	err := client.ContextErr(ctx)
	c.cause <- err
	return nil, err
}

func TestIncrementalCompactUsesLiveModelRoundTimeout(t *testing.T) {
	probe := &deadlineProbeClient{
		MockClient: testkit.NewMockClient(),
		started:    make(chan struct{}),
		cause:      make(chan error, 1),
	}
	sess := chat.NewSession()
	sess.SetHistory(bigHistory(60))
	m := NewContextManager(context.Background(), sess, probe, &config.ContextConfig{EnableAutoSummary: true})
	defer m.Close()
	m.SetSummaryStrategy(SummaryStrategy{MinMessagesForSummary: 2})
	m.SetModelRoundTimeout(25 * time.Millisecond)

	startedAt := time.Now()
	err := m.IncrementalCompact(context.Background())
	if !errors.Is(err, client.ErrModelRoundTimeout) {
		t.Fatalf("IncrementalCompact() error = %v, want typed model-round timeout", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("live 25ms timeout took %v", elapsed)
	}

	select {
	case cause := <-probe.cause:
		if !errors.Is(cause, client.ErrModelRoundTimeout) {
			t.Fatalf("summarizer context cause = %v, want model-round timeout", cause)
		}
		telemetry := client.DetectFailureTelemetry(cause)
		if telemetry.Timeout != 25*time.Millisecond {
			t.Fatalf("timeout telemetry = %v, want 25ms", telemetry.Timeout)
		}
	case <-time.After(time.Second):
		t.Fatal("summarizer did not observe its deadline")
	}
}

func TestIncrementalCompactPreservesTimeoutWhenStreamClosesSilently(t *testing.T) {
	mc := testkit.NewMockClient().EnqueueScript(testkit.ResponseScript{
		// MockClient reacts to context cancellation by closing Chunks without
		// emitting an error chunk — the close-vs-cancel race that Collect()
		// previously misclassified as an empty successful response.
		DelayBeforeFirstChunk: time.Hour,
	})
	sess := chat.NewSession()
	sess.SetHistory(bigHistory(60))
	m := NewContextManager(context.Background(), sess, mc, &config.ContextConfig{EnableAutoSummary: true})
	defer m.Close()
	m.SetSummaryStrategy(SummaryStrategy{MinMessagesForSummary: 2})
	m.SetModelRoundTimeout(25 * time.Millisecond)

	err := m.IncrementalCompact(context.Background())
	if !errors.Is(err, client.ErrModelRoundTimeout) {
		t.Fatalf("silent-close compaction error = %v, want typed model-round timeout", err)
	}
	telemetry := client.DetectFailureTelemetry(err)
	if telemetry.Timeout != 25*time.Millisecond {
		t.Fatalf("silent-close timeout telemetry = %#v, want 25ms", telemetry)
	}
}

type blockingCompactionClient struct {
	*testkit.MockClient
	started chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (c *blockingCompactionClient) SendMessage(ctx context.Context, message string) (*client.StreamingResponse, error) {
	c.calls.Add(1)
	c.once.Do(func() { close(c.started) })
	select {
	case <-c.release:
		return c.MockClient.SendMessage(ctx, message)
	case <-ctx.Done():
		return nil, client.ContextErr(ctx)
	}
}

func TestAutoCompactionSingleFlightAcrossTriggers(t *testing.T) {
	base := testkit.NewMockClient().EnqueueText("Summary keeps the newest complete task state and all relevant file paths.")
	blocking := &blockingCompactionClient{
		MockClient: base,
		started:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	sess := chat.NewSession()
	sess.SetHistory(bigHistory(60))
	m := NewContextManager(context.Background(), sess, blocking, &config.ContextConfig{
		EnableAutoSummary:    true,
		AutoCompactThreshold: 0.5,
	})
	defer m.Close()
	m.SetSummaryStrategy(SummaryStrategy{MinMessagesForSummary: 2})
	m.tokenCounter.limits = TokenLimits{MaxInputTokens: 100}

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		m.tryAutoCompact(context.Background(), 90)
	}()
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("first auto-compaction did not reach the model")
	}

	// A burst of session-change triggers must return without starting another
	// provider call, and manual /compact must report the in-flight operation.
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		m.tryAutoCompact(context.Background(), 90)
	}()
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("duplicate auto-compaction waited instead of being coalesced")
	}
	if err := m.ForceSummarize(context.Background()); !errors.Is(err, ErrSummarizationInProgress) {
		t.Fatalf("ForceSummarize() during auto-compaction = %v, want in-progress sentinel", err)
	}
	agent := NewContextAgent(m, sess, t.TempDir())
	agent.runCompaction(context.Background())
	if got := blocking.calls.Load(); got != 1 {
		t.Fatalf("concurrent triggers started %d provider calls, want 1", got)
	}

	close(blocking.release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first auto-compaction did not finish after release")
	}
}
