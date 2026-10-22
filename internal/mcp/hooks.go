package mcp

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gokin/internal/hooks"
	"gokin/internal/logging"
)

// CallHookTool executes a configured handler directly on its exact MCP server.
// It applies server policy before connecting and again at the request boundary.
// The hook's own configuration is authority to run it, like a command hook.
func (m *Manager) CallHookTool(ctx context.Context, server, name string, args map[string]any) (hooks.MCPHookOutput, error) {
	var output hooks.MCPHookOutput
	if err := ctx.Err(); err != nil {
		return output, err
	}
	m.mu.RLock()
	cfg, ok := m.servers[server]
	paused := ok && cfg.Paused
	m.mu.RUnlock()
	if !ok {
		return output, fmt.Errorf("MCP hook server is not configured")
	}
	if paused {
		return output, fmt.Errorf("MCP hook server is paused")
	}
	if !cfg.ToolAllowed(name) {
		return output, fmt.Errorf("MCP hook tool is outside the server allowlist")
	}
	if err := cfg.CheckReady(ctx); err != nil {
		return output, err
	}
	if _, ok := m.GetClient(server); !ok {
		timeout := defaultServerTimeout
		if raw := os.Getenv("MCP_TIMEOUT"); raw != "" {
			if ms, err := strconv.ParseInt(raw, 10, 64); err == nil && ms > 0 && ms <= 600000 {
				timeout = time.Duration(ms) * time.Millisecond
			}
		}
		connectCtx, cancel := context.WithTimeout(ctx, timeout)
		err := m.connect(connectCtx, server, cfg)
		cancel()
		if err != nil {
			return output, err
		}
		m.notifyHookConnection(server)
	}
	// Only published, currently permitted tools can be used. This preserves
	// server namespaces and keeps unknown/removed tools off the wire.
	m.mu.RLock()
	var tool *MCPTool
	client := m.clients[server]
	if !m.closed && m.servers[server] == cfg && !cfg.Paused && client != nil && client.config == cfg {
		for _, candidate := range m.tools {
			if candidate, ok := candidate.(*MCPTool); ok && candidate.serverName == server && candidate.toolName == name && candidate.client == client {
				tool = candidate
				break
			}
		}
	}
	m.mu.RUnlock()
	if tool == nil || client == nil || !client.IsInitialized() {
		return output, fmt.Errorf("MCP hook tool is unavailable")
	}
	if err := tool.Validate(args); err != nil {
		return output, fmt.Errorf("invalid MCP hook tool arguments: %w", err)
	}
	if err := cfg.CheckReady(ctx); err != nil {
		return output, err
	}
	m.mu.RLock()
	ready := !m.closed && m.servers[server] == cfg && !cfg.Paused && m.clients[server] == client
	m.mu.RUnlock()
	if !ready {
		return output, fmt.Errorf("MCP hook server changed before execution")
	}
	result, err := client.CallTool(ctx, name, args)
	if err != nil {
		return output, err
	}
	if result == nil {
		return output, fmt.Errorf("MCP hook tool returned no result")
	}
	output.IsError = result.IsError
	var text strings.Builder
	for _, block := range result.Content {
		if block == nil || block.Type != "text" {
			continue
		}
		extra := 0
		if text.Len() > 0 {
			extra = 1
		}
		if text.Len()+len(block.Text)+extra > 64<<10 {
			return output, fmt.Errorf("MCP hook text output exceeds 64 KiB")
		}
		if text.Len() > 0 {
			text.WriteByte('\n')
		}
		text.WriteString(block.Text)
	}
	output.Text = text.String()
	return output, nil
}

func (m *Manager) notifyHookConnection(name string) {
	m.mu.RLock()
	callback := m.onToolsChanged
	m.mu.RUnlock()
	if callback == nil {
		return
	}
	go func() {
		defer func() {
			if value := recover(); value != nil {
				logging.Warn("MCP hook connection callback panicked", "server", name, "recover", value)
			}
		}()
		callback(name)
	}()
}
