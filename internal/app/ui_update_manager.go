package app

import (
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// UIUpdateManager coordinates periodic UI updates
type UIUpdateManager struct {
	program          *tea.Program
	eventBroadcaster *UIEventBroadcaster

	lifecycleMu sync.Mutex
	mu          sync.RWMutex
	running     bool
	enabled     bool
}

// NewUIUpdateManager creates a new UI update manager
func NewUIUpdateManager(program *tea.Program, _ *App) *UIUpdateManager {
	return &UIUpdateManager{
		program:          program,
		eventBroadcaster: NewUIEventBroadcaster(program),
		enabled:          true,
	}
}

// Start begins periodic UI updates
func (m *UIUpdateManager) Start() {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return
	}

	if m.eventBroadcaster == nil || m.eventBroadcaster.IsStopped() {
		m.eventBroadcaster = NewUIEventBroadcaster(m.program)
		if !m.enabled {
			m.eventBroadcaster.Disable()
		}
	}
	m.running = true
}

// Stop stops periodic UI updates
func (m *UIUpdateManager) Stop() {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	broadcaster := m.eventBroadcaster
	m.mu.Unlock()

	if broadcaster != nil {
		broadcaster.Stop()
	}
}

// IsRunning returns whether the update manager is running
func (m *UIUpdateManager) IsRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.running
}

// BroadcastTaskStart broadcasts a task start event
func (m *UIUpdateManager) BroadcastTaskStart(taskID, message, planType string) {
	if broadcaster := m.broadcasterForSend(); broadcaster != nil {
		broadcaster.BroadcastTaskStart(taskID, message, planType)
	}
}

// BroadcastTaskComplete broadcasts a task completion event
func (m *UIUpdateManager) BroadcastTaskComplete(taskID string, success bool, duration time.Duration, err error, planType string) {
	if broadcaster := m.broadcasterForSend(); broadcaster != nil {
		broadcaster.BroadcastTaskComplete(taskID, success, duration, err, planType)
	}
}

// BroadcastTaskProgress broadcasts a task progress event
func (m *UIUpdateManager) BroadcastTaskProgress(taskID string, progress float64, message string) {
	if broadcaster := m.broadcasterForSend(); broadcaster != nil {
		broadcaster.BroadcastTaskProgress(taskID, progress, message)
	}
}

func (m *UIUpdateManager) broadcasterForSend() *UIEventBroadcaster {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.running {
		return nil
	}
	return m.eventBroadcaster
}

// Enable enables event broadcasting
func (m *UIUpdateManager) Enable() {
	m.mu.Lock()
	m.enabled = true
	broadcaster := m.eventBroadcaster
	m.mu.Unlock()
	if broadcaster != nil {
		broadcaster.Enable()
	}
}

// Disable disables event broadcasting
func (m *UIUpdateManager) Disable() {
	m.mu.Lock()
	m.enabled = false
	broadcaster := m.eventBroadcaster
	m.mu.Unlock()
	if broadcaster != nil {
		broadcaster.Disable()
	}
}
