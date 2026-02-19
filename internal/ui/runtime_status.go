package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type runtimeStatusProvider interface {
	GetUIRuntimeStatus() RuntimeStatusSnapshot
}

// runtimeStatusCmd returns a tea.Cmd that fetches runtime status in the background.
// This avoids blocking the Bubble Tea event loop on App locks.
func (m *Model) runtimeStatusCmd() tea.Cmd {
	if m.app == nil {
		return nil
	}
	provider, ok := m.app.(runtimeStatusProvider)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		return RuntimeStatusMsg{Status: provider.GetUIRuntimeStatus()}
	}
}

// handleRuntimeStatusMsg applies a background-fetched runtime status snapshot.
func (m *Model) handleRuntimeStatusMsg(msg RuntimeStatusMsg) {
	m.runtimeStatus = msg.Status
	m.lastRuntimeRefresh = time.Now()
}
