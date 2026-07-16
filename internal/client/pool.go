package client

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gokin/internal/logging"
)

const (
	// DefaultMaxPoolSize is the default maximum number of clients in the pool.
	DefaultMaxPoolSize = 5

	// DefaultIdleTimeout is the duration after which idle clients are removed.
	DefaultIdleTimeout = 5 * time.Minute
)

// ErrClientPoolClosed is returned when a caller tries to create a client after
// the pool has started shutting down.
var ErrClientPoolClosed = errors.New("client pool is closed")

type pendingClientCreation struct {
	done   chan struct{}
	client Client
	err    error
}

// ClientPool manages reusable Client instances keyed by provider, model, and
// (for factory-created entries) a construction-config fingerprint.
type ClientPool struct {
	mu       sync.RWMutex
	clients  map[string]Client
	maxSize  int
	lastUsed map[string]time.Time
	creating map[string]*pendingClientCreation
	closed   bool
}

// NewClientPool creates a new ClientPool with the given maximum size.
// If maxSize <= 0, DefaultMaxPoolSize is used.
func NewClientPool(maxSize int) *ClientPool {
	if maxSize <= 0 {
		maxSize = DefaultMaxPoolSize
	}
	return &ClientPool{
		clients:  make(map[string]Client),
		maxSize:  maxSize,
		lastUsed: make(map[string]time.Time),
		creating: make(map[string]*pendingClientCreation),
	}
}

// poolKey generates a pool key from provider and model.
func poolKey(provider, model string) string {
	return fmt.Sprintf("%s:%s", provider, model)
}

const poolFingerprintSeparator = "\x00cfg:"

func scopedPoolKey(provider, model, fingerprint string) string {
	key := poolKey(provider, model)
	if fingerprint != "" {
		key += poolFingerprintSeparator + fingerprint
	}
	return key
}

// parsePoolKey splits a pool key ("provider:model") into its components.
func parsePoolKey(key string) (provider, model string, ok bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return key[:i], key[i+1:], true
		}
	}
	return "", "", false
}

// Get retrieves a client from the pool for the given provider, model, and
// optional construction-config fingerprint. Factory callers pass a fingerprint
// so different endpoints, credentials, or request settings cannot alias.
// Returns the client and true if found, or nil and false if not pooled.
func (p *ClientPool) Get(provider, model string, fingerprint ...string) (Client, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, false
	}

	scope := ""
	if len(fingerprint) > 0 {
		scope = fingerprint[0]
	}
	key := scopedPoolKey(provider, model, scope)
	c, ok := p.clients[key]
	if ok {
		p.lastUsed[key] = time.Now()
		logging.Debug("client retrieved from pool",
			"provider", provider,
			"model", model)
	}
	return c, ok
}

// GetOrCreate atomically retrieves or constructs one client for a scoped pool
// key. Concurrent callers for the same key share the same in-flight creation,
// while callers for unrelated keys can continue independently.
//
// A successfully returned client is owned by the pool. If shutdown or an
// explicit Put wins the race while create is running, the redundant client is
// closed before GetOrCreate returns.
func (p *ClientPool) GetOrCreate(
	provider, model, fingerprint string,
	create func() (Client, error),
) (Client, error) {
	if p == nil {
		return nil, ErrClientPoolClosed
	}
	if create == nil {
		return nil, fmt.Errorf("client factory is nil")
	}

	key := scopedPoolKey(provider, model, fingerprint)

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrClientPoolClosed
	}
	if c, ok := p.clients[key]; ok {
		p.lastUsed[key] = time.Now()
		p.mu.Unlock()
		return c, nil
	}
	if pending, ok := p.creating[key]; ok {
		p.mu.Unlock()
		<-pending.done
		return pending.client, pending.err
	}

	pending := &pendingClientCreation{done: make(chan struct{})}
	p.creating[key] = pending
	p.mu.Unlock()

	created, createErr := create()
	if createErr == nil && isNilClient(created) {
		createErr = fmt.Errorf("client factory returned nil client")
	}

	var closeCreated bool
	p.mu.Lock()
	delete(p.creating, key)
	switch {
	case createErr != nil:
		pending.err = createErr
		closeCreated = !isNilClient(created)
	case p.closed:
		pending.err = ErrClientPoolClosed
		closeCreated = true
	case p.clients[key] != nil:
		// Put may have populated this key while construction was in flight.
		// Preserve that explicit replacement and discard the redundant build.
		pending.client = p.clients[key]
		p.lastUsed[key] = time.Now()
		closeCreated = true
	default:
		if len(p.clients) >= p.maxSize {
			p.evictOldest()
		}
		p.clients[key] = created
		p.lastUsed[key] = time.Now()
		pending.client = created
		logging.Debug("client stored in pool",
			"provider", provider,
			"model", model,
			"pool_size", len(p.clients))
	}
	close(pending.done)
	p.mu.Unlock()

	if closeCreated {
		_ = created.Close()
	}
	return pending.client, pending.err
}

// Invalidate removes (and closes) every pooled construction variant for the
// given provider and model. No-op if absent.
//
// This remains useful for explicitly evicting every cached variant after a
// provider-level transition. Concretely:
// `/login kimi <new-key>` persists the new key to config but, without this
// method, ApplyConfig's call to client.NewClient finds the old Kimi client
// in the pool and returns it unchanged — requests then go out with the
// stale key and 401. ApplyConfig calls this before NewClient to force a
// fresh build.
func (p *ClientPool) Invalidate(provider, model string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return
	}

	baseKey := poolKey(provider, model)
	for key, c := range p.clients {
		if key != baseKey && !strings.HasPrefix(key, baseKey+poolFingerprintSeparator) {
			continue
		}
		_ = c.Close()
		delete(p.clients, key)
		delete(p.lastUsed, key)
		logging.Debug("pool entry invalidated",
			"provider", provider,
			"model", model)
	}
}

// FlushProvider removes and closes every pooled client whose provider
// matches (regardless of model). Used after /login or /provider so no
// stale client with the old API key survives.
func (p *ClientPool) FlushProvider(provider string) {
	if p == nil {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return
	}

	for key, c := range p.clients {
		if keyProvider, _, ok := parsePoolKey(key); ok && keyProvider == provider {
			_ = c.Close()
			delete(p.clients, key)
			delete(p.lastUsed, key)
			logging.Debug("pool entry flushed by provider",
				"provider", provider,
				"key", key)
		}
	}
}

// Put stores a client in the pool. The optional fingerprint has the same
// construction-identity semantics as Get. If the pool is full, the oldest
// idle client is evicted to make room. Put transfers ownership to the pool;
// a client rejected after pool shutdown is closed immediately.
func (p *ClientPool) Put(provider, model string, client Client, fingerprint ...string) {
	if isNilClient(client) {
		return
	}

	p.mu.Lock()

	if p.closed {
		p.mu.Unlock()
		_ = client.Close()
		return
	}

	scope := ""
	if len(fingerprint) > 0 {
		scope = fingerprint[0]
	}
	key := scopedPoolKey(provider, model, scope)

	// Replacing an existing key does not consume another capacity slot. Return
	// before the full-pool eviction path so an unrelated client survives.
	if existing, ok := p.clients[key]; ok {
		if sameClientInstance(existing, client) {
			p.lastUsed[key] = time.Now()
			p.mu.Unlock()
			return
		}
		p.clients[key] = client
		p.lastUsed[key] = time.Now()
		p.mu.Unlock()
		_ = existing.Close()
		logging.Debug("client replaced in pool",
			"provider", provider,
			"model", model)
		return
	}

	// If pool is full, evict the oldest idle client
	if len(p.clients) >= p.maxSize {
		p.evictOldest()
	}

	p.clients[key] = client
	p.lastUsed[key] = time.Now()

	logging.Debug("client stored in pool",
		"provider", provider,
		"model", model,
		"pool_size", len(p.clients))
	p.mu.Unlock()
}

// evictOldest removes the least recently used client from the pool.
// Must be called with p.mu held.
func (p *ClientPool) evictOldest() {
	var oldestKey string
	var oldestTime time.Time

	for key, t := range p.lastUsed {
		if oldestKey == "" || t.Before(oldestTime) {
			oldestKey = key
			oldestTime = t
		}
	}

	if oldestKey != "" {
		if c, ok := p.clients[oldestKey]; ok {
			_ = c.Close()
		}
		delete(p.clients, oldestKey)
		delete(p.lastUsed, oldestKey)

		logging.Debug("evicted oldest client from pool",
			"key", oldestKey)
	}
}

// Cleanup removes clients that have been idle for longer than DefaultIdleTimeout.
func (p *ClientPool) Cleanup() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return
	}

	now := time.Now()
	for key, t := range p.lastUsed {
		if now.Sub(t) > DefaultIdleTimeout {
			if c, ok := p.clients[key]; ok {
				_ = c.Close()
			}
			delete(p.clients, key)
			delete(p.lastUsed, key)

			logging.Debug("cleaned up idle client from pool",
				"key", key)
		}
	}
}

// Size returns the current number of clients in the pool.
func (p *ClientPool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.clients)
}

// Close closes all pooled clients and marks the pool as closed.
func (p *ClientPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return
	}

	p.closed = true
	for key, c := range p.clients {
		_ = c.Close()
		delete(p.clients, key)
		delete(p.lastUsed, key)
	}

	logging.Debug("client pool closed")
}
