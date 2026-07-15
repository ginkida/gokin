package chat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gokin/internal/logging"
)

// SessionManager handles automatic session persistence.
type SessionManager struct {
	session        *Session
	historyManager *HistoryManager
	config         SessionManagerConfig
	mu             sync.Mutex
	lastSaveTime   time.Time
	saveTimer      *time.Timer
	stopChan       chan struct{}
	stopOnce       sync.Once
	asyncSaveCh    chan struct{} // Buffered channel for async save dedup

	// consecutiveSaveFailures tracks the current run of failed Save() calls,
	// reset to 0 on the first success. onSaveFailed fires ONCE per failing
	// streak (on the healthy->failing transition) so a persistent disk-full /
	// permission problem surfaces to the user without spamming a toast on every
	// message. Both guarded by mu; the callback is invoked OUTSIDE the lock.
	consecutiveSaveFailures int
	onSaveFailed            func(err error)
}

// SetOnSaveFailed registers a callback fired when background session
// persistence STARTS failing (the healthy->failing transition of a failure
// streak). Surfacing this matters because autosave failures were previously
// Warn-log only — a user whose disk filled up or whose session dir became
// unwritable kept working and only discovered their history hadn't persisted
// after restart. The callback fires at most once per streak (reset on the
// next successful save) to avoid per-message toast spam.
func (sm *SessionManager) SetOnSaveFailed(fn func(err error)) {
	sm.mu.Lock()
	sm.onSaveFailed = fn
	sm.mu.Unlock()
}

// SessionManagerConfig configures session persistence behavior.
type SessionManagerConfig struct {
	Enabled         bool          // Enable auto-save/load
	SaveInterval    time.Duration // Periodic save interval (default 2m)
	AutoLoad        bool          // Auto-load last session on startup
	MaxSessionAge   time.Duration // Maximum age for sessions before cleanup (default: 30 days)
	MaxSessionCount int           // Maximum number of sessions to keep (default: 50)
}

// DefaultSessionManagerConfig returns default configuration.
func DefaultSessionManagerConfig() SessionManagerConfig {
	return SessionManagerConfig{
		Enabled:         true,
		SaveInterval:    2 * time.Minute,
		AutoLoad:        true,
		MaxSessionAge:   30 * 24 * time.Hour, // 30 days
		MaxSessionCount: 50,                  // Keep max 50 sessions
	}
}

// NewSessionManager creates a new session manager.
func NewSessionManager(session *Session, config SessionManagerConfig) (*SessionManager, error) {
	historyMgr, err := NewHistoryManager()
	if err != nil {
		return nil, fmt.Errorf("failed to create history manager: %w", err)
	}

	sm := &SessionManager{
		session:        session,
		historyManager: historyMgr,
		config:         config,
		stopChan:       make(chan struct{}),
	}

	// A non-positive SaveInterval (e.g. an explicit `session.save_interval: 0`
	// that survives config Load, or a previously-saved 0) would make the
	// periodic-save time.AfterFunc/Reset fire immediately — a tight busy-loop
	// that pegs a goroutine and thrashes the session file on disk. Clamp to the
	// default; both Start (AfterFunc) and periodicSave (Reset) read this field.
	if sm.config.SaveInterval <= 0 {
		sm.config.SaveInterval = 2 * time.Minute
	}
	defaults := DefaultSessionManagerConfig()
	if sm.config.MaxSessionAge <= 0 {
		sm.config.MaxSessionAge = defaults.MaxSessionAge
	}
	if sm.config.MaxSessionCount <= 0 {
		sm.config.MaxSessionCount = defaults.MaxSessionCount
	}

	return sm, nil
}

// Start initializes the session manager.
func (sm *SessionManager) Start(ctx context.Context) {
	if !sm.config.Enabled {
		return
	}

	sm.mu.Lock()
	sm.lastSaveTime = time.Now()
	saveCh := make(chan struct{}, 1)
	sm.asyncSaveCh = saveCh
	// Start periodic save timer
	sm.saveTimer = time.AfterFunc(sm.config.SaveInterval, func() {
		sm.periodicSave()
	})
	sm.mu.Unlock()

	// Background async saver: deduplicates rapid save requests.
	// Multiple SaveAfterMessage() calls within a short window produce one Save().
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Warn("async save goroutine recovered from panic", "error", r)
			}
		}()
		for {
			select {
			case <-saveCh:
				// Debounce: wait briefly to coalesce rapid saves. Use Timer+select
				// (not time.Sleep) so a shutdown during the debounce window cancels
				// immediately instead of waiting out the 100ms then doing one more
				// save after Stop() already finished its own final-save call.
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return
				case <-sm.stopChan:
					timer.Stop()
					return
				}
				// Drain any extra signals that arrived during the debounce window.
				select {
				case <-saveCh:
				default:
				}
				if err := sm.Save(); err != nil {
					// Same failure mode as periodicSave (sm.Save call) — log at the
					// same level so default-Warn users actually see "your session
					// state isn't persisting". Pre-fix this path was Debug, hidden
					// at the default log level.
					logging.Warn("async session save failed", "error", err)
				}
			case <-ctx.Done():
				return
			case <-sm.stopChan:
				return
			}
		}
	}()

	// Clean up old sessions in background (outside lock)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Warn("session cleanup recovered from panic", "error", r)
			}
		}()
		sm.CleanupOldSessions()
	}()
}

// Stop() tears down both the periodic timer and the async saver.

// Stop gracefully shuts down the session manager.
func (sm *SessionManager) Stop() {
	// Stop the save timer first
	sm.mu.Lock()
	if sm.saveTimer != nil {
		sm.saveTimer.Stop()
		sm.saveTimer = nil
	}
	sm.asyncSaveCh = nil
	sm.mu.Unlock()

	// Close stop channel to signal any listeners (Once guards against concurrent Stop calls)
	sm.stopOnce.Do(func() { close(sm.stopChan) })

	// Final save on shutdown
	if err := sm.Save(); err != nil {
		logging.Warn("failed to save session on shutdown", "error", err)
	}
}

// Save saves the current session state.
func (sm *SessionManager) Save() error {
	if !sm.config.Enabled {
		return nil
	}
	if sm.session == nil {
		return fmt.Errorf("cannot save without an active session")
	}

	sm.mu.Lock()
	err := sm.historyManager.SaveFull(sm.session)
	// Track the failure streak and decide whether to notify while still holding
	// the lock; the callback itself fires AFTER Unlock (callbacks-never-under-
	// lock discipline — the UI toast path can re-enter arbitrary code).
	var notify func(error)
	if err != nil {
		sm.consecutiveSaveFailures++
		if sm.consecutiveSaveFailures == 1 {
			notify = sm.onSaveFailed // fire once on the healthy->failing transition
		}
	} else {
		sm.consecutiveSaveFailures = 0
		sm.lastSaveTime = time.Now()
	}
	sessionID := sm.session.GetID()
	msgCount := sm.session.MessageCount()
	sm.mu.Unlock()

	if err != nil {
		if notify != nil {
			notify(err)
		}
		return fmt.Errorf("failed to save session: %w", err)
	}
	logging.Debug("session saved", "session_id", sessionID, "messages", msgCount)
	return nil
}

// LoadLast attempts to load the most recent session.
// Returns the loaded session state and info about it, or error if none found.
func (sm *SessionManager) LoadLast() (*SessionState, *SessionInfo, error) {
	if !sm.config.Enabled || !sm.config.AutoLoad {
		return nil, nil, nil
	}
	return sm.loadLatestSession()
}

// LoadLatest explicitly selects the newest usable session for the active
// workspace, regardless of the automatic-startup AutoLoad preference. It is
// the persistence boundary for an explicit CLI --continue request: disabling
// ambient auto-resume must not make a user-requested continue silently start a
// fresh conversation.
func (sm *SessionManager) LoadLatest() (*SessionState, *SessionInfo, error) {
	if !sm.config.Enabled {
		return nil, nil, wrapSessionLoadError(SessionLoadKindPersistenceDisabled, "",
			fmt.Errorf("session persistence is disabled"))
	}
	return sm.loadLatestSession()
}

func (sm *SessionManager) loadLatestSession() (*SessionState, *SessionInfo, error) {
	if sm.session == nil {
		return nil, nil, fmt.Errorf("cannot load a session without an active session")
	}

	sessions, err := sm.historyManager.ListSessions()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list sessions: %w", err)
	}

	if len(sessions) == 0 {
		return nil, nil, nil // No sessions to load
	}

	// Find candidate sessions for the current WorkDir. Only exact directory
	// matches — never load sessions from other directories.
	var candidates []SessionInfo
	targetDir := sm.session.GetWorkDir()
	if targetDir != "" {
		targetDir = filepath.Clean(targetDir)
	}
	for i := range sessions {
		sessionDir := sessions[i].WorkDir
		if sessionDir != "" {
			sessionDir = filepath.Clean(sessionDir)
		}
		// Strict match: both must be non-empty and identical
		if targetDir == "" || sessionDir == "" || sessionDir != targetDir {
			continue
		}
		candidates = append(candidates, sessions[i])
	}

	if len(candidates) == 0 {
		return nil, nil, nil
	}

	// Metadata listing is intentionally shallow. Try newest to oldest until one
	// candidate also survives full decoding and identity/project validation, so
	// one partially-written or schema-damaged newest file cannot block every
	// older intact snapshot. Preserve the newest failure if none are usable.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].LastActive.Equal(candidates[j].LastActive) {
			return candidates[i].ID < candidates[j].ID
		}
		return candidates[i].LastActive.After(candidates[j].LastActive)
	})
	var firstErr error
	for i := range candidates {
		candidate := &candidates[i]
		state, loadErr := sm.historyManager.LoadFull(candidate.ID)
		if loadErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to load session %s: %w", candidate.ID, loadErr)
			}
			logging.Debug("skipping unusable session snapshot", "session_id", candidate.ID, "error", loadErr)
			continue
		}

		// Revalidate the fully decoded state at the resume boundary. The metadata
		// scan must never be the sole authority for identity or project ownership.
		stateDir := state.WorkDir
		if stateDir != "" {
			stateDir = filepath.Clean(stateDir)
		}
		if state.ID != candidate.ID {
			loadErr = fmt.Errorf("refusing mismatched session %s containing id %s", candidate.ID, state.ID)
		} else if targetDir == "" || stateDir == "" || stateDir != targetDir {
			loadErr = fmt.Errorf("refusing session %s from a different work directory", candidate.ID)
		}
		if loadErr != nil {
			if firstErr == nil {
				firstErr = loadErr
			}
			logging.Debug("skipping invalid session snapshot", "session_id", candidate.ID, "error", loadErr)
			continue
		}
		currentDir := sm.session.GetWorkDir()
		if currentDir != "" {
			currentDir = filepath.Clean(currentDir)
		}
		if currentDir == "" || currentDir != targetDir {
			return nil, nil, fmt.Errorf("working directory changed while loading session %s", candidate.ID)
		}

		logging.Info("loaded previous session",
			"session_id", candidate.ID,
			"messages", candidate.MessageCount,
			"last_active", candidate.LastActive.Format("2006-01-02 15:04:05"))

		return state, candidate, nil
	}

	return nil, nil, firstErr
}

// LoadSession loads one exact persisted identity for the current project
// without mutating the active Session. Callers can perform provider/policy
// checks and then explicitly call RestoreFromState. Both working directories
// must be non-empty and equal; an explicit ID is not permission to cross a
// project boundary.
func (sm *SessionManager) LoadSession(sessionID string) (*SessionState, *SessionInfo, error) {
	if !sm.config.Enabled {
		return nil, nil, wrapSessionLoadError(SessionLoadKindPersistenceDisabled, sessionID,
			fmt.Errorf("session persistence is disabled"))
	}
	if sm.session == nil {
		return nil, nil, wrapSessionLoadError(SessionLoadKindStorage, sessionID,
			fmt.Errorf("cannot load a session without an active session"))
	}
	if err := ValidateSessionID(sessionID); err != nil {
		return nil, nil, wrapSessionLoadError(SessionLoadKindInvalidID, sessionID, err)
	}

	state, err := sm.historyManager.LoadFull(sessionID)
	if err != nil {
		kind := SessionLoadKindStorage
		switch {
		case errors.Is(err, os.ErrNotExist):
			kind = SessionLoadKindNotFound
		case errors.Is(err, errSessionIdentityMismatch):
			kind = SessionLoadKindIdentityMismatch
		case errors.Is(err, errSessionStateCorrupt):
			kind = SessionLoadKindCorrupt
		}
		return nil, nil, wrapSessionLoadError(kind, sessionID,
			fmt.Errorf("failed to load session %s: %w", sessionID, err))
	}
	if state.ID != sessionID {
		return nil, nil, wrapSessionLoadError(SessionLoadKindIdentityMismatch, sessionID,
			fmt.Errorf("%w: requested %q, file contains %q", errSessionIdentityMismatch, sessionID, state.ID))
	}
	targetDir := sm.session.GetWorkDir()
	stateDir := state.WorkDir
	if targetDir == "" || stateDir == "" || filepath.Clean(targetDir) != filepath.Clean(stateDir) {
		return nil, nil, wrapSessionLoadError(SessionLoadKindForeignProject, sessionID,
			fmt.Errorf("refusing session %s from a different work directory", sessionID))
	}

	info := &SessionInfo{
		ID:           state.ID,
		StartTime:    state.StartTime,
		LastActive:   state.LastActive,
		Summary:      state.Summary,
		MessageCount: len(state.History),
		WorkDir:      state.WorkDir,
	}
	return state, info, nil
}

// RestoreFromState restores the current session from a saved state.
func (sm *SessionManager) RestoreFromState(state *SessionState) error {
	if sm.session == nil {
		return fmt.Errorf("cannot restore without an active session")
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if err := sm.session.RestoreFromState(state); err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}

	sm.lastSaveTime = time.Now()
	return nil
}

// SaveAfterMessage saves the session after a message is added.
// This is called automatically after each user message and AI response.
func (sm *SessionManager) SaveAfterMessage() error {
	if !sm.config.Enabled {
		return nil
	}

	// Non-blocking async save: signals background goroutine.
	// If a save is already pending, this is a no-op (dedup).
	sm.mu.Lock()
	saveCh := sm.asyncSaveCh
	sm.mu.Unlock()
	if saveCh != nil {
		select {
		case saveCh <- struct{}{}:
		default: // save already pending
		}
	}
	return nil
}

// periodicSave performs periodic save and resets the timer.
// Detects sleep/wake gaps: if the interval since lastSaveTime exceeds
// 2x the configured SaveInterval, the system likely slept.
func (sm *SessionManager) periodicSave() {
	sm.mu.Lock()
	gap := time.Since(sm.lastSaveTime)
	sm.mu.Unlock()

	if gap > 2*sm.config.SaveInterval {
		logging.Info("session save gap detected (possible sleep/wake)",
			"gap", gap.Round(time.Second).String())
	}

	if err := sm.Save(); err != nil {
		logging.Warn("periodic session save failed", "error", err)
	}

	// Reset timer for next save
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.saveTimer != nil {
		sm.saveTimer.Reset(sm.config.SaveInterval)
	}
}

// GetLastSaveTime returns when the session was last saved.
func (sm *SessionManager) GetLastSaveTime() time.Time {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.lastSaveTime
}

// ClearCurrentSession clears the current session from disk.
// Useful when starting a fresh conversation.
func (sm *SessionManager) ClearCurrentSession() error {
	if !sm.config.Enabled {
		return nil
	}
	if sm.session == nil {
		return fmt.Errorf("cannot clear without an active session")
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	if err := sm.historyManager.DeleteSession(sm.session.GetID()); err != nil {
		// Ignore error if file doesn't exist
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to delete session: %w", err)
		}
	}

	return nil
}

// CleanupOldSessions removes old sessions based on age and count limits.
// This runs in the background and does not block the main application.
func (sm *SessionManager) CleanupOldSessions() error {
	if sm.historyManager == nil {
		return nil
	}
	if sm.session == nil {
		return fmt.Errorf("cannot clean sessions without an active session")
	}

	sessions, err := sm.historyManager.ListSessions()
	if err != nil {
		logging.Debug("failed to list sessions for cleanup", "error", err)
		return err
	}

	if len(sessions) == 0 {
		return nil
	}

	// Sort sessions by LastActive (newest first)
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].LastActive.Equal(sessions[j].LastActive) {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].LastActive.After(sessions[j].LastActive)
	})

	cutoff := time.Now().Add(-sm.config.MaxSessionAge)
	deletedCount := 0
	currentSessionID := sm.session.GetID()

	// Delete sessions that are too old
	for i, sess := range sessions {
		// Always keep current session
		if sess.ID == currentSessionID {
			continue
		}

		shouldDelete := false

		// Delete if older than MaxSessionAge
		if sess.LastActive.Before(cutoff) {
			shouldDelete = true
		}

		// Delete if we have more than MaxSessionCount (keeping newest)
		if i >= sm.config.MaxSessionCount {
			shouldDelete = true
		}

		if shouldDelete {
			if err := sm.historyManager.DeleteSession(sess.ID); err != nil {
				logging.Debug("failed to delete old session", "session_id", sess.ID, "error", err)
			} else {
				deletedCount++
			}
		}
	}

	if deletedCount > 0 {
		logging.Info("cleaned up old sessions", "deleted", deletedCount)
	}

	return nil
}
