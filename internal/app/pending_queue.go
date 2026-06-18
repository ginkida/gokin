package app

// Type-ahead pending queue: messages submitted while a request is processing
// are queued FIFO and dispatched one at a time as requests complete
// (message_processor's defer). Before this the slot was single-message and a
// second type-ahead REPLACED the first — silently dropping user input.
//
// All access goes through these helpers under a.pendingMu; nothing else may
// touch a.pendingQueue directly.

// maxPendingQueue bounds the type-ahead queue. Overflow REJECTS the new
// message (with explicit feedback at the call site) rather than dropping an
// older one — queued messages are user input, never silently discarded.
const maxPendingQueue = 5

// enqueuePending appends a message to the type-ahead queue. Returns the
// message's 1-based queue position, or ok=false when the queue is full (the
// message was NOT queued).
func (a *App) enqueuePending(message string) (pos int, ok bool) {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	if len(a.pendingQueue) >= maxPendingQueue {
		return len(a.pendingQueue), false
	}
	a.pendingQueue = append(a.pendingQueue, message)
	return len(a.pendingQueue), true
}

// dequeuePending pops the oldest queued message. ok=false when empty.
// remaining is the queue length after the pop (for the UI badge).
func (a *App) dequeuePending() (message string, remaining int, ok bool) {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	if len(a.pendingQueue) == 0 {
		return "", 0, false
	}
	message = a.pendingQueue[0]
	// Fresh backing array: the old array's head slot would otherwise pin the
	// dispatched message's memory for the queue's lifetime.
	a.pendingQueue = append([]string(nil), a.pendingQueue[1:]...)
	return message, len(a.pendingQueue), true
}

// drainPending clears the entire type-ahead queue and returns how many messages
// were discarded. Called on cancel (Esc/Ctrl+C): without it, the cancelled
// request's dispatch defer would pop a queued message and start a NEW request
// right after the user asked to stop.
func (a *App) drainPending() int {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	n := len(a.pendingQueue)
	a.pendingQueue = nil
	return n
}

// pendingCount returns the number of queued type-ahead messages.
func (a *App) pendingCount() int {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	return len(a.pendingQueue)
}

// pendingSnapshot returns a copy of the queue (oldest first) for recovery
// snapshots and /recovery inspection.
func (a *App) pendingSnapshot() []string {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	if len(a.pendingQueue) == 0 {
		return nil
	}
	return append([]string(nil), a.pendingQueue...)
}
