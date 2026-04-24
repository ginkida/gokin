package client

import (
	"context"
	"testing"

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
