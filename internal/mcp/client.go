package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"gokin/internal/logging"
)

// NotificationHandler processes an unsolicited JSON-RPC notification from
// the server (ID is absent, Method is set). Called in its own goroutine —
// slow handlers do not block the receiveLoop.
type NotificationHandler func(method string, params any)

// Client handles JSON-RPC communication with an MCP server.
type Client struct {
	transport  Transport
	serverInfo *ServerInfo
	tools      []*ToolInfo
	resources  []*Resource

	// Connection state
	initialized bool
	mu          sync.RWMutex

	// initMu serializes Initialize() calls so we send exactly one
	// initialize request per client. It must be SEPARATE from c.mu:
	// Initialize() does a full network round-trip via request() and
	// must not hold c.mu during that round-trip — handleMessage's
	// notification path takes c.mu.RLock() to read notificationHandler,
	// and a server-initiated notification arriving before the
	// InitializeResult would deadlock receiveLoop on RLock while
	// Initialize was holding the write lock waiting for that exact
	// response.
	initMu sync.Mutex

	// Request tracking
	nextID    int64
	pending   map[int64]chan *JSONRPCMessage
	pendingMu sync.Mutex

	// Configuration
	serverName string
	config     *ServerConfig

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	// Reconnection state. Kept under a dedicated mutex so receiveLoop can
	// update backoff/consecutiveFails without contending with mu, which
	// request-level methods (Initialize, ListTools, etc.) hold across full
	// network round-trips — a pre-2026 deadlock we hit when the server
	// replied while a request was still in flight.
	reconnectMu          sync.Mutex
	backoff              time.Duration
	maxBackoff           time.Duration
	consecutiveFails     int
	maxReconnectAttempts int

	// Incoming unsolicited notifications (protected by mu).
	notificationHandler NotificationHandler

	// notifWg tracks in-flight notification handler goroutines launched by
	// handleMessage. Close waits for them (with a timeout) so shutdown
	// doesn't leave orphaned handler goroutines still doing network/registry
	// work after the client is gone.
	notifWg sync.WaitGroup
}

// NewClient creates a new MCP client with the specified transport.
func NewClient(ctx context.Context, cfg *ServerConfig) (*Client, error) {
	var transport Transport
	var err error

	switch cfg.Transport {
	case "stdio":
		transport, err = NewStdioTransport(cfg.Command, cfg.Args, cfg.Env)
	case "http":
		transport, err = NewHTTPTransport(ctx, cfg.URL, cfg.Headers, cfg.Timeout)
	default:
		return nil, fmt.Errorf("unknown transport type: %s", cfg.Transport)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create transport: %w", err)
	}

	return newClientWithTransport(ctx, cfg, transport), nil
}

// newClientWithTransport builds a client around an already-constructed
// transport. Used by NewClient and by tests that inject a fake transport.
func newClientWithTransport(ctx context.Context, cfg *ServerConfig, transport Transport) *Client {
	ctx, cancel := context.WithCancel(ctx)

	c := &Client{
		transport:            transport,
		serverName:           cfg.Name,
		config:               cfg,
		pending:              make(map[int64]chan *JSONRPCMessage),
		ctx:                  ctx,
		cancel:               cancel,
		done:                 make(chan struct{}),
		backoff:              100 * time.Millisecond,
		maxBackoff:           30 * time.Second,
		maxReconnectAttempts: 10,
	}

	// Start message receiver goroutine
	go c.receiveLoop()

	return c
}

// receiveLoop reads messages from the transport and routes them.
func (c *Client) receiveLoop() {
	defer close(c.done)

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		msg, err := c.transport.Receive()
		if err != nil {
			if c.ctx.Err() != nil {
				return
			}

			c.reconnectMu.Lock()
			c.consecutiveFails++
			fails := c.consecutiveFails
			c.reconnectMu.Unlock()

			logging.Warn("MCP receive error", "error", err, "consecutive_fails", fails)

			// Attempt reconnection with backoff
			if !c.reconnect() {
				return
			}
			continue
		}

		// Reset backoff on successful receive. Must NOT touch c.mu here —
		// a request() caller may be holding c.mu across its round-trip.
		c.reconnectMu.Lock()
		c.backoff = 100 * time.Millisecond
		c.consecutiveFails = 0
		c.reconnectMu.Unlock()

		c.handleMessage(msg)
	}
}

// reconnect attempts to recreate the transport with exponential backoff.
// Returns true if reconnection should continue, false to stop the receive loop.
func (c *Client) reconnect() bool {
	c.reconnectMu.Lock()
	// Check if we've exceeded max reconnect attempts
	if c.consecutiveFails >= c.maxReconnectAttempts {
		fails := c.consecutiveFails
		c.reconnectMu.Unlock()
		logging.Error("MCP max reconnect attempts reached, giving up",
			"server", c.serverName,
			"attempts", fails)
		return false
	}
	currentBackoff := c.backoff
	// Exponential backoff: double each time, capped at maxBackoff
	c.backoff = c.backoff * 2
	if c.backoff > c.maxBackoff {
		c.backoff = c.maxBackoff
	}
	// Capture for log lines below — reading c.consecutiveFails outside the
	// lock would race with receiveLoop incrementing it.
	fails := c.consecutiveFails
	c.reconnectMu.Unlock()

	// c.config is set once in newClientWithTransport and never reassigned, so
	// reading fields on it is safe without a lock.
	cfg := c.config

	logging.Info("MCP reconnecting", "server", c.serverName, "backoff", currentBackoff)

	backoffTimer := time.NewTimer(currentBackoff)
	select {
	case <-backoffTimer.C:
	case <-c.ctx.Done():
		backoffTimer.Stop()
		return false
	}

	// Try to recreate transport
	var transport Transport
	var err error

	switch cfg.Transport {
	case "stdio":
		transport, err = NewStdioTransport(cfg.Command, cfg.Args, cfg.Env)
	case "http":
		transport, err = NewHTTPTransport(c.ctx, cfg.URL, cfg.Headers, cfg.Timeout)
	default:
		logging.Error("MCP unknown transport for reconnect", "transport", cfg.Transport)
		return false
	}

	if err != nil {
		logging.Warn("MCP transport recreation failed", "error", err, "consecutive_fails", fails)
		return true // Keep trying (consecutiveFails will be checked next iteration)
	}

	// Swap transport
	oldTransport := c.transport
	c.mu.Lock()
	c.transport = transport
	c.initialized = false
	c.mu.Unlock()

	// Close old transport (ignore errors). Guard against nil in case the
	// original transport was never successfully created.
	if oldTransport != nil {
		_ = oldTransport.Close()
	}

	// Re-initialize using direct transport I/O.
	// We must NOT use Initialize() here because it calls request() which waits
	// for receiveLoop to deliver the response via handleMessage(). But we ARE
	// receiveLoop — so that would deadlock.
	if err := c.initializeDirect(); err != nil {
		logging.Warn("MCP re-initialize failed", "error", err, "consecutive_fails", fails)
		return true // Keep trying (consecutiveFails will be checked next iteration)
	}

	logging.Info("MCP reconnected successfully", "server", c.serverName)
	return true
}

// initializeDirect performs MCP initialization using direct transport I/O.
// Called from reconnect() which runs inside receiveLoop — the normal
// Initialize() → request() path would deadlock because request() waits for
// receiveLoop to route the response, but receiveLoop is the caller.
func (c *Client) initializeDirect() error {
	params := &InitializeParams{
		ProtocolVersion: ProtocolVersion,
		ClientInfo: &ClientInfo{
			Name:    "gokin",
			Version: "1.0.0",
		},
		Capabilities: map[string]any{},
	}

	id := atomic.AddInt64(&c.nextID, 1)
	msg := &JSONRPCMessage{
		ID:     id,
		Method: MethodInitialize,
		Params: params,
	}

	if err := c.transport.Send(msg); err != nil {
		return fmt.Errorf("initialize send failed: %w", err)
	}

	// Read response directly from transport.
	// We're in receiveLoop, so we're the sole reader — no concurrent access.
	// The server was just started, so the initialize response should arrive quickly.
	resp, err := c.transport.Receive()
	if err != nil {
		return fmt.Errorf("initialize receive failed: %w", err)
	}

	if resp.Error != nil {
		return resp.Error
	}

	// Parse result
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return fmt.Errorf("failed to marshal initialize result: %w", err)
	}

	var result InitializeResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return fmt.Errorf("failed to parse initialize result: %w", err)
	}

	// Send initialized notification
	if err := c.notify(MethodInitialized, nil); err != nil {
		return fmt.Errorf("failed to send initialized notification: %w", err)
	}

	// Update state under lock
	c.mu.Lock()
	c.serverInfo = result.ServerInfo
	c.initialized = true
	c.mu.Unlock()

	logging.Info("MCP server re-initialized",
		"name", c.serverName,
		"server", result.ServerInfo.Name,
		"version", result.ServerInfo.Version)

	return nil
}

// handleMessage routes an incoming message to the appropriate handler.
func (c *Client) handleMessage(msg *JSONRPCMessage) {
	if msg == nil {
		logging.Warn("MCP received nil message")
		return
	}

	if msg.IsResponse() {
		// Route to pending request
		id, ok := msg.ID.(float64) // JSON numbers are float64
		if !ok {
			logging.Warn("MCP response with invalid ID type", "id", msg.ID)
			return
		}

		c.pendingMu.Lock()
		ch, exists := c.pending[int64(id)]
		if exists {
			delete(c.pending, int64(id))
		}
		c.pendingMu.Unlock()

		if exists {
			select {
			case ch <- msg:
			default:
				logging.Warn("MCP response channel full", "id", id)
			}
		} else {
			logging.Warn("MCP response for unknown request", "id", id)
		}
	} else if msg.IsNotification() {
		logging.Debug("MCP notification received", "method", msg.Method)
		c.mu.RLock()
		h := c.notificationHandler
		c.mu.RUnlock()
		if h != nil {
			// Run the handler in its own goroutine so a slow handler can't
			// stall the transport receive pump. Track via notifWg so Close
			// can wait for in-flight handlers.
			method := msg.Method
			params := msg.Params
			c.notifWg.Add(1)
			go func() {
				defer c.notifWg.Done()
				defer func() {
					if r := recover(); r != nil {
						logging.Warn("MCP notification handler panic",
							"server", c.serverName,
							"method", method,
							"recover", r)
					}
				}()
				h(method, params)
			}()
		}
	}
}

// SetNotificationHandler installs a handler for unsolicited JSON-RPC
// notifications such as notifications/tools/list_changed. Safe to call at
// any time — each notification runs the currently-installed handler.
// Passing nil removes the handler.
func (c *Client) SetNotificationHandler(h NotificationHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notificationHandler = h
}

// request sends a request and waits for a response.
func (c *Client) request(ctx context.Context, method string, params any) (*JSONRPCMessage, error) {
	id := atomic.AddInt64(&c.nextID, 1)

	// Create response channel
	respCh := make(chan *JSONRPCMessage, 1)
	c.pendingMu.Lock()
	c.pending[id] = respCh
	c.pendingMu.Unlock()

	// Clean up on return
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	// Build and send request
	msg := &JSONRPCMessage{
		ID:     id,
		Method: method,
		Params: params,
	}

	if err := c.transport.Send(msg); err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	// Wait for response with timeout
	timeout := c.config.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-respCh:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp, nil
	case <-timer.C:
		return nil, fmt.Errorf("request timeout after %v", timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// notify sends a notification (no response expected).
func (c *Client) notify(method string, params any) error {
	msg := &JSONRPCMessage{
		Method: method,
		Params: params,
	}
	return c.transport.Send(msg)
}

// Initialize initializes the connection with the MCP server.
//
// Locking discipline: takes c.initMu to serialize concurrent Initialize
// calls, but does NOT hold c.mu across the network round-trip. Holding
// c.mu (write) here would deadlock receiveLoop's notification path
// (handleMessage → c.mu.RLock to read notificationHandler) any time a
// server emits a notification before the InitializeResult — see the
// regression test in client_init_deadlock_test.go.
func (c *Client) Initialize(ctx context.Context) error {
	c.initMu.Lock()
	defer c.initMu.Unlock()

	c.mu.RLock()
	already := c.initialized
	c.mu.RUnlock()
	if already {
		return nil
	}

	// Send initialize request
	params := &InitializeParams{
		ProtocolVersion: ProtocolVersion,
		ClientInfo: &ClientInfo{
			Name:    "gokin",
			Version: "1.0.0",
		},
		Capabilities: map[string]any{}, // We don't advertise any client capabilities yet
	}

	resp, err := c.request(ctx, MethodInitialize, params)
	if err != nil {
		return fmt.Errorf("initialize failed: %w", err)
	}

	// Parse result
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return fmt.Errorf("failed to marshal initialize result: %w", err)
	}

	var result InitializeResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return fmt.Errorf("failed to parse initialize result: %w", err)
	}

	// Send initialized notification before publishing state — if the
	// server rejects it we don't want IsInitialized() to start returning
	// true.
	if err := c.notify(MethodInitialized, nil); err != nil {
		return fmt.Errorf("failed to send initialized notification: %w", err)
	}

	// Publish state under c.mu briefly. By this point all network I/O
	// is done so receiveLoop won't be blocked behind us.
	c.mu.Lock()
	c.serverInfo = result.ServerInfo
	c.initialized = true
	c.mu.Unlock()

	logging.Info("MCP server initialized",
		"name", c.serverName,
		"server", result.ServerInfo.Name,
		"version", result.ServerInfo.Version)

	return nil
}

// ListTools retrieves the list of tools from the server.
func (c *Client) ListTools(ctx context.Context) ([]*ToolInfo, error) {
	c.mu.RLock()
	if !c.initialized {
		c.mu.RUnlock()
		return nil, fmt.Errorf("client not initialized")
	}
	c.mu.RUnlock()

	resp, err := c.request(ctx, MethodToolsList, nil)
	if err != nil {
		return nil, fmt.Errorf("tools/list failed: %w", err)
	}

	// Parse result
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tools result: %w", err)
	}

	var result ListToolsResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to parse tools result: %w", err)
	}

	c.mu.Lock()
	c.tools = result.Tools
	c.mu.Unlock()

	logging.Debug("MCP tools listed",
		"server", c.serverName,
		"count", len(result.Tools))

	return result.Tools, nil
}

// CallTool calls a tool on the MCP server.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*CallToolResult, error) {
	c.mu.RLock()
	if !c.initialized {
		c.mu.RUnlock()
		return nil, fmt.Errorf("client not initialized")
	}
	c.mu.RUnlock()

	params := &CallToolParams{
		Name:      name,
		Arguments: args,
	}

	resp, err := c.request(ctx, MethodToolsCall, params)
	if err != nil {
		return nil, fmt.Errorf("tools/call failed: %w", err)
	}

	// Parse result
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal call result: %w", err)
	}

	var result CallToolResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to parse call result: %w", err)
	}

	logging.Debug("MCP tool called",
		"server", c.serverName,
		"tool", name,
		"is_error", result.IsError)

	return &result, nil
}

// ListResources retrieves the list of resources from the server.
func (c *Client) ListResources(ctx context.Context) ([]*Resource, error) {
	c.mu.RLock()
	if !c.initialized {
		c.mu.RUnlock()
		return nil, fmt.Errorf("client not initialized")
	}
	c.mu.RUnlock()

	resp, err := c.request(ctx, MethodResourcesList, nil)
	if err != nil {
		return nil, fmt.Errorf("resources/list failed: %w", err)
	}

	// Parse result
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal resources result: %w", err)
	}

	var result ListResourcesResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to parse resources result: %w", err)
	}

	c.mu.Lock()
	c.resources = result.Resources
	c.mu.Unlock()

	logging.Debug("MCP resources listed",
		"server", c.serverName,
		"count", len(result.Resources))

	return result.Resources, nil
}

// GetServerInfo returns information about the connected server.
func (c *Client) GetServerInfo() *ServerInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverInfo
}

// GetServerName returns the configured server name.
func (c *Client) GetServerName() string {
	return c.serverName
}

// IsInitialized returns whether the client has been initialized.
func (c *Client) IsInitialized() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.initialized
}

// ConsecutiveFails returns the number of consecutive receive failures.
func (c *Client) ConsecutiveFails() int {
	c.reconnectMu.Lock()
	defer c.reconnectMu.Unlock()
	return c.consecutiveFails
}

// Close closes the client and releases resources.
func (c *Client) Close() error {
	c.cancel()

	// Wait for receive loop to finish
	closeTimer := time.NewTimer(5 * time.Second)
	select {
	case <-c.done:
		closeTimer.Stop()
	case <-closeTimer.C:
		logging.Warn("MCP client receive loop did not stop in time")
	}

	// Wait for any in-flight notification handlers. Capped at 2s so a
	// pathologically slow or hung handler can't block shutdown indefinitely.
	notifDone := make(chan struct{})
	go func() {
		c.notifWg.Wait()
		close(notifDone)
	}()
	notifTimer := time.NewTimer(2 * time.Second)
	select {
	case <-notifDone:
		notifTimer.Stop()
	case <-notifTimer.C:
		logging.Warn("MCP notification handlers still running at close",
			"server", c.serverName)
	}

	// Close transport
	if err := c.transport.Close(); err != nil {
		return fmt.Errorf("failed to close transport: %w", err)
	}

	logging.Debug("MCP client closed", "server", c.serverName)
	return nil
}

// Ping sends a ping request to verify the connection is alive.
func (c *Client) Ping(ctx context.Context) error {
	c.mu.RLock()
	if !c.initialized {
		c.mu.RUnlock()
		return fmt.Errorf("client not initialized")
	}
	c.mu.RUnlock()

	_, err := c.request(ctx, MethodPing, nil)
	return err
}
