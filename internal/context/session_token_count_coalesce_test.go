package context

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/config"
	"gokin/internal/testkit"

	"google.golang.org/genai"
)

type coalescingTokenClient struct {
	*testkit.MockClient
	firstStarted  chan struct{}
	releaseFirst  chan struct{}
	blockAll      bool
	calls         atomic.Int32
	concurrent    atomic.Int32
	maxConcurrent atomic.Int32
}

func (c *coalescingTokenClient) CountTokens(ctx context.Context, contents []*genai.Content) (*genai.CountTokensResponse, error) {
	call := c.calls.Add(1)
	concurrent := c.concurrent.Add(1)
	defer c.concurrent.Add(-1)
	for {
		previous := c.maxConcurrent.Load()
		if concurrent <= previous || c.maxConcurrent.CompareAndSwap(previous, concurrent) {
			break
		}
	}
	if call == 1 {
		close(c.firstStarted)
	}
	if c.blockAll || call == 1 {
		select {
		case <-c.releaseFirst:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &genai.CountTokensResponse{TotalTokens: int32(len(contents) * 100)}, nil
}

func waitForSessionCounterIdle(t *testing.T, m *ContextManager) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if !m.sessionCountRunning.Load() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("session token-count worker did not become idle")
		case <-ticker.C:
		}
	}
}

func TestSessionTokenCountCoalescesBurstToLatestSnapshot(t *testing.T) {
	c := &coalescingTokenClient{
		MockClient:   testkit.NewMockClient(),
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	sess := chat.NewSession()
	m := NewContextManager(context.Background(), sess, c, &config.ContextConfig{})
	defer m.Close()
	m.StartSessionWatcher()

	sess.AddUserMessage("first")
	select {
	case <-c.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first token count did not start")
	}
	for i := 0; i < 25; i++ {
		sess.AddUserMessage("burst update")
	}
	close(c.releaseFirst)
	waitForSessionCounterIdle(t, m)

	if got := c.calls.Load(); got != 2 {
		t.Fatalf("26 session changes made %d token-count calls, want first + latest", got)
	}
	if got := c.maxConcurrent.Load(); got != 1 {
		t.Fatalf("session token-count concurrency = %d, want 1", got)
	}
	wantTokens := len(sess.GetHistory()) * 100
	if got := m.GetCurrentTokens(); got != wantTokens {
		t.Fatalf("latest token count = %d, want %d", got, wantTokens)
	}
}

func TestSessionTokenCountWorkerStopsOnManagerCancellation(t *testing.T) {
	c := &coalescingTokenClient{
		MockClient:   testkit.NewMockClient(),
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		blockAll:     true,
	}
	sess := chat.NewSession()
	m := NewContextManager(context.Background(), sess, c, &config.ContextConfig{})
	m.StartSessionWatcher()

	sess.AddUserMessage("block until shutdown")
	select {
	case <-c.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("token count did not start")
	}
	m.Close()
	waitForSessionCounterIdle(t, m)
	if got := c.concurrent.Load(); got != 0 {
		t.Fatalf("%d token-count calls remain after Close", got)
	}
}

func TestSessionTokenCountDoesNotOverwriteAuthoritativeProviderUsage(t *testing.T) {
	c := &coalescingTokenClient{
		MockClient:   testkit.NewMockClient(),
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	sess := chat.NewSession()
	m := NewContextManager(context.Background(), sess, c, &config.ContextConfig{MaxInputTokens: 100_000})
	defer m.Close()
	m.StartSessionWatcher()

	sess.AddUserMessage("provider will report exact usage")
	select {
	case <-c.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("local token count did not start")
	}
	m.ObserveAPIUsage(59_600, 1_250)
	close(c.releaseFirst)
	waitForSessionCounterIdle(t, m)

	if got := c.calls.Load(); got != 1 {
		t.Fatalf("provider usage update triggered %d local counts, want original stale call only", got)
	}
	usage := m.GetTokenUsage()
	if usage == nil || usage.InputTokens != 59_600 || usage.OutputTokens != 1_250 || usage.IsEstimate {
		t.Fatalf("authoritative provider usage was overwritten: %+v", usage)
	}
}
