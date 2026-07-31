package app

import "strings"

// TrySteerHeadless injects a cross-process follow-up into the currently active
// headless executor loop. It never creates a new foreground owner: callers
// retain the message and run it as the next turn when this returns false.
func (a *App) TrySteerHeadless(message string) bool {
	if a == nil || a.executor == nil {
		return false
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return false
	}
	a.mu.Lock()
	active := a.headlessRunActive && a.processing && !a.shuttingDown && !a.dropSteerLeftovers
	a.mu.Unlock()
	if !active {
		return false
	}
	return a.executor.TryQueueUserSteer(a.expandAtReferences(message))
}
