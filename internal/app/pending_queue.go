package app

import (
	"context"

	"gokin/internal/tools"
)

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

type pendingRequest struct {
	message             string
	recoveryCheckpoints []tools.ToolCheckpoint
	recoveryID          string
	recoverySessionID   string
	recoveryMemoryQuery string
	recoveryEpoch       uint64
	// postCancelUserIntent marks input explicitly submitted by the user after
	// Esc returned but before the cancelled foreground's defer released its
	// processing slot. Only this provenance may cross the still-closed cancel
	// gate; late steer/hooks/timers remain unmarked and cannot resurrect work.
	postCancelUserIntent bool
}

type sideEffectRecoveryContextKey struct{}

type conversationLineageContextKey struct{}

type conversationLineage struct {
	sessionID string
	epoch     uint64
}

type sideEffectRecoveryContext struct {
	checkpoints []tools.ToolCheckpoint
	recoveryID  string
	sessionID   string
}

func withSideEffectRecovery(ctx context.Context, checkpoints []tools.ToolCheckpoint) context.Context {
	return withPersistedSideEffectRecovery(ctx, "", "", checkpoints)
}

func withPersistedSideEffectRecovery(ctx context.Context, recoveryID, sessionID string, checkpoints []tools.ToolCheckpoint) context.Context {
	if recoveryID == "" && sessionID == "" && len(checkpoints) == 0 {
		return ctx
	}
	return context.WithValue(ctx, sideEffectRecoveryContextKey{}, sideEffectRecoveryContext{
		checkpoints: append([]tools.ToolCheckpoint(nil), checkpoints...),
		recoveryID:  recoveryID,
		sessionID:   sessionID,
	})
}

func sideEffectRecoveryFromContext(ctx context.Context) []tools.ToolCheckpoint {
	recovery, _ := sideEffectRecoveryContextFromContext(ctx)
	return recovery.checkpoints
}

func sideEffectRecoveryContextFromContext(ctx context.Context) (sideEffectRecoveryContext, bool) {
	if ctx == nil {
		return sideEffectRecoveryContext{}, false
	}
	recovery, ok := ctx.Value(sideEffectRecoveryContextKey{}).(sideEffectRecoveryContext)
	if !ok {
		return sideEffectRecoveryContext{}, false
	}
	recovery.checkpoints = append([]tools.ToolCheckpoint(nil), recovery.checkpoints...)
	return recovery, true
}

func withConversationLineage(ctx context.Context, sessionID string, epoch uint64) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, conversationLineageContextKey{}, conversationLineage{
		sessionID: sessionID,
		epoch:     epoch,
	})
}

func conversationLineageFromContext(ctx context.Context) (conversationLineage, bool) {
	if ctx == nil {
		return conversationLineage{}, false
	}
	lineage, ok := ctx.Value(conversationLineageContextKey{}).(conversationLineage)
	return lineage, ok
}

func (a *App) captureConversationLineage() conversationLineage {
	if a == nil {
		return conversationLineage{}
	}
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	lineage := conversationLineage{epoch: a.recoveryEpoch.Load()}
	if a.session != nil {
		lineage.sessionID = a.session.GetID()
	}
	return lineage
}

func (a *App) conversationLineageMatches(lineage conversationLineage) bool {
	if a == nil {
		return false
	}
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	return a.session != nil && a.session.GetID() == lineage.sessionID &&
		a.recoveryEpoch.Load() == lineage.epoch
}

// enqueuePending appends a message to the type-ahead queue. Returns the
// message's 1-based queue position, or ok=false when the queue is full (the
// message was NOT queued).
func (a *App) enqueuePending(message string) (pos int, ok bool) {
	return a.enqueuePendingRequest(pendingRequest{message: message})
}

func (a *App) enqueuePostCancelUserPending(message string) (pos int, ok bool) {
	return a.enqueuePendingRequest(pendingRequest{
		message:              message,
		postCancelUserIntent: true,
	})
}

// enqueueRecoveryPending keeps the exact side-effect checkpoint generation
// attached to an automatic retry while it waits behind newer foreground work.
// A plain string queue loses this lineage; the intervening request resets the
// executor journal and the retry can then repeat already-completed writes.
func (a *App) enqueueRecoveryPending(message, memoryQuery, recoveryID, sessionID string, recoveryEpoch uint64, checkpoints []tools.ToolCheckpoint) (pos int, ok bool) {
	return a.enqueuePendingRequest(pendingRequest{
		message:             message,
		recoveryCheckpoints: append([]tools.ToolCheckpoint(nil), checkpoints...),
		recoveryID:          recoveryID,
		recoverySessionID:   sessionID,
		recoveryMemoryQuery: memoryQuery,
		recoveryEpoch:       recoveryEpoch,
	})
}

func (a *App) enqueuePendingRequest(request pendingRequest) (pos int, ok bool) {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	if len(a.pendingQueue) >= maxPendingQueue {
		return len(a.pendingQueue), false
	}
	a.pendingQueue = append(a.pendingQueue, request)
	return len(a.pendingQueue), true
}

// prependPending places an already-accepted foreground continuation ahead of
// later type-ahead. It intentionally bypasses maxPendingQueue: the continuation
// belongs to the command that already owns the foreground slot, while the cap
// applies only to additional user submissions.
func (a *App) prependPending(message string) int {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	a.pendingQueue = append([]pendingRequest{{message: message}}, a.pendingQueue...)
	return len(a.pendingQueue)
}

// dequeuePending pops the oldest queued message. ok=false when empty.
// remaining is the queue length after the pop (for the UI badge).
func (a *App) dequeuePending() (message string, remaining int, ok bool) {
	request, remaining, ok := a.dequeuePendingRequest()
	return request.message, remaining, ok
}

func (a *App) dequeuePendingRequest() (request pendingRequest, remaining int, ok bool) {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	if len(a.pendingQueue) == 0 {
		return pendingRequest{}, 0, false
	}
	request = a.pendingQueue[0]
	// Fresh backing array: the old array's head slot would otherwise pin the
	// dispatched message's memory for the queue's lifetime.
	a.pendingQueue = append([]pendingRequest(nil), a.pendingQueue[1:]...)
	return request, len(a.pendingQueue), true
}

// dequeuePendingRequestForSession defers recovery requests claimed for a
// session which is no longer active. Ordinary type-ahead remains valid across
// /resume and keeps its FIFO order; an old session's checkpoint generation
// must never execute against the newly-selected conversation. Stale recoveries
// stay queued (instead of being discarded) so switching back can dispatch them
// or a pre-switch release can return them durably to scheduled state.
func (a *App) dequeuePendingRequestForSession(activeSessionID string, activeRecoveryEpoch uint64) (request pendingRequest, remaining int, ok bool, stale int) {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	for i := range a.pendingQueue {
		request = a.pendingQueue[i]
		if request.recoverySessionID != "" &&
			(request.recoverySessionID != activeSessionID || request.recoveryEpoch != activeRecoveryEpoch) {
			stale++
			continue
		}
		a.pendingQueue = append(
			append([]pendingRequest(nil), a.pendingQueue[:i]...),
			a.pendingQueue[i+1:]...,
		)
		return request, len(a.pendingQueue), true, stale
	}
	return pendingRequest{}, len(a.pendingQueue), false, stale
}

// dequeuePostCancelUserPending closes a cancelled turn without reopening the
// gate to untrusted late producers. Esc already drained the pre-boundary FIFO;
// anything subsequently enqueued without explicit-user provenance is a late
// callback/programmatic leftover and is discarded. Multiple explicit inputs
// retain their FIFO order and are handed off one per foreground completion.
func (a *App) dequeuePostCancelUserPending() (request pendingRequest, remaining int, ok bool, discarded int) {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	explicit := make([]pendingRequest, 0, len(a.pendingQueue))
	for _, queued := range a.pendingQueue {
		if queued.postCancelUserIntent {
			explicit = append(explicit, queued)
			continue
		}
		discarded++
	}
	if len(explicit) == 0 {
		a.pendingQueue = nil
		return pendingRequest{}, 0, false, discarded
	}
	request = explicit[0]
	a.pendingQueue = append([]pendingRequest(nil), explicit[1:]...)
	return request, len(a.pendingQueue), true, discarded
}

// drainPending clears the entire type-ahead queue and returns how many messages
// were discarded. Called on cancel (Esc/Ctrl+C): without it, the cancelled
// request's dispatch defer would pop a queued message and start a NEW request
// right after the user asked to stop.
func (a *App) drainPending() int {
	return len(a.drainPendingRequests())
}

func (a *App) drainPendingRequests() []pendingRequest {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	drained := append([]pendingRequest(nil), a.pendingQueue...)
	a.pendingQueue = nil
	return drained
}

func (a *App) recoveryPendingForSession(sessionID string) []pendingRequest {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	var requests []pendingRequest
	for _, request := range a.pendingQueue {
		if request.recoverySessionID == sessionID && request.recoveryID != "" {
			requests = append(requests, request)
		}
	}
	return requests

}

func (a *App) dropRecoveryPendingKeys(keys map[recoveryTimerKey]struct{}) int {
	if len(keys) == 0 {
		return 0
	}
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	kept := make([]pendingRequest, 0, len(a.pendingQueue))
	dropped := 0
	for _, request := range a.pendingQueue {
		key := recoveryTimerKey{
			sessionID: request.recoverySessionID, recoveryID: request.recoveryID,
		}
		if _, match := keys[key]; match {
			dropped++
			continue
		}
		kept = append(kept, request)
	}
	a.pendingQueue = kept
	return dropped
}

func (a *App) dropRecoveryPendingForSession(sessionID string) int {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	if len(a.pendingQueue) == 0 {
		return 0
	}
	kept := make([]pendingRequest, 0, len(a.pendingQueue))
	dropped := 0
	for _, request := range a.pendingQueue {
		if request.recoverySessionID != "" && request.recoverySessionID == sessionID {
			dropped++
			continue
		}
		kept = append(kept, request)
	}
	a.pendingQueue = kept
	return dropped
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
	messages := make([]string, len(a.pendingQueue))
	for i := range a.pendingQueue {
		messages[i] = a.pendingQueue[i].message
	}
	return messages
}
