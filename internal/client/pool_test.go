package client

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genai"
)

// fakeClient is a minimal Client stub for pool-identity tests — we only need
// Close (which the pool calls on eviction) and a way to tell instances apart.
type fakeClient struct {
	id     string
	closed bool
}

func (f *fakeClient) Close() error                  { f.closed = true; return nil }
func (f *fakeClient) SetTools(_ []*genai.Tool)      {}
func (f *fakeClient) SetSystemInstruction(_ string) {}
func (f *fakeClient) SetTurnContext(_ string)       {}
func (f *fakeClient) SetThinkingBudget(_ int32)     {}
func (f *fakeClient) SetRateLimiter(_ interface{})  {}
func (f *fakeClient) GetModel() string              { return f.id }
func (f *fakeClient) SetModel(_ string)             {}
func (f *fakeClient) WithModel(_ string) Client     { return f }
func (f *fakeClient) GetRawClient() any             { return nil }
func (f *fakeClient) SendMessage(_ context.Context, _ string) (*StreamingResponse, error) {
	return nil, nil
}
func (f *fakeClient) SendMessageWithHistory(_ context.Context, _ []*genai.Content, _ string) (*StreamingResponse, error) {
	return nil, nil
}
func (f *fakeClient) SendFunctionResponse(_ context.Context, _ []*genai.Content, _ []*genai.FunctionResponse) (*StreamingResponse, error) {
	return nil, nil
}
func (f *fakeClient) CountTokens(_ context.Context, _ []*genai.Content) (*genai.CountTokensResponse, error) {
	return &genai.CountTokensResponse{}, nil
}

// Regression: the pool keys by (provider, model) only — not by API key or
// any other config field. When `/login <provider> <new-key>` saves a new
// key to config.yaml and ApplyConfig calls client.NewClient, the factory
// asks the pool for a cached entry. Without Invalidate, it hits the cache
// and returns the OLD client built with the OLD key — requests go out with
// stale credentials and the user sees 401 despite a valid key sitting on
// disk. A full process restart "fixed" it because the pool started empty.
//
// Invalidate is the contract the app layer uses to opt out of the cache
// when a config input that affects client construction has changed.
func TestClientPool_InvalidateEvictsAndClosesEntry(t *testing.T) {
	pool := NewClientPool(5)
	oldClient := &fakeClient{id: "old-key-client"}
	pool.Put("kimi", "kimi-for-coding", oldClient)

	// Sanity: the entry is there.
	got, ok := pool.Get("kimi", "kimi-for-coding")
	if !ok || got != oldClient {
		t.Fatalf("pre-check: pool.Get should return the put client, got %+v ok=%v", got, ok)
	}

	// Invalidate — this is the one fix.
	pool.Invalidate("kimi", "kimi-for-coding")

	// Cache miss now.
	got, ok = pool.Get("kimi", "kimi-for-coding")
	if ok || got != nil {
		t.Errorf("after Invalidate, pool.Get should miss; got %+v ok=%v", got, ok)
	}
	if !oldClient.closed {
		t.Error("Invalidate must Close() the evicted client to release its HTTP resources")
	}

	// A subsequent Put should populate a fresh entry (the new client built
	// with the new key in production).
	newClient := &fakeClient{id: "new-key-client"}
	pool.Put("kimi", "kimi-for-coding", newClient)
	got, ok = pool.Get("kimi", "kimi-for-coding")
	if !ok || got != newClient {
		t.Errorf("after re-Put, should see new client; got %+v ok=%v", got, ok)
	}
	if newClient.closed {
		t.Error("fresh client must not be closed")
	}
}

// Invalidating a non-existent entry must be a silent no-op — callers from
// ApplyConfig don't know whether the pool already has an entry, and having
// to pre-check would reintroduce a TOCTOU window.
func TestClientPool_InvalidateMissingEntryIsNoOp(t *testing.T) {
	pool := NewClientPool(5)
	// Should not panic or error.
	pool.Invalidate("kimi", "kimi-for-coding")
	pool.Invalidate("", "")

	// Closing a still-empty pool must stay fine after spurious Invalidates.
	pool.Close()
}

// Invalidate on a closed pool must short-circuit — after shutdown, no
// goroutine should try to close entries that may already be gone.
func TestClientPool_InvalidateOnClosedPool(t *testing.T) {
	pool := NewClientPool(5)
	c := &fakeClient{id: "x"}
	pool.Put("kimi", "kimi-for-coding", c)

	pool.Close() // all entries closed by Close itself.

	// Further Invalidates must be inert.
	pool.Invalidate("kimi", "kimi-for-coding")
}

// Invalidate must scope to (provider, model) — it must not drop other
// providers' pooled clients. Important when /login kimi runs: any DeepSeek
// or GLM clients in the pool (from fallback chains) must survive.
func TestClientPool_InvalidateIsScoped(t *testing.T) {
	pool := NewClientPool(5)
	kimi := &fakeClient{id: "kimi"}
	deepseek := &fakeClient{id: "deepseek"}
	pool.Put("kimi", "kimi-for-coding", kimi)
	pool.Put("deepseek", "deepseek-v4-pro", deepseek)

	pool.Invalidate("kimi", "kimi-for-coding")

	// kimi gone
	if _, ok := pool.Get("kimi", "kimi-for-coding"); ok {
		t.Error("kimi entry should be gone")
	}
	if !kimi.closed {
		t.Error("kimi client should be closed")
	}

	// deepseek survives
	got, ok := pool.Get("deepseek", "deepseek-v4-pro")
	if !ok || got != deepseek {
		t.Errorf("deepseek entry should remain; got %+v ok=%v", got, ok)
	}
	if deepseek.closed {
		t.Error("deepseek client must not be closed by an unrelated Invalidate")
	}
}

func TestClientPool_InvalidateRemovesAllConfigFingerprints(t *testing.T) {
	pool := NewClientPool(5)
	first := &fakeClient{id: "endpoint-a"}
	second := &fakeClient{id: "endpoint-b"}
	pool.Put("kimi", "kimi-for-coding", first, "config-a")
	pool.Put("kimi", "kimi-for-coding", second, "config-b")

	pool.Invalidate("kimi", "kimi-for-coding")
	if pool.Size() != 0 {
		t.Fatalf("pool size = %d, want all config variants removed", pool.Size())
	}
	if !first.closed || !second.closed {
		t.Fatalf("scoped clients were not both closed: first=%v second=%v", first.closed, second.closed)
	}
}

func TestClientPool_GetOrCreateConcurrentSingleConstruction(t *testing.T) {
	pool := NewClientPool(2)
	const callers = 32

	type result struct {
		client Client
		err    error
	}
	results := make(chan result, callers)
	start := make(chan struct{})
	var creates atomic.Int32
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			client, err := pool.GetOrCreate("glm", "glm-5.2", "same-config", func() (Client, error) {
				creates.Add(1)
				return &fakeClient{id: "shared"}, nil
			})
			results <- result{client: client, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	if got := creates.Load(); got != 1 {
		t.Fatalf("factory calls = %d, want exactly 1", got)
	}
	var shared Client
	for result := range results {
		if result.err != nil {
			t.Fatalf("GetOrCreate: %v", result.err)
		}
		if shared == nil {
			shared = result.client
		} else if result.client != shared {
			t.Fatal("concurrent callers received different clients")
		}
	}
	if got := pool.Size(); got != 1 {
		t.Fatalf("pool size = %d, want 1", got)
	}
}

func TestClientPool_PutReplacementAtCapacityKeepsUnrelatedClient(t *testing.T) {
	pool := NewClientPool(2)
	unrelated := &fakeClient{id: "unrelated"}
	old := &fakeClient{id: "old"}
	replacement := &fakeClient{id: "replacement"}
	pool.Put("kimi", "kimi-for-coding", unrelated)
	pool.Put("glm", "glm-5.2", old)

	// Make eviction order deterministic. The old implementation incorrectly
	// evicted the unrelated entry before replacing an already-present key.
	pool.mu.Lock()
	pool.lastUsed[poolKey("kimi", "kimi-for-coding")] = time.Unix(1, 0)
	pool.lastUsed[poolKey("glm", "glm-5.2")] = time.Unix(2, 0)
	pool.mu.Unlock()

	pool.Put("glm", "glm-5.2", replacement)

	if got := pool.Size(); got != 2 {
		t.Fatalf("pool size = %d, want 2", got)
	}
	if !old.closed {
		t.Error("replaced client was not closed")
	}
	if unrelated.closed {
		t.Error("replacement at capacity evicted an unrelated client")
	}
	if got, ok := pool.Get("kimi", "kimi-for-coding"); !ok || got != unrelated {
		t.Fatalf("unrelated client missing after replacement: client=%v ok=%v", got, ok)
	}
	if got, ok := pool.Get("glm", "glm-5.2"); !ok || got != replacement {
		t.Fatalf("replacement was not stored: client=%v ok=%v", got, ok)
	}
}

func TestClientPool_PutSameInstanceDoesNotCloseStoredClient(t *testing.T) {
	pool := NewClientPool(1)
	client := &fakeClient{id: "same"}
	pool.Put("glm", "glm-5.2", client)
	pool.Put("glm", "glm-5.2", client)

	if client.closed {
		t.Fatal("re-putting the same owned instance closed the live pool entry")
	}
	if got, ok := pool.Get("glm", "glm-5.2"); !ok || got != client {
		t.Fatalf("stored client = %v ok=%v, want original instance", got, ok)
	}
}

func TestClientPool_GetOrCreateAtCapacityEvictsOldest(t *testing.T) {
	pool := NewClientPool(1)
	old := &fakeClient{id: "old"}
	pool.Put("kimi", "kimi-for-coding", old)

	created := &fakeClient{id: "new"}
	got, err := pool.GetOrCreate("glm", "glm-5.2", "config", func() (Client, error) {
		return created, nil
	})
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if got != created {
		t.Fatalf("client = %v, want newly created client", got)
	}
	if !old.closed {
		t.Error("oldest client was not closed on capacity eviction")
	}
	if size := pool.Size(); size != 1 {
		t.Fatalf("pool size = %d, want 1", size)
	}
}

func TestClientPool_GetOrCreateCloseDuringCreationClosesResult(t *testing.T) {
	pool := NewClientPool(1)
	created := &fakeClient{id: "late"}
	started := make(chan struct{})
	release := make(chan struct{})
	type creationResult struct {
		client Client
		err    error
	}
	resultCh := make(chan creationResult, 1)
	go func() {
		client, err := pool.GetOrCreate("glm", "glm-5.2", "config", func() (Client, error) {
			close(started)
			<-release
			return created, nil
		})
		resultCh <- creationResult{client: client, err: err}
	}()

	<-started
	pool.Close()
	close(release)
	gotResult := <-resultCh
	if !errors.Is(gotResult.err, ErrClientPoolClosed) {
		t.Fatalf("error = %v, want ErrClientPoolClosed", gotResult.err)
	}
	if gotResult.client != nil {
		t.Fatalf("client = %v, want nil after shutdown", gotResult.client)
	}
	if !created.closed {
		t.Error("client completed after shutdown was not closed")
	}
	if size := pool.Size(); size != 0 {
		t.Fatalf("pool size = %d after shutdown, want 0", size)
	}
}

func TestClientPool_PutAfterCloseClosesRejectedClient(t *testing.T) {
	pool := NewClientPool(1)
	pool.Close()
	client := &fakeClient{id: "late-put"}
	pool.Put("glm", "glm-5.2", client)
	if !client.closed {
		t.Error("Put must close a client it cannot take ownership of")
	}
}
