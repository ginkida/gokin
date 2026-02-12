package ui

import "time"

type runtimeStatusProvider interface {
	GetUIRuntimeStatus() RuntimeStatusSnapshot
}

func (m *Model) refreshRuntimeStatus() {
	if m.app == nil {
		return
	}
	provider, ok := m.app.(runtimeStatusProvider)
	if !ok {
		return
	}
	m.runtimeStatus = provider.GetUIRuntimeStatus()
	m.lastRuntimeRefresh = time.Now()
}
