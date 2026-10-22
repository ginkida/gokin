package mcp

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"gokin/internal/hooks"
)

// ClaudePluginServerNames snapshots only plugin-owned configurations. Native
// servers do not participate in the plugin reload prompt-cache decision.
func (m *Manager) ClaudePluginServerNames() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var names []string
	for name, cfg := range m.servers {
		if cfg.pluginFingerprint != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// ReconcileClaudePlugins keeps unchanged connections and retires removed or
// changed sources before dialing replacements. Native server configs are never
// overwritten; callers must reconcile every returned server's tool registry
// even when one replacement fails to connect.
func (m *Manager) ReconcileClaudePlugins(ctx context.Context, authority *hooks.Manager, workDir string, allowDefault ...bool) ([]string, error) {
	m.pluginReloadMu.Lock()
	defer m.pluginReloadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	loaded, err := LoadClaudePluginServers(authority, workDir, allowDefault...)
	if err != nil {
		return nil, err
	}
	next := make(map[string]*ServerConfig, len(loaded))
	for _, cfg := range loaded {
		if len(allowDefault) > 0 && !allowDefault[0] && !cfg.PluginExplicitlyEnabled() {
			continue
		}
		next[cfg.Name] = cfg
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, fmt.Errorf("MCP manager is shut down")
	}
	for name := range next {
		if current := m.servers[name]; current != nil && current.pluginFingerprint == "" {
			m.mu.Unlock()
			return nil, fmt.Errorf("plugin MCP server %q conflicts with a native server", name)
		}
	}
	affected := map[string]bool{}
	var retired []*Client
	for name, current := range m.servers {
		if current.pluginFingerprint == "" {
			continue
		}
		replacement := next[name]
		if replacement != nil && replacement.pluginFingerprint == current.pluginFingerprint {
			next[name] = current
			continue
		}
		if replacement != nil {
			replacement.Paused = current.Paused
		}
		if client := m.detachClientLocked(name); client != nil {
			retired = append(retired, client)
		}
		delete(m.servers, name)
		delete(m.health, name)
		affected[name] = true
	}
	var toConnect []string
	for name, cfg := range next {
		m.servers[name] = cfg
		if m.clients[name] == nil && !cfg.Paused {
			toConnect = append(toConnect, name)
			affected[name] = true
		}
	}
	m.mu.Unlock()
	for _, client := range retired {
		_ = client.Close()
	}
	sort.Strings(toConnect)
	var errs []error
	for _, name := range toConnect {
		connectCtx, cancel := context.WithTimeout(ctx, defaultServerTimeout)
		err := m.Connect(connectCtx, name)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	names := make([]string, 0, len(affected))
	for name := range affected {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, errors.Join(errs...)
}
