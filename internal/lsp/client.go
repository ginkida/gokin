package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Code     any    `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}
type diagnosticBatch struct {
	URI         string       `json:"uri"`
	Version     *int         `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type client struct {
	process      *process
	cfg          *Config
	mu           sync.Mutex
	nextID       atomic.Int64
	pending      map[string]chan rpcMessage
	failed       error
	done         chan struct{}
	diagnostics  map[string]diagnosticBatch
	versions     map[string]int
	updates      chan struct{}
	capabilities map[string]json.RawMessage
	encoding     string
	syncKind     int
	openClose    bool
	save         bool
}

func newClient(ctx context.Context, cfg *Config) (*client, error) {
	p, err := startProcess(ctx, cfg)
	if err != nil {
		return nil, err
	}
	c := &client{process: p, cfg: cfg, pending: map[string]chan rpcMessage{}, done: make(chan struct{}), diagnostics: map[string]diagnosticBatch{}, versions: map[string]int{}, updates: make(chan struct{}, 1), encoding: "utf-16"}
	go c.receive()
	var initialized struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	err = c.request(ctx, "initialize", map[string]any{
		"processId": nil, "clientInfo": map[string]any{"name": "gokin", "version": "1"}, "rootUri": fileURI(cfg.WorkspaceFolder),
		"workspaceFolders": []any{map[string]any{"uri": fileURI(cfg.WorkspaceFolder), "name": "workspace"}}, "initializationOptions": cfg.InitializationOptions,
		"capabilities": map[string]any{"general": map[string]any{"positionEncodings": []string{"utf-16", "utf-8", "utf-32"}}, "workspace": map[string]any{"configuration": true, "workspaceFolders": true, "applyEdit": false}, "textDocument": map[string]any{"synchronization": map[string]any{"didSave": true}, "publishDiagnostics": map[string]any{"versionSupport": true}, "definition": map[string]any{"linkSupport": true}, "implementation": map[string]any{"linkSupport": true}, "documentSymbol": map[string]any{"hierarchicalDocumentSymbolSupport": true}, "diagnostic": map[string]any{"dynamicRegistration": false}, "hover": map[string]any{"contentFormat": []string{"plaintext", "markdown"}}}},
	}, &initialized)
	if err == nil {
		c.capabilities = initialized.Capabilities
		if raw := c.capabilities["positionEncoding"]; len(raw) > 0 {
			_ = json.Unmarshal(raw, &c.encoding)
		}
		if c.encoding != "utf-8" && c.encoding != "utf-16" && c.encoding != "utf-32" {
			err = fmt.Errorf("unsupported LSP position encoding %q", c.encoding)
		}
		if raw := c.capabilities["textDocumentSync"]; len(raw) > 0 {
			var options struct {
				OpenClose bool            `json:"openClose"`
				Change    int             `json:"change"`
				Save      json.RawMessage `json:"save"`
			}
			if json.Unmarshal(raw, &c.syncKind) == nil {
				c.openClose = c.syncKind != 0
			} else if json.Unmarshal(raw, &options) == nil {
				c.syncKind = options.Change
				c.openClose = options.OpenClose
				c.save = len(options.Save) > 0 && string(options.Save) != "false" && string(options.Save) != "null"
			}
		}
	}
	if err == nil {
		err = c.notify(ctx, "initialized", map[string]any{})
	}
	if err == nil && cfg.Settings != nil {
		err = c.notify(ctx, "workspace/didChangeConfiguration", map[string]any{"settings": cfg.Settings})
	}
	if err != nil {
		p.forceClose()
		shutdown, cancel := context.WithTimeout(context.Background(), cfg.timeout("shutdown"))
		p.finish(shutdown)
		cancel()
		return c, err
	}
	return c, nil
}

func (c *client) receive() {
	defer close(c.done)
	for {
		message, err := readFrame(c.process.reader)
		if err != nil {
			c.mu.Lock()
			c.failed = err
			c.mu.Unlock()
			c.process.forceClose()
			return
		}
		if message.Method != "" {
			if len(message.ID) > 0 {
				c.serverRequest(message)
				continue
			}
			if message.Method == "textDocument/publishDiagnostics" {
				var batch diagnosticBatch
				if json.Unmarshal(message.Params, &batch) == nil && len(batch.Diagnostics) <= 10000 {
					c.mu.Lock()
					version, open := c.versions[batch.URI]
					if open && (batch.Version == nil || *batch.Version == version) {
						c.diagnostics[batch.URI] = batch
						select {
						case c.updates <- struct{}{}:
						default:
						}
					}
					c.mu.Unlock()
				}
			}
			continue
		}
		c.mu.Lock()
		pending := c.pending[string(message.ID)]
		c.mu.Unlock()
		if pending != nil {
			select {
			case pending <- message:
			default:
			}
		}
	}
}

func (c *client) serverRequest(message rpcMessage) {
	var result any
	var rpcErr *rpcError
	switch message.Method {
	case "workspace/configuration":
		var params struct {
			Items []struct {
				Section string `json:"section"`
			} `json:"items"`
		}
		if json.Unmarshal(message.Params, &params) != nil || len(params.Items) > 128 {
			rpcErr = &rpcError{-32602, "invalid configuration request"}
			break
		}
		values := make([]any, len(params.Items))
		for i, item := range params.Items {
			v := c.cfg.Settings
			if item.Section != "" {
				for _, part := range strings.Split(item.Section, ".") {
					m, ok := v.(map[string]any)
					if !ok {
						v = nil
						break
					}
					v = m[part]
				}
			}
			values[i] = v
		}
		result = values
	case "workspace/workspaceFolders":
		result = []any{map[string]any{"uri": fileURI(c.cfg.WorkspaceFolder), "name": "workspace"}}
	case "workspace/applyEdit":
		result = map[string]any{"applied": false, "failureReason": "gokin LSP navigation does not apply server edits"}
	case "window/workDoneProgress/create":
		result = nil
	default:
		rpcErr = &rpcError{-32601, "client method not supported"}
	}
	data, _ := json.Marshal(result)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.process.send(ctx, rpcMessage{ID: message.ID, Result: data, Error: rpcErr})
}

func (c *client) request(ctx context.Context, method string, params, out any) error {
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	id := strconv.FormatInt(c.nextID.Add(1), 10)
	response := make(chan rpcMessage, 1)
	c.mu.Lock()
	if c.failed != nil {
		err = c.failed
		c.mu.Unlock()
		return fmt.Errorf("LSP connection stopped: %w", err)
	}
	c.pending[id] = response
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.process.send(ctx, rpcMessage{ID: json.RawMessage(id), Method: method, Params: data}); err != nil {
		return err
	}
	select {
	case message := <-response:
		if message.Error != nil {
			return message.Error
		}
		if len(message.Result) == 0 {
			return fmt.Errorf("LSP response has no result")
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(message.Result, out)
	case <-c.done:
		c.mu.Lock()
		err := c.failed
		c.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		return fmt.Errorf("LSP connection stopped: %w", err)
	case <-ctx.Done():
		cancelCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = c.notify(cancelCtx, "$/cancelRequest", map[string]any{"id": c.nextIDFor(id)})
		return ctx.Err()
	}
}
func (c *client) nextIDFor(id string) int64 { n, _ := strconv.ParseInt(id, 10, 64); return n }
func (c *client) notify(ctx context.Context, method string, params any) error {
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.process.send(ctx, rpcMessage{Method: method, Params: data})
}
func (c *client) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}
func (c *client) close() {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.timeout("shutdown"))
	defer cancel()
	if c.alive() {
		_ = c.request(ctx, "shutdown", nil, nil)
		_ = c.notify(ctx, "exit", nil)
	}
	c.process.finish(ctx)
}
