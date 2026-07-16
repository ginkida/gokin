package app

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"gokin/internal/chat"
	"gokin/internal/fileutil"
	"gokin/internal/logging"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

const (
	maxPendingRecoveries      = 8
	recoveryStartupGrace      = 100 * time.Millisecond
	recoveryRedispatchGrace   = 250 * time.Millisecond
	recoveryQueuePollInterval = 250 * time.Millisecond
)

var (
	errPendingRecoveryUnavailable  = errors.New("pending recovery is no longer available")
	errRecoveryConversationChanged = errors.New("conversation changed before recovery persistence")
	errUnsafeRecoveryGeneration    = errors.New("unsafe recovery generation")
	errRecoveryPersistenceFailed   = errors.New("recovery persistence failed")
	errRecoveryCommitUncertain     = errors.New("recovery persistence commit is uncertain")
	errRecoveryBlockedByClaimed    = errors.New("recovery is blocked by an ambiguous claimed entry")
)

type recoveryTimerKey struct {
	sessionID  string
	recoveryID string
}

type recoveryTimerRegistration struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func newPendingRecoveryID() string {
	var raw [12]byte
	if _, err := cryptorand.Read(raw[:]); err == nil {
		return "recovery-" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("recovery-%d", time.Now().UnixNano())
}

func (a *App) clearConversationPersistenceBoundary() (int, error) {
	return a.replaceConversationPersistenceBoundary(nil)
}

// replaceConversationPersistenceBoundary is the only supported executable
// conversation-root reset. The optional rebuild runs under the session lease
// after the old retry lineage is invalidated and before the new root is saved.
func (a *App) replaceConversationPersistenceBoundary(rebuild func()) (int, error) {
	if a == nil || a.session == nil {
		return 0, nil
	}
	if a.sessionManager == nil {
		// Session persistence is intentionally absent (the supported
		// session.enabled=false runtime, plus lightweight embedded/test Apps).
		// There is no active autosave/load source to tombstone, but the in-memory
		// epoch/ledger/queue boundary is still mandatory.
		a.sessionLeaseMu.Lock()
		a.recoveryEpoch.Add(1)
		sessionID := a.session.GetID()
		a.session.Clear()
		if a.executor != nil {
			a.executor.ResetSideEffectLedger()
			a.executor.SetSideEffectDedup(false)
		}
		if rebuild != nil {
			rebuild()
		}
		a.sessionLeaseMu.Unlock()
		a.cancelRecoveryTimersForSession(sessionID)
		return a.dropRecoveryPendingForSession(sessionID), nil
	}
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	previousState := a.session.GetState()
	previousEpoch := a.recoveryEpoch.Load()
	sessionID := a.session.GetID()
	transactionErr := a.sessionManager.ApplyAndSave(func() (func(), error) {
		a.recoveryEpoch.Add(1)
		a.session.Clear()
		if a.executor != nil {
			a.executor.ResetSideEffectLedger()
			a.executor.SetSideEffectDedup(false)
		}
		if rebuild != nil {
			rebuild()
		}
		rollback := func() {
			if err := a.session.RestoreFromState(previousState); err != nil {
				logging.Error("failed to roll back conversation boundary", "error", err)
			}
			a.recoveryEpoch.Store(previousEpoch)
			a.restoreToolCheckpoints()
		}
		return rollback, nil
	})
	if transactionErr != nil && !errors.Is(transactionErr, fileutil.ErrAtomicWriteCommitUncertain) {
		return 0, transactionErr
	}
	// Drop queued executable retries only after the new root is known to be
	// visible (or commit-uncertain after replace). On a definite pre-commit
	// failure ApplyAndSave restored the prior epoch/session and those requests
	// remain valid.
	a.cancelRecoveryTimersForSession(sessionID)
	dropped := a.dropRecoveryPendingForSession(sessionID)
	if transactionErr != nil {
		return dropped, fmt.Errorf("%w: conversation boundary: %v", errRecoveryCommitUncertain, transactionErr)
	}
	return dropped, nil
}

// persistPendingRecovery durably couples a delayed automatic retry to its
// exact request/checkpoint generation before the timer is allowed to start.
// replaceID is non-empty only when a claimed retry schedules its next attempt.
func (a *App) persistPendingRecovery(
	kind, message, userMessage string,
	checkpoints []tools.ToolCheckpoint,
	attempt int,
	delay time.Duration,
	replaceID string,
	expectedSessionID string,
	expectedEpoch uint64,
) (chat.SerializedPendingRecovery, error) {
	if a == nil || a.session == nil {
		return chat.SerializedPendingRecovery{}, fmt.Errorf("%w: session is unavailable", errRecoveryConversationChanged)
	}
	if strings.TrimSpace(message) == "" {
		return chat.SerializedPendingRecovery{}, fmt.Errorf("%w: refusing to persist an empty recovery request", errUnsafeRecoveryGeneration)
	}
	serializedCheckpoints, err := serializeToolCheckpointsExact(checkpoints)
	if err != nil {
		return chat.SerializedPendingRecovery{}, fmt.Errorf("%w: %v", errUnsafeRecoveryGeneration, err)
	}
	now := time.Now()
	rateLimitAttempts, autoResumeAttempts := a.recoveryBudgetSnapshot(message)
	if kind == "rate_limit" && rateLimitAttempts < attempt {
		rateLimitAttempts = attempt
	}
	if kind == "auto_resume" && autoResumeAttempts < attempt {
		autoResumeAttempts = attempt
	}
	recovery := chat.SerializedPendingRecovery{
		ID:                 newPendingRecoveryID(),
		SessionID:          expectedSessionID,
		Message:            message,
		UserMessage:        strings.TrimSpace(userMessage),
		Checkpoints:        serializedCheckpoints,
		Kind:               kind,
		Attempt:            attempt,
		RateLimitAttempts:  rateLimitAttempts,
		AutoResumeAttempts: autoResumeAttempts,
		NotBefore:          now.Add(max(delay, 0)),
		State:              chat.PendingRecoveryScheduled,
		CreatedAt:          now,
	}
	recovery.GenerationSignature = chat.PendingRecoveryGenerationSignature(recovery)
	if err := validatePendingRecovery(recovery); err != nil {
		return chat.SerializedPendingRecovery{}, fmt.Errorf("%w: %v", errUnsafeRecoveryGeneration, err)
	}
	if a.sessionManager == nil {
		return chat.SerializedPendingRecovery{}, fmt.Errorf("%w: session persistence is unavailable", errRecoveryPersistenceFailed)
	}

	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	if a.recoveryEpoch.Load() != expectedEpoch || a.session.GetID() != expectedSessionID {
		return chat.SerializedPendingRecovery{}, errRecoveryConversationChanged
	}

	sessionID := expectedSessionID
	previous := a.session.GetPendingRecoveries()
	previousToolCheckpoints := a.session.GetToolCheckpoints()
	if replaceID == "" && len(previous) >= maxPendingRecoveries {
		return chat.SerializedPendingRecovery{}, fmt.Errorf("too many pending recoveries (%d)", len(previous))
	}
	if replaceID != "" {
		for _, old := range previous {
			if old.ID != replaceID {
				continue
			}
			oldRateLimitAttempts := old.RateLimitAttempts
			oldAutoResumeAttempts := old.AutoResumeAttempts
			if old.Kind == "rate_limit" && oldRateLimitAttempts < old.Attempt {
				oldRateLimitAttempts = old.Attempt
			}
			if old.Kind == "auto_resume" && oldAutoResumeAttempts < old.Attempt {
				oldAutoResumeAttempts = old.Attempt
			}
			recovery.RateLimitAttempts = max(recovery.RateLimitAttempts, oldRateLimitAttempts)
			recovery.AutoResumeAttempts = max(recovery.AutoResumeAttempts, oldAutoResumeAttempts)
			break
		}
		recovery.GenerationSignature = chat.PendingRecoveryGenerationSignature(recovery)
		if err := validatePendingRecovery(recovery); err != nil {
			return chat.SerializedPendingRecovery{}, fmt.Errorf("%w: %v", errUnsafeRecoveryGeneration, err)
		}
	}
	transactionErr := a.sessionManager.ApplyAndSave(func() (func(), error) {
		if a.recoveryEpoch.Load() != expectedEpoch || a.session.GetID() != expectedSessionID {
			return nil, errRecoveryConversationChanged
		}
		if !a.session.AddPendingRecovery(recovery, replaceID) {
			if replaceID != "" {
				return nil, fmt.Errorf("%w: claimed recovery %q was replaced or cleared", errRecoveryConversationChanged, replaceID)
			}
			return nil, fmt.Errorf("duplicate pending recovery id %q", recovery.ID)
		}

		// Keep the legacy/current-turn journal in the same durable snapshot. The
		// embedded generation above remains authoritative for this request.
		a.syncToolCheckpoints()
		rollback := func() {
			a.session.RemovePendingRecovery(recovery.ID)
			if replaceID != "" {
				for _, old := range previous {
					if old.ID == replaceID {
						a.session.AddPendingRecovery(old, "")
						break
					}
				}
			}
			a.session.SetToolCheckpoints(previousToolCheckpoints)
		}
		return rollback, nil
	})
	if transactionErr != nil {
		if errors.Is(transactionErr, errRecoveryConversationChanged) {
			return chat.SerializedPendingRecovery{}, transactionErr
		}
		if errors.Is(transactionErr, fileutil.ErrAtomicWriteCommitUncertain) {
			return chat.SerializedPendingRecovery{}, fmt.Errorf("%w: %v", errRecoveryCommitUncertain, transactionErr)
		}
		return chat.SerializedPendingRecovery{}, fmt.Errorf("%w: persist pending recovery: %v", errRecoveryPersistenceFailed, transactionErr)
	}

	a.journalEvent("side_effect_recovery_persisted", map[string]any{
		"recovery_id": recovery.ID,
		"session_id":  sessionID,
		"kind":        kind,
		"attempt":     attempt,
		"checkpoints": len(recovery.Checkpoints),
	})
	return recovery, nil
}

func (a *App) recoveryBudgetSnapshot(message string) (rateLimitAttempts, autoResumeAttempts int) {
	key := rateLimitRetryKey(message)
	a.rateLimitRetryMu.Lock()
	rateLimitAttempts = a.rateLimitRetryCount[key]
	a.rateLimitRetryMu.Unlock()
	a.autoResumeMu.Lock()
	autoResumeAttempts = a.autoResumeCount[key]
	a.autoResumeMu.Unlock()
	return rateLimitAttempts, autoResumeAttempts
}

// claimPendingRecovery persists scheduled -> claimed before returning an
// executable payload. A crash after this boundary cannot automatically replay
// mutations a second time: claimed entries are advisory/manual-only on restore.
func (a *App) claimPendingRecovery(id, sessionID string, expectedEpoch uint64) (chat.SerializedPendingRecovery, []tools.ToolCheckpoint, error) {
	if a == nil || a.session == nil || a.sessionManager == nil {
		return chat.SerializedPendingRecovery{}, nil, errPendingRecoveryUnavailable
	}

	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	if a.recoveryEpoch.Load() != expectedEpoch || a.session.GetID() != sessionID {
		return chat.SerializedPendingRecovery{}, nil, errPendingRecoveryUnavailable
	}
	var claimed chat.SerializedPendingRecovery
	transactionErr := a.sessionManager.ApplyAndSave(func() (func(), error) {
		if a.recoveryEpoch.Load() != expectedEpoch || a.session.GetID() != sessionID {
			return nil, errPendingRecoveryUnavailable
		}
		var target *chat.SerializedPendingRecovery
		for _, pending := range a.session.GetPendingRecoveries() {
			if pending.ID != id && pending.State == chat.PendingRecoveryClaimed {
				return nil, fmt.Errorf(
					"%w: recovery %q remains claimed", errRecoveryBlockedByClaimed, pending.ID)
			}
			if pending.ID == id && pending.SessionID == sessionID && pending.State == chat.PendingRecoveryScheduled {
				copy := pending
				target = &copy
			}
		}
		if target == nil {
			return nil, errPendingRecoveryUnavailable
		}
		if err := validatePendingRecovery(*target); err != nil {
			return nil, err
		}
		var ok bool
		claimed, ok = a.session.TransitionPendingRecovery(
			id, sessionID, chat.PendingRecoveryScheduled, chat.PendingRecoveryClaimed)
		if !ok {
			return nil, errPendingRecoveryUnavailable
		}
		rollback := func() {
			_, _ = a.session.TransitionPendingRecovery(
				id, sessionID, chat.PendingRecoveryClaimed, chat.PendingRecoveryScheduled)
		}
		return rollback, nil
	})
	if transactionErr != nil {
		if errors.Is(transactionErr, fileutil.ErrAtomicWriteCommitUncertain) {
			a.cancelRecoveryTimersForSession(sessionID)
			return chat.SerializedPendingRecovery{}, nil, fmt.Errorf(
				"%w: persist recovery claim: %v", errRecoveryCommitUncertain, transactionErr)
		}
		return chat.SerializedPendingRecovery{}, nil, fmt.Errorf("persist recovery claim: %w", transactionErr)
	}
	// A claimed generation is the sole executable recovery for this session.
	// Pause every sibling timer until this claim either completes durably or is
	// replaced by a later scheduled attempt; otherwise sibling one-shots wake,
	// lose the global-claim race, and disappear forever.
	a.cancelRecoveryTimersForSession(sessionID)
	a.markRecoveryAwaitingDispatch(claimed.SessionID, claimed.ID)
	a.seedPendingRecoveryBudget(claimed)
	return claimed, deserializeToolCheckpoints(claimed.Checkpoints), nil
}

// releaseClaimedRecoveriesLocked returns claims that this process can prove did
// not start to scheduled state. The caller holds sessionLeaseMu, which keeps the
// active session identity stable across the mutation and mandatory save.
func (a *App) releaseClaimedRecoveriesLocked(keys []recoveryTimerKey) (bool, error) {
	if a == nil || a.session == nil || len(keys) == 0 {
		return false, nil
	}
	currentSessionID := a.session.GetID()
	seen := make(map[recoveryTimerKey]struct{}, len(keys))
	claimed := make([]recoveryTimerKey, 0, len(keys))
	stateByID := make(map[string]chat.SerializedPendingRecovery)
	for _, recovery := range a.session.GetPendingRecoveries() {
		stateByID[recovery.ID] = recovery
	}
	for _, key := range keys {
		if key.sessionID != currentSessionID || key.recoveryID == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		if recovery, ok := stateByID[key.recoveryID]; ok &&
			recovery.SessionID == key.sessionID && recovery.State == chat.PendingRecoveryClaimed {
			claimed = append(claimed, key)
		}
	}
	if len(claimed) == 0 {
		return false, nil
	}

	mutate := func() (func(), error) {
		transitioned := make([]recoveryTimerKey, 0, len(claimed))
		rollback := func() {
			for i := len(transitioned) - 1; i >= 0; i-- {
				key := transitioned[i]
				if _, ok := a.session.TransitionPendingRecovery(
					key.recoveryID, key.sessionID,
					chat.PendingRecoveryScheduled, chat.PendingRecoveryClaimed); !ok {
					logging.Error("failed to roll back unstarted recovery release",
						"recovery_id", key.recoveryID, "session_id", key.sessionID)
				}
			}
		}
		for _, key := range claimed {
			if _, ok := a.session.TransitionPendingRecovery(
				key.recoveryID, key.sessionID,
				chat.PendingRecoveryClaimed, chat.PendingRecoveryScheduled); !ok {
				return rollback, fmt.Errorf(
					"%w: claimed recovery %q changed before unstarted release",
					errPendingRecoveryUnavailable, key.recoveryID)
			}
			transitioned = append(transitioned, key)
		}
		return rollback, nil
	}

	if a.sessionManager == nil {
		rollback, err := mutate()
		if err != nil && rollback != nil {
			rollback()
		}
		return false, err
	}
	transactionErr := a.sessionManager.ApplyAndSave(mutate)
	if transactionErr == nil {
		return false, nil
	}
	if errors.Is(transactionErr, fileutil.ErrAtomicWriteCommitUncertain) {
		return true, transactionErr
	}
	return false, transactionErr
}

func (a *App) finalizeReleasedRecoveries(keys []recoveryTimerKey, reason string, rearm bool) {
	for _, key := range keys {
		a.markRecoveryDispatched(key.sessionID, key.recoveryID)
		a.cancelRecoveryTimer(key.sessionID, key.recoveryID)
		a.journalEvent("side_effect_recovery_released", map[string]any{
			"recovery_id": key.recoveryID,
			"session_id":  key.sessionID,
			"reason":      reason,
		})
	}
	if rearm {
		a.resumePersistedRecoveriesWithGrace(false, recoveryRedispatchGrace)
	}
}

func (a *App) releaseClaimedRecovery(id, sessionID string, expectedEpoch uint64, reason string) error {
	if a == nil || a.session == nil || id == "" || sessionID == "" {
		return nil
	}
	key := recoveryTimerKey{sessionID: sessionID, recoveryID: id}
	a.sessionLeaseMu.Lock()
	if a.session.GetID() != sessionID || a.recoveryEpoch.Load() != expectedEpoch {
		a.sessionLeaseMu.Unlock()
		return errPendingRecoveryUnavailable
	}
	commitUncertain, err := a.releaseClaimedRecoveriesLocked([]recoveryTimerKey{key})
	a.sessionLeaseMu.Unlock()
	if err != nil && !commitUncertain {
		return fmt.Errorf("release unstarted recovery: %w", err)
	}
	a.finalizeReleasedRecoveries([]recoveryTimerKey{key}, reason, true)
	if commitUncertain {
		return fmt.Errorf("%w: release unstarted recovery: %v", errRecoveryCommitUncertain, err)
	}
	return nil
}

// cancelPendingAtRecoveryBoundary makes Esc/Ctrl+C one atomic dispatch
// boundary. recoveryEpoch is advanced while sessionLeaseMu is held, so a claim
// cannot enqueue immediately after the drain with the old lineage. Claims in
// the FIFO or in this process's claim->dispatch handoff are proven-unstarted and
// can be released; an already-started claimed turn is absent from both sets and
// intentionally remains ambiguous.
func (a *App) cancelPendingAtRecoveryBoundary() (drainedCount, remaining, released int, releaseErr error) {
	if a == nil {
		return 0, 0, 0, nil
	}
	a.sessionLeaseMu.Lock()
	drainedCount, remaining, released, releasedKeys, releaseErr := a.cancelPendingAtRecoveryBoundaryLocked()
	a.sessionLeaseMu.Unlock()
	a.finalizePendingRecoveryCancelBoundary(releasedKeys)
	return drainedCount, remaining, released, releaseErr
}

// cancelPendingAtRecoveryBoundaryLocked performs the durable half of the
// cancellation boundary. The caller holds sessionLeaseMu. Keeping this split
// lets cancelProcessing cancel the foreground owner and invalidate/drain every
// pre-Esc recovery while one lease barrier excludes both timer dispatch and a
// completing turn's FIFO handoff.
func (a *App) cancelPendingAtRecoveryBoundaryLocked() (drainedCount, remaining, released int, releasedKeys []recoveryTimerKey, releaseErr error) {
	drained := a.drainPendingRequests()
	drainedCount = len(drained)
	a.recoveryEpoch.Add(1)
	if a.session == nil {
		remaining = a.pendingCount()
		return drainedCount, remaining, 0, nil, nil
	}

	sessionID := a.session.GetID()
	keySet := make(map[recoveryTimerKey]struct{})
	for _, request := range drained {
		if request.recoveryID == "" {
			continue
		}
		key := recoveryTimerKey{
			sessionID: request.recoverySessionID, recoveryID: request.recoveryID,
		}
		if request.recoverySessionID != sessionID {
			// The active SessionManager cannot durably release a different
			// conversation's claimed marker. Drop only this executable FIFO owner;
			// the target session remains claimed/manual-only for /recovery review.
			continue
		}
		keySet[key] = struct{}{}
	}
	for _, key := range a.awaitingRecoveryClaimsForSession(sessionID) {
		keySet[key] = struct{}{}
	}

	keys := make([]recoveryTimerKey, 0, len(keySet))
	for key := range keySet {
		keys = append(keys, key)
	}
	commitUncertain, err := a.releaseClaimedRecoveriesLocked(keys)
	definiteFailure := err != nil && !commitUncertain
	// A definite save failure rolls the durable state back to claimed. Never put
	// that request back into the ordinary FIFO: the cancelled turn's finalizer
	// would otherwise auto-run it immediately after Esc. Claimed is the safe,
	// manual-only state until storage is healthy and /recovery is reviewed.
	// Every pre-boundary timer captured the old epoch. Cancel the bounded
	// registry before publishing the paused durable state for the new epoch.
	a.cancelRecoveryTimersForSession(sessionID)
	remaining = a.pendingCount()

	if !definiteFailure && len(keys) > 0 {
		releasedKeys = keys
		released = len(keys)
	}
	if definiteFailure {
		return drainedCount, remaining, 0, nil, fmt.Errorf("release cancelled recoveries: %w", err)
	}
	if commitUncertain {
		return drainedCount, remaining, released, releasedKeys, fmt.Errorf(
			"%w: release cancelled recoveries: %v", errRecoveryCommitUncertain, err)
	}
	return drainedCount, remaining, released, releasedKeys, nil
}

func (a *App) finalizePendingRecoveryCancelBoundary(releasedKeys []recoveryTimerKey) {
	if a == nil {
		return
	}
	if len(releasedKeys) > 0 {
		a.finalizeReleasedRecoveries(releasedKeys, "cancelled_before_dispatch", false)
	}
	// Explicit cancellation pauses automation for the rest of this process.
	// Scheduled state remains durable for restart or an exact explicit repeat;
	// re-arming here would let an unrelated post-Esc task reopen the gate and
	// accidentally run the old mutation beside it.
}

// releaseProvenUnstartedRecoveriesBeforeSwitchLocked closes the only window in
// which a claimed generation belongs to the current session but has not started:
// it is either in the FIFO or was claimed by this process and is waiting to enter
// that FIFO. Persisted claims absent from both sets remain ambiguous and untouched.
// The caller holds sessionLeaseMu.
func (a *App) releaseProvenUnstartedRecoveriesBeforeSwitchLocked(sessionID string) (bool, error) {
	if a == nil || a.session == nil || a.session.GetID() != sessionID {
		return false, nil
	}
	keySet := make(map[recoveryTimerKey]struct{})
	for _, request := range a.recoveryPendingForSession(sessionID) {
		if request.recoveryID != "" {
			keySet[recoveryTimerKey{
				sessionID: request.recoverySessionID, recoveryID: request.recoveryID,
			}] = struct{}{}
		}
	}
	for _, key := range a.awaitingRecoveryClaimsForSession(sessionID) {
		keySet[key] = struct{}{}
	}
	if len(keySet) == 0 {
		return false, nil
	}
	keys := make([]recoveryTimerKey, 0, len(keySet))
	for key := range keySet {
		keys = append(keys, key)
	}
	commitUncertain, err := a.releaseClaimedRecoveriesLocked(keys)
	if err != nil && !commitUncertain {
		return false, fmt.Errorf("release queued recovery before session switch: %w", err)
	}
	a.dropRecoveryPendingKeys(keySet)
	a.finalizeReleasedRecoveries(keys, "session_switch_before_dispatch", false)
	if commitUncertain {
		logging.Warn("pre-switch recovery release committed with uncertain durability",
			"session_id", sessionID, "error", err)
	}
	return true, nil
}

func (a *App) seedPendingRecoveryBudget(recovery chat.SerializedPendingRecovery) {
	key := rateLimitRetryKey(recovery.Message)
	rateLimitAttempts := recovery.RateLimitAttempts
	autoResumeAttempts := recovery.AutoResumeAttempts
	if recovery.Kind == "rate_limit" && rateLimitAttempts < recovery.Attempt {
		rateLimitAttempts = recovery.Attempt
	}
	if recovery.Kind == "auto_resume" && autoResumeAttempts < recovery.Attempt {
		autoResumeAttempts = recovery.Attempt
	}
	if rateLimitAttempts > 0 {
		a.rateLimitRetryMu.Lock()
		if a.rateLimitRetryCount == nil {
			a.rateLimitRetryCount = make(map[string]int)
		}
		if a.rateLimitRetryCount[key] < rateLimitAttempts {
			a.rateLimitRetryCount[key] = rateLimitAttempts
		}
		a.rateLimitRetryMu.Unlock()
	}
	if autoResumeAttempts > 0 {
		a.autoResumeMu.Lock()
		if a.autoResumeCount == nil {
			a.autoResumeCount = make(map[string]int)
		}
		if a.autoResumeCount[key] < autoResumeAttempts {
			a.autoResumeCount[key] = autoResumeAttempts
		}
		a.autoResumeMu.Unlock()
	}
}

func validatePendingRecovery(recovery chat.SerializedPendingRecovery) error {
	switch {
	case strings.TrimSpace(recovery.ID) == "":
		return fmt.Errorf("persisted recovery has no id")
	case strings.TrimSpace(recovery.SessionID) == "":
		return fmt.Errorf("persisted recovery %q has no session id", recovery.ID)
	case strings.TrimSpace(recovery.Message) == "":
		return fmt.Errorf("persisted recovery %q has an empty request", recovery.ID)
	case recovery.State != chat.PendingRecoveryScheduled && recovery.State != chat.PendingRecoveryClaimed:
		return fmt.Errorf("persisted recovery %q has invalid state %q", recovery.ID, recovery.State)
	}
	expectedGenerationSignature := chat.PendingRecoveryGenerationSignature(recovery)
	if expectedGenerationSignature == "" || recovery.GenerationSignature != expectedGenerationSignature {
		return fmt.Errorf("persisted recovery %q has a mismatched generation signature", recovery.ID)
	}
	if recovery.RateLimitAttempts < 0 || recovery.RateLimitAttempts > maxAutoRateLimitRetries {
		return fmt.Errorf("persisted recovery %q has invalid rate-limit budget %d", recovery.ID, recovery.RateLimitAttempts)
	}
	if recovery.AutoResumeAttempts < 0 || recovery.AutoResumeAttempts > maxAutoResumeAttempts {
		return fmt.Errorf("persisted recovery %q has invalid auto-resume budget %d", recovery.ID, recovery.AutoResumeAttempts)
	}
	switch recovery.Kind {
	case "rate_limit":
		if recovery.Attempt < 1 || recovery.Attempt > maxAutoRateLimitRetries {
			return fmt.Errorf("persisted recovery %q has invalid rate-limit attempt %d", recovery.ID, recovery.Attempt)
		}
		if recovery.RateLimitAttempts != 0 && recovery.RateLimitAttempts < recovery.Attempt {
			return fmt.Errorf("persisted recovery %q rate-limit budget %d is below attempt %d", recovery.ID, recovery.RateLimitAttempts, recovery.Attempt)
		}
	case "auto_resume":
		if recovery.Attempt < 1 || recovery.Attempt > maxAutoResumeAttempts {
			return fmt.Errorf("persisted recovery %q has invalid auto-resume attempt %d", recovery.ID, recovery.Attempt)
		}
		if recovery.AutoResumeAttempts != 0 && recovery.AutoResumeAttempts < recovery.Attempt {
			return fmt.Errorf("persisted recovery %q auto-resume budget %d is below attempt %d", recovery.ID, recovery.AutoResumeAttempts, recovery.Attempt)
		}
	default:
		return fmt.Errorf("persisted recovery %q has invalid kind %q", recovery.ID, recovery.Kind)
	}
	seenCallIDs := make(map[string]struct{}, len(recovery.Checkpoints))
	for i, checkpoint := range recovery.Checkpoints {
		if strings.TrimSpace(checkpoint.ToolName) == "" {
			return fmt.Errorf("persisted recovery %q checkpoint %d has no tool name", recovery.ID, i)
		}
		if !tools.IsWriteTool(checkpoint.ToolName) {
			return fmt.Errorf("persisted recovery %q checkpoint %d is not a side-effecting tool", recovery.ID, i)
		}
		expectedSignature := tools.ToolCheckpointSignature(checkpoint.ToolName, checkpoint.Args)
		if expectedSignature == "" || checkpoint.Signature != expectedSignature {
			return fmt.Errorf("persisted recovery %q checkpoint %d has a mismatched signature", recovery.ID, i)
		}
		// Duplicate signatures are intentional and order-sensitive: a turn can
		// run the same stateful call more than once and receive a different result
		// each time. The generation signature binds their full ordered sequence,
		// while the replay journal consumes that sequence as a one-shot queue.
		if checkpoint.CallID != "" {
			if _, duplicate := seenCallIDs[checkpoint.CallID]; duplicate {
				return fmt.Errorf("persisted recovery %q contains duplicate checkpoint call id %q", recovery.ID, checkpoint.CallID)
			}
			seenCallIDs[checkpoint.CallID] = struct{}{}
		}
		if checkpoint.ResultV2 == nil {
			return fmt.Errorf("persisted recovery %q checkpoint %d has no ResultV2 outcome", recovery.ID, i)
		}
		expectedOutcomeSignature := serializedToolOutcomeSignature(checkpoint)
		if expectedOutcomeSignature == "" || checkpoint.OutcomeSignature != expectedOutcomeSignature {
			return fmt.Errorf("persisted recovery %q checkpoint %d has a mismatched outcome signature", recovery.ID, i)
		}
		if checkpoint.ResultV2.DataPresent && len(checkpoint.ResultV2.Data) == 0 {
			return fmt.Errorf("persisted recovery %q checkpoint %d marks missing ResultV2 data present", recovery.ID, i)
		}
		if len(checkpoint.ResultV2.Data) > 0 {
			if _, err := decodeCheckpointResultData(checkpoint.ResultV2.Data); err != nil {
				return fmt.Errorf("persisted recovery %q checkpoint %d has malformed ResultV2 data: %w", recovery.ID, i, err)
			}
		}
		if block := checkpoint.ResultV2.PolicyBlock; block != nil {
			switch tools.PolicyBlockKind(block.Kind) {
			case tools.PolicyBlockPermission, tools.PolicyBlockSafety, tools.PolicyBlockHook, tools.PolicyBlockPlan:
			default:
				return fmt.Errorf("persisted recovery %q checkpoint %d has invalid policy block kind %q", recovery.ID, i, block.Kind)
			}
			if strings.TrimSpace(block.Reason) == "" {
				return fmt.Errorf("persisted recovery %q checkpoint %d has an empty policy block reason", recovery.ID, i)
			}
		}
	}
	return nil
}

func validatePendingRecoveryBatch(sessionID string, recoveries []chat.SerializedPendingRecovery) ([]chat.SerializedPendingRecovery, error) {
	if len(recoveries) > maxPendingRecoveries {
		return nil, fmt.Errorf("session %q contains %d pending recoveries (maximum %d)", sessionID, len(recoveries), maxPendingRecoveries)
	}
	seenIDs := make(map[string]struct{}, len(recoveries))
	seenPromptAliases := make(map[string]string, len(recoveries)*2)
	active := make([]chat.SerializedPendingRecovery, 0, len(recoveries))
	for _, recovery := range recoveries {
		if recovery.SessionID != sessionID {
			return nil, fmt.Errorf("pending recovery %q belongs to session %q, not active session %q", recovery.ID, recovery.SessionID, sessionID)
		}
		if err := validatePendingRecovery(recovery); err != nil {
			return nil, err
		}
		if _, duplicate := seenIDs[recovery.ID]; duplicate {
			return nil, fmt.Errorf("session %q contains duplicate pending recovery id %q", sessionID, recovery.ID)
		}
		seenIDs[recovery.ID] = struct{}{}
		aliases := map[string]struct{}{
			strings.TrimSpace(recovery.Message): {},
		}
		if userMessage := strings.TrimSpace(recovery.UserMessage); userMessage != "" {
			aliases[userMessage] = struct{}{}
		}
		for alias := range aliases {
			if otherID, duplicate := seenPromptAliases[alias]; duplicate {
				return nil, fmt.Errorf("pending recoveries %q and %q share the same request identity", otherID, recovery.ID)
			}
			seenPromptAliases[alias] = recovery.ID
		}
		active = append(active, recovery)
	}
	return active, nil
}

func (a *App) schedulePersistedRecovery(recovery chat.SerializedPendingRecovery, startup bool) {
	a.schedulePersistedRecoveryWithGrace(recovery, startup, 0)
}

func (a *App) schedulePersistedRecoveryWithGrace(recovery chat.SerializedPendingRecovery, startup bool, minimumDelay time.Duration) {
	if err := validatePendingRecovery(recovery); err != nil {
		logging.Warn("ignored malformed pending recovery", "error", err)
		return
	}
	if recovery.State != chat.PendingRecoveryScheduled {
		return
	}
	delay := time.Until(recovery.NotBefore)
	if delay < 0 {
		delay = 0
	}
	if startup && delay < recoveryStartupGrace {
		delay = recoveryStartupGrace
	}
	if delay < minimumDelay {
		delay = minimumDelay
	}
	scheduledEpoch := a.recoveryEpoch.Load()
	key := recoveryTimerKey{sessionID: recovery.SessionID, recoveryID: recovery.ID}
	registration, reserved := a.reserveRecoveryTimer(key)
	if !reserved {
		return
	}

	a.safeGo("persisted-side-effect-recovery", func() {
		defer a.releaseRecoveryTimer(key, registration)
		wait := delay
		for {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-registration.ctx.Done():
				timer.Stop()
				return
			}
			// Do not claim a mutation when a busy foreground cannot accept its
			// queued dispatch. Waiting in the bounded timer registry avoids a
			// claimed->scheduled disk-write loop while the FIFO remains full.
			if a.recoveryQueueIsFullWhileBusy() {
				wait = recoveryQueuePollInterval
				continue
			}
			// The reservation protects only a sleeping timer. Release it before
			// claim so a terminal clear racing this wake-up can re-arm the still-
			// scheduled generation after the global claimed guard is removed.
			a.releaseRecoveryTimer(key, registration)
			break
		}

		claimed, checkpoints, err := a.claimPendingRecovery(
			recovery.ID, recovery.SessionID, scheduledEpoch)
		if err != nil {
			if !errors.Is(err, errPendingRecoveryUnavailable) {
				logging.Warn("safe recovery was not dispatched", "recovery_id", recovery.ID, "error", err)
				a.safeSendToProgram(ui.StatusUpdateMsg{
					Type:    ui.StatusWarning,
					Message: "Safe retry could not be persisted and was not run — use /recovery to inspect it",
				})
			}
			return
		}
		a.journalEvent("side_effect_recovery_claimed", map[string]any{
			"recovery_id": claimed.ID,
			"session_id":  claimed.SessionID,
			"kind":        claimed.Kind,
			"attempt":     claimed.Attempt,
		})
		a.handleRecoveryResubmit(
			claimed.Message, claimed.UserMessage, claimed.ID, claimed.SessionID, scheduledEpoch, checkpoints)
	})
}

func (a *App) recoveryQueueIsFullWhileBusy() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.processing {
		return false
	}
	a.pendingMu.Lock()
	full := len(a.pendingQueue) >= maxPendingQueue
	a.pendingMu.Unlock()
	return full
}

func (a *App) reserveRecoveryTimer(key recoveryTimerKey) (*recoveryTimerRegistration, bool) {
	if a == nil || key.sessionID == "" || key.recoveryID == "" {
		return nil, false
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	registration := &recoveryTimerRegistration{ctx: ctx, cancel: cancel}

	a.recoveryTimerMu.Lock()
	if a.recoveryTimers == nil {
		a.recoveryTimers = make(map[recoveryTimerKey]*recoveryTimerRegistration)
	}
	if _, exists := a.recoveryTimers[key]; exists {
		a.recoveryTimerMu.Unlock()
		cancel()
		return nil, false
	}
	a.recoveryTimers[key] = registration
	a.recoveryTimerMu.Unlock()
	return registration, true
}

func (a *App) releaseRecoveryTimer(key recoveryTimerKey, registration *recoveryTimerRegistration) {
	if a == nil || registration == nil {
		return
	}
	a.recoveryTimerMu.Lock()
	if a.recoveryTimers[key] == registration {
		delete(a.recoveryTimers, key)
	}
	a.recoveryTimerMu.Unlock()
	// Release the child context even on the normal timer-fired path. Contexts
	// retain their parent linkage until cancellation; deletion from the registry
	// alone is not sufficient lifecycle cleanup.
	registration.cancel()
}

func (a *App) cancelRecoveryTimer(sessionID, recoveryID string) {
	if a == nil || sessionID == "" || recoveryID == "" {
		return
	}
	key := recoveryTimerKey{sessionID: sessionID, recoveryID: recoveryID}
	a.recoveryTimerMu.Lock()
	registration := a.recoveryTimers[key]
	if registration != nil {
		delete(a.recoveryTimers, key)
	}
	a.recoveryTimerMu.Unlock()
	if registration != nil {
		registration.cancel()
	}
}

func (a *App) cancelRecoveryTimersForSession(sessionID string) {
	if a == nil || sessionID == "" {
		return
	}
	var cancelled []*recoveryTimerRegistration
	a.recoveryTimerMu.Lock()
	for key, registration := range a.recoveryTimers {
		if key.sessionID != sessionID {
			continue
		}
		delete(a.recoveryTimers, key)
		cancelled = append(cancelled, registration)
	}
	for key := range a.recoveryAwaitingDispatch {
		if key.sessionID == sessionID {
			delete(a.recoveryAwaitingDispatch, key)
		}
	}
	a.recoveryTimerMu.Unlock()
	for _, registration := range cancelled {
		registration.cancel()
	}
}

func (a *App) cancelRecoveryTimersExceptSession(sessionID string) {
	if a == nil {
		return
	}
	var cancelled []*recoveryTimerRegistration
	a.recoveryTimerMu.Lock()
	for key, registration := range a.recoveryTimers {
		if key.sessionID == sessionID {
			continue
		}
		delete(a.recoveryTimers, key)
		cancelled = append(cancelled, registration)
	}
	a.recoveryTimerMu.Unlock()
	for _, registration := range cancelled {
		registration.cancel()
	}
}

func (a *App) markRecoveryAwaitingDispatch(sessionID, recoveryID string) {
	if a == nil || sessionID == "" || recoveryID == "" {
		return
	}
	key := recoveryTimerKey{sessionID: sessionID, recoveryID: recoveryID}
	a.recoveryTimerMu.Lock()
	if a.recoveryAwaitingDispatch == nil {
		a.recoveryAwaitingDispatch = make(map[recoveryTimerKey]struct{})
	}
	a.recoveryAwaitingDispatch[key] = struct{}{}
	a.recoveryTimerMu.Unlock()
}

func (a *App) markRecoveryDispatched(sessionID, recoveryID string) {
	if a == nil || sessionID == "" || recoveryID == "" {
		return
	}
	a.recoveryTimerMu.Lock()
	delete(a.recoveryAwaitingDispatch, recoveryTimerKey{
		sessionID: sessionID, recoveryID: recoveryID,
	})
	a.recoveryTimerMu.Unlock()
}

func (a *App) awaitingRecoveryClaimsForSession(sessionID string) []recoveryTimerKey {
	if a == nil || sessionID == "" {
		return nil
	}
	a.recoveryTimerMu.Lock()
	defer a.recoveryTimerMu.Unlock()
	claims := make([]recoveryTimerKey, 0, len(a.recoveryAwaitingDispatch))
	for key := range a.recoveryAwaitingDispatch {
		if key.sessionID == sessionID {
			claims = append(claims, key)
		}
	}
	return claims
}

// resumePersistedRecoveries is called only after the restored session is the
// active session. Scheduled entries resume automatically; claimed means the
// prior process may have stopped during execution, so it is never auto-run.
func (a *App) resumePersistedRecoveries(startup bool) {
	a.resumePersistedRecoveriesWithGrace(startup, 0)
}

func (a *App) resumePersistedRecoveriesWithGrace(startup bool, minimumDelay time.Duration) {
	if a == nil || a.session == nil {
		return
	}
	sessionID := a.session.GetID()
	// Only the active session owns timer goroutines. Repeated A->B->A switches
	// therefore cannot accumulate sleepers for every session ever visited.
	a.cancelRecoveryTimersExceptSession(sessionID)
	recoveries, err := validatePendingRecoveryBatch(sessionID, a.session.GetPendingRecoveries())
	if err != nil {
		a.cancelRecoveryTimersForSession(sessionID)
		logging.Warn("pending recoveries blocked as a malformed batch", "error", err)
		a.safeSendToProgramAsync(ui.StatusUpdateMsg{
			Type:    ui.StatusWarning,
			Message: "Saved recovery state is inconsistent and was not run — inspect /recovery or use /clear",
		})
		return
	}
	for _, recovery := range recoveries {
		if recovery.State == chat.PendingRecoveryClaimed {
			a.cancelRecoveryTimersForSession(sessionID)
			a.safeSendToProgramAsync(ui.StatusUpdateMsg{
				Type:    ui.StatusWarning,
				Message: "An interrupted safe retry may have partially run; all automatic retries are paused until /recovery is reviewed or /clear is used",
			})
			return
		}
	}
	for _, recovery := range recoveries {
		switch recovery.State {
		case chat.PendingRecoveryScheduled:
			a.schedulePersistedRecoveryWithGrace(recovery, startup, minimumDelay)
		}
	}
}

func (a *App) clearPendingRecovery(id, sessionID, reason string) error {
	if a == nil || a.session == nil || id == "" {
		return nil
	}
	a.sessionLeaseMu.Lock()
	if a.session.GetID() != sessionID {
		a.sessionLeaseMu.Unlock()
		return nil
	}
	var cleared chat.SerializedPendingRecovery
	for _, recovery := range a.session.GetPendingRecoveries() {
		if recovery.ID == id && recovery.SessionID == sessionID {
			cleared = recovery
			break
		}
	}
	if cleared.ID == "" {
		a.sessionLeaseMu.Unlock()
		return nil
	}

	var transactionErr error
	if a.sessionManager == nil {
		if !a.session.RemovePendingRecovery(id) {
			a.sessionLeaseMu.Unlock()
			return nil
		}
	} else {
		transactionErr = a.sessionManager.ApplyAndSave(func() (func(), error) {
			if a.session.GetID() != sessionID || !a.session.RemovePendingRecovery(id) {
				return nil, errPendingRecoveryUnavailable
			}
			rollback := func() {
				if !a.session.AddPendingRecovery(cleared, "") {
					logging.Error("failed to roll back pending-recovery clear",
						"recovery_id", id, "session_id", sessionID)
				}
			}
			return rollback, nil
		})
	}
	a.sessionLeaseMu.Unlock()

	commitUncertain := errors.Is(transactionErr, fileutil.ErrAtomicWriteCommitUncertain)
	if transactionErr != nil && !commitUncertain {
		logging.Warn("failed to persist pending-recovery clear; claimed marker retained",
			"recovery_id", id, "session_id", sessionID, "error", transactionErr)
		return fmt.Errorf("persist pending-recovery clear: %w", transactionErr)
	}

	a.cancelRecoveryTimer(sessionID, id)
	a.markRecoveryDispatched(sessionID, id)
	a.journalEvent("side_effect_recovery_cleared", map[string]any{
		"recovery_id": id,
		"session_id":  sessionID,
		"reason":      reason,
	})
	// Clearing the sole claimed generation re-opens the global execution slot.
	// Rebuild timers for every remaining scheduled generation; the registry makes
	// this idempotent when an older timer is still sleeping.
	a.resumePersistedRecoveries(false)
	if commitUncertain {
		logging.Warn("pending-recovery clear committed with uncertain directory durability",
			"recovery_id", id, "session_id", sessionID, "error", transactionErr)
		return fmt.Errorf("%w: clear pending recovery: %v", errRecoveryCommitUncertain, transactionErr)
	}
	return nil
}

// pendingRecoveryForHeadlessPrompt makes non-interactive resume deterministic:
// the exact interrupted request may continue with its lineage, while a new
// prompt is never silently substituted and a previously-claimed request is
// never replayed after an ambiguous crash boundary.
func (a *App) pendingRecoveryForHeadlessPrompt(prompt string) (*chat.SerializedPendingRecovery, error) {
	recovery, _, err := a.pendingRecoveryForHeadlessPromptAtLineage(prompt)
	return recovery, err
}

func (a *App) pendingRecoveryForHeadlessPromptAtLineage(prompt string) (*chat.SerializedPendingRecovery, conversationLineage, error) {
	if a == nil || a.session == nil {
		return nil, conversationLineage{}, nil
	}
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	lineage := conversationLineage{
		sessionID: a.session.GetID(),
		epoch:     a.recoveryEpoch.Load(),
	}
	recovery, err := a.pendingRecoveryForHeadlessPromptUnlocked(prompt, lineage.sessionID)
	return recovery, lineage, err
}

func (a *App) pendingRecoveryForHeadlessPromptUnlocked(prompt, sessionID string) (*chat.SerializedPendingRecovery, error) {
	prompt = strings.TrimSpace(prompt)
	recoveries, err := validatePendingRecoveryBatch(sessionID, a.session.GetPendingRecoveries())
	if err != nil {
		return nil, err
	}
	var match *chat.SerializedPendingRecovery
	for _, recovery := range recoveries {
		if recovery.State == chat.PendingRecoveryClaimed {
			return nil, fmt.Errorf("session %q contains claimed recovery %q that may have partially executed; open it interactively and inspect /recovery before running another headless prompt", sessionID, recovery.ID)
		}
		userMessage := strings.TrimSpace(recovery.UserMessage)
		matches := prompt == strings.TrimSpace(recovery.Message) ||
			(userMessage != "" && prompt == userMessage)
		if !matches {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("headless prompt matches more than one pending recovery in session %q", sessionID)
		}
		copy := recovery
		match = &copy
	}
	if match != nil {
		return match, nil
	}
	for _, recovery := range recoveries {
		if recovery.State == chat.PendingRecoveryScheduled {
			return nil, fmt.Errorf("session %q has a scheduled recovery for a different request; resume it interactively or start a fresh session instead of applying its checkpoints to this prompt", sessionID)
		}
	}
	return nil, nil
}

// tryResumeScheduledRecovery turns an explicit repetition of the same
// interactive request into the durable recovery itself. Without this guard a
// user retrying before the automatic timer fires would run once with a fresh
// ledger and then again when the timer wakes.
func (a *App) tryResumeScheduledRecovery(prompt string) bool {
	if a == nil || a.session == nil {
		return false
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return false
	}
	// Slash commands are the escape hatch for malformed/claimed state
	// (/recovery and /clear) and must never be swallowed by retry matching.
	if strings.HasPrefix(prompt, "/") {
		return false
	}
	sessionID := a.session.GetID()
	recoveries, err := validatePendingRecoveryBatch(sessionID, a.session.GetPendingRecoveries())
	if err != nil {
		logging.Warn("interactive prompt blocked by malformed recovery state", "error", err)
		a.safeSendToProgramAsync(ui.StatusUpdateMsg{
			Type:    ui.StatusWarning,
			Message: "Saved recovery state is inconsistent; inspect /recovery or use /clear before sending another task",
		})
		return true
	}
	var match *chat.SerializedPendingRecovery
	for _, recovery := range recoveries {
		userMessage := strings.TrimSpace(recovery.UserMessage)
		if prompt != strings.TrimSpace(recovery.Message) && (userMessage == "" || prompt != userMessage) {
			continue
		}
		if recovery.State == chat.PendingRecoveryClaimed {
			a.safeSendToProgramAsync(ui.StatusUpdateMsg{
				Type:    ui.StatusWarning,
				Message: "This safe retry is already claimed or may have partially run; inspect /recovery instead of replaying it",
			})
			return true
		}
		if recovery.State != chat.PendingRecoveryScheduled {
			continue
		}
		if match != nil {
			a.safeSendToProgramAsync(ui.StatusUpdateMsg{
				Type:    ui.StatusWarning,
				Message: "This request matches multiple pending recoveries; inspect /recovery before choosing one",
			})
			return true
		}
		copy := recovery
		match = &copy
	}
	if match == nil {
		return false
	}
	claimEpoch := a.recoveryEpoch.Load()
	claimed, checkpoints, err := a.claimPendingRecovery(
		match.ID, match.SessionID, claimEpoch)
	if err != nil {
		message := "Safe retry state changed before it could be claimed; wait for the scheduled retry or inspect /recovery"
		if !errors.Is(err, errPendingRecoveryUnavailable) {
			message = "Safe retry could not persist its claim and was not run; inspect /recovery"
			logging.Warn("explicit safe retry claim failed", "recovery_id", match.ID, "error", err)
		}
		a.safeSendToProgramAsync(ui.StatusUpdateMsg{Type: ui.StatusWarning, Message: message})
		return true
	}
	a.safeSendToProgramAsync(ui.StatusUpdateMsg{
		Type:    ui.StatusRetry,
		Message: "Resuming the interrupted request with its saved side-effect ledger",
	})
	a.handleRecoveryResubmit(
		claimed.Message, claimed.UserMessage, claimed.ID, claimed.SessionID,
		claimEpoch, checkpoints)
	return true
}

// claimQueuedRecoveryForPrompt closes the gap between initial interactive
// typing and FIFO dispatch. A duplicate prompt may have been queued before the
// failed foreground turn persisted its recovery; it must be promoted to that
// exact checkpoint generation (or blocked if another dispatcher claimed it),
// never launched as a fresh-ledger turn.
func (a *App) claimQueuedRecoveryForPrompt(prompt string, expectedEpoch uint64) (chat.SerializedPendingRecovery, []tools.ToolCheckpoint, bool, error) {
	if a == nil || a.session == nil {
		return chat.SerializedPendingRecovery{}, nil, false, nil
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" || strings.HasPrefix(prompt, "/") {
		return chat.SerializedPendingRecovery{}, nil, false, nil
	}
	sessionID := a.session.GetID()
	recoveries, err := validatePendingRecoveryBatch(sessionID, a.session.GetPendingRecoveries())
	if err != nil {
		return chat.SerializedPendingRecovery{}, nil, true, err
	}
	var match *chat.SerializedPendingRecovery
	for _, recovery := range recoveries {
		userMessage := strings.TrimSpace(recovery.UserMessage)
		if prompt != strings.TrimSpace(recovery.Message) && (userMessage == "" || prompt != userMessage) {
			continue
		}
		if recovery.State == chat.PendingRecoveryClaimed {
			return chat.SerializedPendingRecovery{}, nil, true, fmt.Errorf(
				"%w: matching recovery %q is already claimed", errRecoveryBlockedByClaimed, recovery.ID)
		}
		if recovery.State != chat.PendingRecoveryScheduled {
			continue
		}
		if match != nil {
			return chat.SerializedPendingRecovery{}, nil, true, fmt.Errorf(
				"queued prompt matches multiple pending recoveries")
		}
		copy := recovery
		match = &copy
	}
	if match == nil {
		return chat.SerializedPendingRecovery{}, nil, false, nil
	}
	claimed, checkpoints, err := a.claimPendingRecovery(
		match.ID, match.SessionID, expectedEpoch)
	if err == nil {
		// The FIFO head already owns the foreground slot; from this point a
		// cancellation is execution-ambiguous rather than proven-unstarted.
		a.markRecoveryDispatched(claimed.SessionID, claimed.ID)
	}
	return claimed, checkpoints, true, err
}
