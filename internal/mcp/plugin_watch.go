package mcp

import (
	"context"
	"time"

	"gokin/internal/hooks"
)

// StartPluginPolicyWatch retires revoked/changed connections without starting
// new ones. Loading new executables/endpoints still requires startup or reload.
func (m *Manager) StartPluginPolicyWatch(ctx context.Context, authority *hooks.Manager, workDir string, allowDefault ...bool) {
	m.pluginWatchMu.Lock()
	defer m.pluginWatchMu.Unlock()
	m.mu.RLock()
	closed := m.closed
	m.mu.RUnlock()
	m.pluginWatchDefault = len(allowDefault) == 0 || allowDefault[0]
	if closed || m.pluginWatchCancel != nil {
		return
	}
	watchCtx, cancel := context.WithCancel(ctx)
	m.pluginWatchCancel = cancel
	m.pluginWatchDone = make(chan struct{})
	done := m.pluginWatchDone
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				m.pluginWatchMu.Lock()
				mode := m.pluginWatchDefault
				m.pluginWatchMu.Unlock()
				m.prunePluginConnections(authority, workDir, mode)
			}
		}
	}()
}

func (m *Manager) StopPluginPolicyWatch() {
	m.pluginWatchMu.Lock()
	cancel, done := m.pluginWatchCancel, m.pluginWatchDone
	m.pluginWatchCancel = nil
	m.pluginWatchDone = nil
	m.pluginWatchMu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (m *Manager) prunePluginConnections(authority *hooks.Manager, workDir string, allowDefault ...bool) {
	current, err := LoadClaudePluginServersUnguarded(authority, workDir, allowDefault...)
	valid := map[string]string{}
	if err == nil {
		for _, cfg := range current {
			valid[cfg.Name] = cfg.pluginFingerprint
		}
	}
	m.mu.Lock()
	var retired []*Client
	var changed []string
	for name, cfg := range m.servers {
		if cfg.pluginFingerprint != "" && cfg.pluginFingerprint != valid[name] {
			if client := m.detachClientLocked(name); client != nil {
				retired = append(retired, client)
				changed = append(changed, name)
			}
		}
	}
	callback := m.onToolsChanged
	m.mu.Unlock()
	for _, client := range retired {
		_ = client.Close()
	}
	if callback != nil {
		for _, name := range changed {
			callback(name)
		}
	}
}
