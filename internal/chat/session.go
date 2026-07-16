package chat

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gokin/internal/logging"
	"gokin/internal/security"
	"gokin/internal/skills"

	"google.golang.org/genai"
)

const (
	// MaxMessages is the maximum number of messages to keep in history.
	MaxMessages = 100
)

// ChangeEvent represents a session history change event.
type ChangeEvent struct {
	OldCount int
	NewCount int
	Version  int64
}

// ChangeHandler is called when session history changes.
type ChangeHandler func(ChangeEvent)

// Session represents a chat session.
type Session struct {
	ID        string
	StartTime time.Time
	WorkDir   string
	History   []*genai.Content
	// Provider is the provider identifier the session was built against
	// (e.g. "kimi", "deepseek", "glm"). Loaded from disk on resume and
	// compared against the active provider — if they differ, the session
	// is dropped because thinking-block signatures and tool_use ID
	// formats don't round-trip across providers. Empty for legacy
	// sessions saved before v0.71.4; those are treated as compatible
	// on first load (one-time migration) and tagged on first save.
	Provider    string
	Branches    map[string]*Session // named branches (forks)
	Checkpoints map[string]int      // named checkpoints (name -> history index)
	// systemInstruction is the system prompt (passed via API parameter, never
	// in history). UNEXPORTED on purpose: it is read by GetState() on the async
	// save goroutine while the app goroutine updates it, so all access MUST go
	// through the locked accessors (GetSystemInstruction / SetSystemInstruction)
	// — a direct field write from another package would race the save.
	systemInstruction       string
	tokenCounts             []int // tokens per message
	totalTokens             int   // cached total
	version                 int64 // version for optimistic concurrency control
	onChange                ChangeHandler
	scratchpad              string
	toolCheckpoints         []SerializedToolCheckpoint     // persisted tool checkpoint journal
	pendingRecoveries       []SerializedPendingRecovery    // durable retry lineage, session-scoped
	invocationLedger        *skills.InvocationLedger       // exact latest rendered Skills, session-scoped
	checkpointInvokedSkills map[string][]skills.Invocation // Skill snapshots for named rollback points
	checkpointRawIndices    map[string]bool                // indices measured without synthetic Skill carry
	mu                      sync.RWMutex
}

// NewSession creates a new chat session.
func NewSession() *Session {
	return &Session{
		ID:                      generateSessionID(),
		StartTime:               time.Now(),
		History:                 make([]*genai.Content, 0),
		invocationLedger:        skills.NewInvocationLedger(),
		checkpointInvokedSkills: make(map[string][]skills.Invocation),
		checkpointRawIndices:    make(map[string]bool),
	}
}

// InvocationLedger returns the session-scoped Skill invocation ledger. The
// pointer remains stable for the Session lifetime so a bound SkillTool keeps
// writing into the same owner across resume/restore operations.
func (s *Session) InvocationLedger() *skills.InvocationLedger {
	s.mu.RLock()
	ledger := s.invocationLedger
	s.mu.RUnlock()
	if ledger != nil {
		return ledger
	}

	// Preserve zero-value Session compatibility for tests and embedders.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.invocationLedger == nil {
		s.invocationLedger = skills.NewInvocationLedger()
	}
	return s.invocationLedger
}

// SetChangeHandler sets the callback for history changes.
func (s *Session) SetChangeHandler(handler ChangeHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = handler
}

// notifyChange notifies the handler of history changes.
// Caller must hold s.mu.Lock(). This method releases the lock before calling
// the handler and does NOT re-acquire it. After calling notifyChange, the
// caller must NOT use locked state (use defer-friendly pattern).
func (s *Session) notifyChange(oldCount int) {
	// Capture event data and handler BEFORE unlocking
	handler := s.onChange
	event := ChangeEvent{
		OldCount: oldCount,
		NewCount: len(s.History),
		Version:  s.version,
	}

	// Always release the lock — whether or not handler is set
	s.mu.Unlock()

	if handler == nil {
		return
	}

	// Protect against panics in the handler — without recover, a misbehaving
	// onChange callback would crash the whole process, since notifyChange is
	// called from many session-mutation paths. Log the panic so it's visible
	// in the gokin log instead of being silently swallowed (the prior version
	// had a recover() with an empty body and a "Log panic but don't crash"
	// TODO that never got filled in).
	defer func() {
		if r := recover(); r != nil {
			logging.Error("panic in session onChange handler",
				"panic", fmt.Sprintf("%v", r),
				"old_count", event.OldCount,
				"new_count", event.NewCount,
				"version", event.Version)
		}
	}()

	// Call handler outside lock (prevent deadlock if handler tries to access session)
	handler(event)
}

// AddUserMessage adds a user message to the history.
func (s *Session) AddUserMessage(message string) {
	s.mu.Lock()

	oldCount := len(s.History)
	s.History = append(s.History, genai.NewContentFromText(message, genai.RoleUser))
	// Keep tokenCounts aligned with History — zero entry until counted.
	s.tokenCounts = append(s.tokenCounts, 0)
	s.version++

	// Auto-trim if history exceeds max
	s.trimHistoryLocked()

	s.notifyChange(oldCount)
}

// AddModelMessage adds a model message to the history.
func (s *Session) AddModelMessage(message string) {
	s.mu.Lock()

	oldCount := len(s.History)
	s.History = append(s.History, genai.NewContentFromText(message, genai.RoleModel))
	s.tokenCounts = append(s.tokenCounts, 0)
	s.version++

	// Auto-trim if history exceeds max
	s.trimHistoryLocked()

	s.notifyChange(oldCount)
}

// AddContent adds raw content to the history.
func (s *Session) AddContent(content *genai.Content) {
	s.mu.Lock()

	oldCount := len(s.History)
	s.History = append(s.History, content)
	s.tokenCounts = append(s.tokenCounts, 0)
	s.version++

	// Auto-trim if history exceeds max
	s.trimHistoryLocked()

	s.notifyChange(oldCount)
}

// SetHistory replaces the entire history and applies sliding window.
func (s *Session) SetHistory(history []*genai.Content) {
	s.SetHistoryWithSkillCarry(history, 0)
}

// SetHistoryWithSkillCarry replaces history and atomically reattaches any
// active Skill snapshots missing from the retained raw messages. insertAt is a
// preferred index in the supplied history (callers that just produced a
// summary normally pass the slot immediately after it).
func (s *Session) SetHistoryWithSkillCarry(history []*genai.Content, insertAt int) []*genai.Content {
	s.mu.Lock()

	oldCount := len(s.History)
	committed := s.prepareHistoryWithSkillCarryLocked(history, insertAt)
	s.History = committed
	s.tokenCounts = make([]int, len(committed)) // keep len(tokenCounts)==len(History)
	s.totalTokens = 0
	s.version++
	s.notifyChange(oldCount)
	return cloneHistorySlice(committed)
}

// SetHistoryIfVersion atomically sets history only if the version matches.
// Returns true if the update was applied, false if version mismatch.
func (s *Session) SetHistoryIfVersion(history []*genai.Content, expectedVersion int64) bool {
	_, committed := s.SetHistoryIfVersionWithSkillCarry(history, expectedVersion, 0)
	return committed
}

// SetHistoryIfVersionWithSkillCarry is SetHistoryWithSkillCarry with optimistic
// concurrency. It returns the exact committed slice for token accounting.
func (s *Session) SetHistoryIfVersionWithSkillCarry(history []*genai.Content, expectedVersion int64, insertAt int) ([]*genai.Content, bool) {
	s.mu.Lock()

	if s.version != expectedVersion {
		s.mu.Unlock()
		return nil, false
	}

	oldCount := len(s.History)
	committed := s.prepareHistoryWithSkillCarryLocked(history, insertAt)
	s.History = committed
	s.tokenCounts = make([]int, len(committed)) // keep len(tokenCounts)==len(History)
	s.totalTokens = 0
	s.version++
	s.notifyChange(oldCount)
	return cloneHistorySlice(committed), true
}

// GetVersion returns the current version of the session history.
func (s *Session) GetVersion() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// GetHistoryWithVersion returns a copy of the history along with its version.
func (s *Session) GetHistoryWithVersion() ([]*genai.Content, int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	history := make([]*genai.Content, len(s.History))
	copy(history, s.History)
	return history, s.version
}

// GetHistory returns a copy of the history.
func (s *Session) GetHistory() []*genai.Content {
	s.mu.RLock()
	defer s.mu.RUnlock()

	history := make([]*genai.Content, len(s.History))
	copy(history, s.History)
	return history
}

// Clear clears the session history.
func (s *Session) Clear() {
	s.mu.Lock()

	oldCount := len(s.History)
	s.History = make([]*genai.Content, 0)
	s.tokenCounts = make([]int, 0)
	s.totalTokens = 0
	if s.invocationLedger != nil {
		s.invocationLedger.Clear()
	}
	// /clear establishes a new conversation root. Retaining an old checkpoint
	// would let /restore silently resurrect both stale history indices and the
	// Skills that were deliberately cleared.
	s.Checkpoints = make(map[string]int)
	s.checkpointInvokedSkills = make(map[string][]skills.Invocation)
	s.checkpointRawIndices = make(map[string]bool)
	// A cleared conversation must not retain a prior turn's mutation replay or
	// an automatic retry capable of resurrecting it.
	s.toolCheckpoints = nil
	s.pendingRecoveries = nil
	s.version++

	s.notifyChange(oldCount) // unlocks mu
}

func cloneSkillInvocations(invocations []skills.Invocation) []skills.Invocation {
	if len(invocations) == 0 {
		return nil
	}
	cloned := make([]skills.Invocation, len(invocations))
	copy(cloned, invocations)
	return cloned
}

// restoreSkillInvocations validates untrusted persisted entries through a
// temporary ledger and then replaces the contents of the stable owner ledger.
// An over-cap payload leaves the temporary ledger empty, so the destination is
// still cleared instead of retaining Skills from a previously loaded session.
func restoreSkillInvocations(destination *skills.InvocationLedger, invocations []skills.Invocation) error {
	validated := skills.NewInvocationLedger()
	validationErr := validated.Restore(invocations)
	// Snapshot entries already passed every bound/hash check. Preserve the
	// destination pointer held by the foreground SkillTool.
	_ = destination.Restore(validated.SnapshotNewestFirst())
	return validationErr
}

// MessageCount returns the number of messages in the session.
func (s *Session) MessageCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.History)
}

// trimHistoryLocked trims history to max messages.
// System instruction is now passed via API parameter, not stored in history.
// Simple sliding window: keep the last MaxMessages messages.
// Caller MUST hold s.mu.Lock() before calling.
func (s *Session) trimHistoryLocked() {
	previousHistory := s.History
	previousCounts := s.tokenCounts
	s.History = s.prepareHistoryWithSkillCarryLocked(s.History, 0)
	newCounts := realignTokenCounts(previousHistory, previousCounts, s.History)
	s.tokenCounts = newCounts
	s.totalTokens = 0
	for _, count := range s.tokenCounts {
		s.totalTokens += count
	}
}

// prepareHistoryWithSkillCarryLocked is the single destructive-history gate.
// It first removes/rebuilds synthetic carry state, enforces the hard message
// window without splitting retained tool pairs, then reserves at most one slot
// for a missing active-Skill block. Caller must hold s.mu.
func (s *Session) prepareHistoryWithSkillCarryLocked(history []*genai.Content, insertAt int) []*genai.Content {
	invocations := []skills.Invocation(nil)
	if s.invocationLedger != nil {
		invocations = s.invocationLedger.SnapshotNewestFirst()
	}

	// Let the carry helper remove any stale block and interpret insertAt against
	// the resulting raw history before we do anything else. This preserves exact
	// placement after a summary even when KeepStart already contained the prior
	// synthetic block.
	committed := skills.ReattachInvocations(history, invocations, insertAt)
	if len(committed) <= MaxMessages {
		return committed
	}

	// Strip the just-rebuilt block before applying the hard raw-history window.
	raw := skills.ReattachInvocations(committed, nil, 0)
	raw, dropped := trimHistoryToLimit(raw, MaxMessages)
	adjustedInsert := insertAt - dropped
	if adjustedInsert < 0 {
		adjustedInsert = 0
	}
	committed = skills.ReattachInvocations(raw, invocations, adjustedInsert)
	if len(committed) <= MaxMessages {
		return committed
	}

	// A carry block is one ordinary user message. Reserve its slot and repeat
	// the raw-snapshot scan after trimming: the second trim may itself remove a
	// previously-retained full Skill FunctionResponse.
	raw, extraDropped := trimHistoryToLimit(raw, MaxMessages-1)
	adjustedInsert -= extraDropped
	if adjustedInsert < 0 {
		adjustedInsert = 0
	}
	committed = skills.ReattachInvocations(raw, invocations, adjustedInsert)
	if len(committed) > MaxMessages {
		// Defensive only: ReattachInvocations is contractually one message. Keep
		// the newest hard-bounded window if a future implementation regresses.
		committed, _ = trimHistoryToLimit(committed, MaxMessages)
	}
	return committed
}

// trimHistoryToLimit returns a suffix no larger than limit and how many leading
// messages were dropped. When retaining a whole pair would exceed the hard
// limit, it keeps the requested boundary and removes only orphaned tool parts
// instead of allowing adjustBoundaryForToolPairs to grow past the cap.
func trimHistoryToLimit(history []*genai.Content, limit int) ([]*genai.Content, int) {
	if limit < 0 {
		limit = 0
	}
	if len(history) <= limit {
		return history, 0
	}
	desired := len(history) - limit
	boundary := adjustBoundaryForToolPairs(history, desired)
	if boundary < desired {
		boundary = desired
	}
	if boundary > len(history) {
		boundary = len(history)
	}
	trimmed := pruneUnpairedToolParts(history[boundary:])
	if len(trimmed) > limit {
		// pruneUnpairedToolParts never grows the slice; retain this hard backstop
		// for future changes and malformed histories.
		extra := len(trimmed) - limit
		boundary += extra
		trimmed = pruneUnpairedToolParts(trimmed[extra:])
	}
	return trimmed, boundary
}

// pruneUnpairedToolParts balances duplicate IDs by count and drops only the
// excess FunctionResponse parts. Unmatched FunctionCalls are retained because
// the newest call may be pending while its executor response is still being
// produced. Plain text and ID-less legacy parts remain untouched. It allocates
// only when an orphaned response is present.
func pruneUnpairedToolParts(history []*genai.Content) []*genai.Content {
	callCounts := make(map[string]int)
	responseCounts := make(map[string]int)
	for _, content := range history {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			if call := part.FunctionCall; call != nil && call.ID != "" {
				callCounts[call.ID]++
			}
			if response := part.FunctionResponse; response != nil && response.ID != "" {
				responseCounts[response.ID]++
			}
		}
	}
	paired := make(map[string]int, len(callCounts)+len(responseCounts))
	orphans := 0
	for id, calls := range callCounts {
		pairs := min(calls, responseCounts[id])
		paired[id] = pairs
		// An unmatched call may be the live tail of the current tool turn. It is
		// safe to retain; deleting it would make the eventual response an orphan.
	}
	for id, responses := range responseCounts {
		orphans += responses - paired[id]
	}
	if orphans == 0 {
		return history
	}

	seenResponses := make(map[string]int, len(paired))
	result := make([]*genai.Content, 0, len(history))
	for _, content := range history {
		if content == nil {
			continue
		}
		parts := make([]*genai.Part, 0, len(content.Parts))
		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			keep := true
			if response := part.FunctionResponse; response != nil && response.ID != "" {
				keep = seenResponses[response.ID] < paired[response.ID]
				seenResponses[response.ID]++
			}
			if keep {
				parts = append(parts, part)
			}
		}
		if len(parts) == 0 {
			continue
		}
		if len(parts) == len(content.Parts) {
			result = append(result, content)
		} else {
			result = append(result, &genai.Content{Role: content.Role, Parts: parts})
		}
	}
	return result
}

func realignTokenCounts(oldHistory []*genai.Content, oldCounts []int, newHistory []*genai.Content) []int {
	positions := make(map[*genai.Content][]int, len(oldHistory))
	for index, content := range oldHistory {
		count := 0
		if index < len(oldCounts) {
			count = oldCounts[index]
		}
		positions[content] = append(positions[content], count)
	}
	used := make(map[*genai.Content]int, len(positions))
	result := make([]int, len(newHistory))
	for index, content := range newHistory {
		cursor := used[content]
		if counts := positions[content]; cursor < len(counts) {
			result[index] = counts[cursor]
			used[content] = cursor + 1
		}
	}
	return result
}

func cloneHistorySlice(history []*genai.Content) []*genai.Content {
	cloned := make([]*genai.Content, len(history))
	copy(cloned, history)
	return cloned
}

// TrimHistory manually triggers history trimming to max messages.
func (s *Session) TrimHistory() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trimHistoryLocked()
}

// generateSessionID generates a unique session ID.
func generateSessionID() string {
	b := make([]byte, 3)
	_, _ = cryptorand.Read(b)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// AddContentWithTokens adds content with its token count.
func (s *Session) AddContentWithTokens(content *genai.Content, tokens int) {
	s.mu.Lock()

	oldCount := len(s.History)
	s.History = append(s.History, content)
	s.tokenCounts = append(s.tokenCounts, tokens)
	s.totalTokens += tokens
	s.version++
	s.trimHistoryLocked()
	s.notifyChange(oldCount) // unlocks s.mu
}

// GetTokenCount returns the cached total token count.
func (s *Session) GetTokenCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.totalTokens
}

// SetTotalTokens sets the total token count (from external counting).
func (s *Session) SetTotalTokens(tokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totalTokens = tokens
}

// ReplaceWithSummary replaces messages up to index with a summary.
func (s *Session) ReplaceWithSummary(upToIndex int, summary *genai.Content, summaryTokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if upToIndex > len(s.History) {
		upToIndex = len(s.History)
	}

	// Adjust boundary to avoid splitting tool pairs
	upToIndex = adjustBoundaryForToolPairs(s.History, upToIndex)

	// Keep messages after upToIndex
	remaining := s.History[upToIndex:]

	// Extract remaining token counts aligned to remaining messages.
	// If tokenCounts was shorter than upToIndex, the missing counts are 0.
	// Always produce exactly len(remaining) entries so tokenCounts stays
	// in sync with History after rebuild.
	remainingTokens := make([]int, len(remaining))
	for i := range remaining {
		origIdx := upToIndex + i
		if origIdx < len(s.tokenCounts) {
			remainingTokens[i] = s.tokenCounts[origIdx]
		}
	}

	// Build new history with summary
	s.History = make([]*genai.Content, 0, 1+len(remaining))
	s.History = append(s.History, summary)
	s.History = append(s.History, remaining...)

	// Rebuild token counts — length matches History length
	s.tokenCounts = make([]int, 0, 1+len(remainingTokens))
	s.tokenCounts = append(s.tokenCounts, summaryTokens)
	s.tokenCounts = append(s.tokenCounts, remainingTokens...)
	preCarryHistory := s.History
	preCarryCounts := s.tokenCounts
	s.History = s.prepareHistoryWithSkillCarryLocked(s.History, 1)
	s.tokenCounts = realignTokenCounts(preCarryHistory, preCarryCounts, s.History)

	// Recalculate total
	s.totalTokens = 0
	for _, t := range s.tokenCounts {
		s.totalTokens += t
	}
	s.version++
}

// GetTokenCounts returns token counts per message.
func (s *Session) GetTokenCounts() []int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	counts := make([]int, len(s.tokenCounts))
	copy(counts, s.tokenCounts)
	return counts
}

// SetWorkDir sets the working directory for this session.
func (s *Session) SetWorkDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.WorkDir = dir
}

// GetWorkDir returns the session working directory under lock. Persistence and
// resume code run concurrently with session setup/export paths and must not
// read the exported field without synchronization.
func (s *Session) GetWorkDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.WorkDir
}

// SetProvider records the provider identifier the session was built
// against. Used by the auto-resume path to detect cross-provider
// contamination — replaying a Kimi session's history to DeepSeek, for
// instance, 400s because the thinking-block signature formats differ.
func (s *Session) SetProvider(provider string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Provider = provider
}

// GetProvider returns the provider the session was last tagged for.
// Empty string means a legacy session saved before v0.71.4 — caller
// should treat as compatible on the first load and tag on save.
func (s *Session) GetProvider() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Provider
}

// GetID returns the session's ID under lock.
func (s *Session) GetID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ID
}

// SetID updates the session's ID under lock and returns the PREVIOUS value.
// GetState() reads s.ID on the async save goroutine while the app goroutine
// may swap it (e.g. SaveCommand's temporary rename-for-export), so every
// external mutation must go through here rather than a direct field write —
// round 6: SaveCommand.Execute wrote session.ID directly at 3 sites, racing
// the autosave goroutine's locked read (concretely reachable: a queued
// autosave from a just-sent message racing an immediately-following /save).
func (s *Session) SetID(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.ID
	s.ID = id
	return prev
}

// GetSystemInstruction returns the session's system instruction under lock.
func (s *Session) GetSystemInstruction() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.systemInstruction
}

// SetSystemInstruction updates the session's system instruction under lock.
// GetState() reads this field on the async save goroutine, so every external
// mutation must go through here rather than a direct field write.
func (s *Session) SetSystemInstruction(instruction string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.systemInstruction = instruction
}

// GetState returns the current state of the session for serialization.
func (s *Session) GetState() *SessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	history := make([]SerializedContent, len(s.History))
	for i, content := range s.History {
		history[i] = SerializeContent(content)
	}

	state := &SessionState{
		ID:                s.ID,
		StartTime:         s.StartTime,
		LastActive:        time.Now(),
		WorkDir:           s.WorkDir,
		Provider:          s.Provider,
		History:           history,
		TokenCounts:       make([]int, len(s.tokenCounts)),
		TotalTokens:       s.totalTokens,
		Version:           s.version,
		Scratchpad:        s.scratchpad,
		SystemInstruction: s.systemInstruction,
	}
	if s.invocationLedger != nil {
		state.InvokedSkills = s.invocationLedger.SnapshotNewestFirst()
	}
	if len(s.checkpointInvokedSkills) > 0 {
		state.CheckpointInvokedSkills = make(map[string][]skills.Invocation, len(s.checkpointInvokedSkills))
		for name, invocations := range s.checkpointInvokedSkills {
			state.CheckpointInvokedSkills[name] = cloneSkillInvocations(invocations)
		}
	}
	copy(state.TokenCounts, s.tokenCounts)

	// Persist checkpoints
	if len(s.Checkpoints) > 0 {
		state.Checkpoints = make(map[string]int, len(s.Checkpoints))
		for k, v := range s.Checkpoints {
			state.Checkpoints[k] = v
		}
	}
	if len(s.checkpointRawIndices) > 0 {
		state.CheckpointRawIndices = make(map[string]bool, len(s.checkpointRawIndices))
		for name, raw := range s.checkpointRawIndices {
			if raw {
				state.CheckpointRawIndices[name] = true
			}
		}
	}

	// Persist branches (recursive — each branch is a Session)
	if len(s.Branches) > 0 {
		state.Branches = make(map[string]*SessionState, len(s.Branches))
		for name, branch := range s.Branches {
			state.Branches[name] = branch.GetState()
		}
	}

	// Persist tool checkpoints
	if len(s.toolCheckpoints) > 0 {
		state.ToolCheckpoints = cloneSerializedToolCheckpoints(s.toolCheckpoints)
	}
	if len(s.pendingRecoveries) > 0 {
		state.PendingRecoveries = cloneSerializedPendingRecoveries(s.pendingRecoveries)
	}

	// Generate summary
	state.Summary = state.GenerateSummary()

	return state
}

// RestoreFromState restores the session from a saved state.
func (s *Session) RestoreFromState(state *SessionState) error {
	if state == nil {
		return fmt.Errorf("cannot restore nil session state")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	history := make([]*genai.Content, len(state.History))
	for i, sc := range state.History {
		content, err := DeserializeContent(sc)
		if err != nil {
			return err
		}
		history[i] = content
	}

	// Fix legacy sessions that have empty tool call IDs
	fixEmptyToolIDs(history)

	s.ID = state.ID
	s.StartTime = state.StartTime
	s.WorkDir = state.WorkDir
	s.Provider = state.Provider
	s.History = history
	// Align tokenCounts with history length. Legacy sessions serialised with
	// an empty TokenCounts slice (saved before v0.78.32) would violate
	// len(tokenCounts)==len(History); pad with zeros in that case.
	s.tokenCounts = make([]int, len(history))
	for i, c := range state.TokenCounts {
		if i < len(history) {
			s.tokenCounts[i] = c
		}
	}
	s.totalTokens = state.TotalTokens
	s.version = state.Version
	s.scratchpad = state.Scratchpad
	s.systemInstruction = state.SystemInstruction
	if s.invocationLedger == nil {
		s.invocationLedger = skills.NewInvocationLedger()
	}
	if err := restoreSkillInvocations(s.invocationLedger, state.InvokedSkills); err != nil {
		// Restore publishes every valid bounded entry atomically and reports only
		// rejected corrupt entries. Do not make an otherwise healthy session
		// unresumable because one optional Skill snapshot was damaged.
		logging.Warn("skipped invalid persisted skill invocations", "error", err)
	}
	preCarryHistory := s.History
	preCarryCounts := s.tokenCounts
	s.History = s.prepareHistoryWithSkillCarryLocked(s.History, 0)
	s.tokenCounts = realignTokenCounts(preCarryHistory, preCarryCounts, s.History)
	s.totalTokens = 0
	for _, count := range s.tokenCounts {
		s.totalTokens += count
	}
	s.checkpointInvokedSkills = make(map[string][]skills.Invocation, len(state.CheckpointInvokedSkills))
	for name, invocations := range state.CheckpointInvokedSkills {
		validated := skills.NewInvocationLedger()
		if err := restoreSkillInvocations(validated, invocations); err != nil {
			logging.Warn("skipped invalid checkpoint skill invocations", "checkpoint", name, "error", err)
		}
		s.checkpointInvokedSkills[name] = validated.SnapshotNewestFirst()
	}

	// Restore checkpoints — ALWAYS replace (including clearing when the restored
	// state has none), so a prior session's checkpoints don't survive across
	// `/resume <id>` into a different session. A stale checkpoint maps a name to
	// a history index that no longer exists in the newly-restored history, so a
	// later `/restore <name>` would jump to a wrong/invalid position.
	s.Checkpoints = make(map[string]int, len(state.Checkpoints))
	for k, v := range state.Checkpoints {
		s.Checkpoints[k] = v
	}
	s.checkpointRawIndices = make(map[string]bool, len(state.CheckpointRawIndices))
	for name, raw := range state.CheckpointRawIndices {
		if raw {
			s.checkpointRawIndices[name] = true
		}
	}

	// Restore branches (recursive) — always replace for the same reason.
	s.Branches = make(map[string]*Session, len(state.Branches))
	for name, branchState := range state.Branches {
		branch := NewSession()
		if err := branch.RestoreFromState(branchState); err != nil {
			return fmt.Errorf("failed to restore branch %q: %w", name, err)
		}
		s.Branches[name] = branch
	}

	// Restore tool checkpoints — always replace.
	s.toolCheckpoints = cloneSerializedToolCheckpoints(state.ToolCheckpoints)
	s.pendingRecoveries = cloneSerializedPendingRecoveries(state.PendingRecoveries)

	return nil
}

// GetScratchpad returns the current scratchpad content.
func (s *Session) GetScratchpad() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scratchpad
}

// SetScratchpad sets the scratchpad content.
func (s *Session) SetScratchpad(content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scratchpad = content
}

// GetToolCheckpoints returns persisted tool checkpoint entries.
func (s *Session) GetToolCheckpoints() []SerializedToolCheckpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSerializedToolCheckpoints(s.toolCheckpoints)
}

// SetToolCheckpoints sets the persisted tool checkpoint entries.
func (s *Session) SetToolCheckpoints(checkpoints []SerializedToolCheckpoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCheckpoints = cloneSerializedToolCheckpoints(checkpoints)
}

// GetPendingRecoveries returns an ownership-safe snapshot of all durable
// retries for this session.
func (s *Session) GetPendingRecoveries() []SerializedPendingRecovery {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSerializedPendingRecoveries(s.pendingRecoveries)
}

// AddPendingRecovery appends a new retry, or atomically replaces replaceID
// when a claimed attempt schedules its next generation. A missing replaceID
// fails closed so an unrelated durable retry is never silently displaced.
func (s *Session) AddPendingRecovery(recovery SerializedPendingRecovery, replaceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if replaceID != "" {
		for i := range s.pendingRecoveries {
			if s.pendingRecoveries[i].ID == replaceID {
				s.pendingRecoveries[i] = cloneSerializedPendingRecovery(recovery)
				return true
			}
		}
		return false
	}
	for i := range s.pendingRecoveries {
		if s.pendingRecoveries[i].ID == recovery.ID {
			return false
		}
	}
	s.pendingRecoveries = append(s.pendingRecoveries, cloneSerializedPendingRecovery(recovery))
	return true
}

// TransitionPendingRecovery performs a compare-and-swap state transition and
// returns the transitioned immutable snapshot.
func (s *Session) TransitionPendingRecovery(id, sessionID, from, to string) (SerializedPendingRecovery, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.pendingRecoveries {
		recovery := &s.pendingRecoveries[i]
		if recovery.ID != id || recovery.SessionID != sessionID || recovery.State != from {
			continue
		}
		recovery.State = to
		recovery.GenerationSignature = PendingRecoveryGenerationSignature(*recovery)
		return cloneSerializedPendingRecovery(*recovery), true
	}
	return SerializedPendingRecovery{}, false
}

// RemovePendingRecovery removes only the matching generation.
func (s *Session) RemovePendingRecovery(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.pendingRecoveries {
		if s.pendingRecoveries[i].ID != id {
			continue
		}
		copy(s.pendingRecoveries[i:], s.pendingRecoveries[i+1:])
		s.pendingRecoveries[len(s.pendingRecoveries)-1] = SerializedPendingRecovery{}
		s.pendingRecoveries = s.pendingRecoveries[:len(s.pendingRecoveries)-1]
		return true
	}
	return false
}

func cloneSerializedPendingRecoveries(src []SerializedPendingRecovery) []SerializedPendingRecovery {
	if len(src) == 0 {
		return nil
	}
	out := make([]SerializedPendingRecovery, len(src))
	for i := range src {
		out[i] = cloneSerializedPendingRecovery(src[i])
	}
	return out
}

func cloneSerializedPendingRecovery(src SerializedPendingRecovery) SerializedPendingRecovery {
	out := src
	out.Checkpoints = cloneSerializedToolCheckpoints(src.Checkpoints)
	return out
}

func cloneSerializedToolCheckpoints(src []SerializedToolCheckpoint) []SerializedToolCheckpoint {
	if len(src) == 0 {
		return nil
	}
	out := make([]SerializedToolCheckpoint, len(src))
	for i := range src {
		out[i] = src[i]
		if src[i].Args != nil {
			out[i].Args = make(map[string]any, len(src[i].Args))
			for key, value := range src[i].Args {
				out[i].Args[key] = cloneSerializedJSONValue(value)
			}
		}
		if src[i].ResultV2 != nil {
			result := *src[i].ResultV2
			result.Data = append(json.RawMessage(nil), src[i].ResultV2.Data...)
			if src[i].ResultV2.PolicyBlock != nil {
				block := *src[i].ResultV2.PolicyBlock
				result.PolicyBlock = &block
			}
			out[i].ResultV2 = &result
		}
	}
	return out
}

func cloneSerializedJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneSerializedJSONValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = cloneSerializedJSONValue(typed[i])
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	case json.RawMessage:
		return append(json.RawMessage(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	default:
		// JSON scalars (string, float64, bool, nil) and any immutable custom
		// scalar are safe to share.
		return value
	}
}

// --- Session Branching (Forking) ---

// Fork creates a branch by copying the current history into a new Session.
// The branch is stored in the Branches map under the given name.
func (s *Session) Fork(name string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Branches == nil {
		s.Branches = make(map[string]*Session)
	}

	// Copy current history into a new session
	historyCopy := make([]*genai.Content, len(s.History))
	copy(historyCopy, s.History)

	tokenCountsCopy := make([]int, len(s.tokenCounts))
	copy(tokenCountsCopy, s.tokenCounts)

	branch := &Session{
		ID:                      generateSessionID() + "-" + name,
		StartTime:               time.Now(),
		WorkDir:                 s.WorkDir,
		History:                 historyCopy,
		tokenCounts:             tokenCountsCopy,
		totalTokens:             s.totalTokens,
		scratchpad:              s.scratchpad,
		systemInstruction:       s.systemInstruction,
		invocationLedger:        skills.NewInvocationLedger(),
		checkpointInvokedSkills: make(map[string][]skills.Invocation, len(s.checkpointInvokedSkills)),
		checkpointRawIndices:    make(map[string]bool, len(s.checkpointRawIndices)),
		Checkpoints:             make(map[string]int, len(s.Checkpoints)),
	}
	if s.invocationLedger != nil {
		_ = branch.invocationLedger.Restore(s.invocationLedger.SnapshotNewestFirst())
	}
	for checkpoint, invocations := range s.checkpointInvokedSkills {
		branch.checkpointInvokedSkills[checkpoint] = cloneSkillInvocations(invocations)
	}
	for checkpoint, index := range s.Checkpoints {
		branch.Checkpoints[checkpoint] = index
	}
	for checkpoint, raw := range s.checkpointRawIndices {
		if raw {
			branch.checkpointRawIndices[checkpoint] = true
		}
	}

	s.Branches[name] = branch
	return branch
}

// GetBranch retrieves a branch by name.
func (s *Session) GetBranch(name string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.Branches == nil {
		return nil, false
	}
	branch, ok := s.Branches[name]
	return branch, ok
}

// ListBranches returns all branch names.
func (s *Session) ListBranches() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.Branches == nil {
		return nil
	}
	names := make([]string, 0, len(s.Branches))
	for name := range s.Branches {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// --- Named Checkpoints ---

// SaveCheckpoint saves the current history length as a named checkpoint.
func (s *Session) SaveCheckpoint(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Checkpoints == nil {
		s.Checkpoints = make(map[string]int)
	}
	// Synthetic active-skill carry can appear or disappear before this boundary
	// when a Skill is updated. Store the checkpoint against stable raw history so
	// later carry rebuilds cannot shift which real message the index denotes.
	rawHistory := skills.ReattachInvocations(s.History, nil, 0)
	s.Checkpoints[name] = len(rawHistory)
	if s.checkpointRawIndices == nil {
		s.checkpointRawIndices = make(map[string]bool)
	}
	s.checkpointRawIndices[name] = true
	if s.checkpointInvokedSkills == nil {
		s.checkpointInvokedSkills = make(map[string][]skills.Invocation)
	}
	if s.invocationLedger != nil {
		s.checkpointInvokedSkills[name] = s.invocationLedger.SnapshotNewestFirst()
	} else {
		s.checkpointInvokedSkills[name] = nil
	}
}

// RestoreCheckpoint truncates the history to the saved checkpoint index.
// Returns true if the checkpoint was found and restored, false otherwise.
func (s *Session) RestoreCheckpoint(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Checkpoints == nil {
		return false
	}
	idx, ok := s.Checkpoints[name]
	if !ok {
		return false
	}
	restoreBase := s.History
	if s.checkpointRawIndices[name] {
		restoreBase = skills.ReattachInvocations(s.History, nil, 0)
	}
	if idx < 0 || idx > len(restoreBase) {
		// Checkpoint indices come from persisted state and are therefore
		// untrusted. Reject before touching either history or active Skills.
		delete(s.Checkpoints, name)
		delete(s.checkpointInvokedSkills, name)
		delete(s.checkpointRawIndices, name)
		return false
	}
	if invocations, exists := s.checkpointInvokedSkills[name]; exists {
		if s.invocationLedger == nil {
			s.invocationLedger = skills.NewInvocationLedger()
		}
		if err := restoreSkillInvocations(s.invocationLedger, invocations); err != nil {
			logging.Warn("skipped invalid checkpoint skill invocations during restore", "checkpoint", name, "error", err)
		}
	} else if s.invocationLedger != nil {
		// Legacy checkpoints have no Skill snapshot. Keeping today's invocations
		// would falsely carry workflows loaded after the rollback point; fail
		// closed to an empty active set.
		s.invocationLedger.Clear()
	}

	// Truncate the checkpoint's index basis, then remove any orphaned responses.
	// For new checkpoints restoreBase excludes synthetic carry; legacy persisted
	// checkpoints retain their historical full-history indexing semantics.
	oldHistory := s.History
	oldCounts := s.tokenCounts
	s.History = restoreBase[:idx]
	s.History = removeOrphanedToolParts(s.History)
	s.tokenCounts = realignTokenCounts(oldHistory, oldCounts, s.History)

	rawNewLen := len(skills.ReattachInvocations(s.History, nil, 0))
	legacyNewLen := len(s.History)

	// Keep cached token state aligned before rebuilding carry below.
	s.totalTokens = 0
	for _, count := range s.tokenCounts {
		s.totalTokens += count
	}
	preCarryHistory := s.History
	preCarryCounts := s.tokenCounts
	s.History = s.prepareHistoryWithSkillCarryLocked(s.History, 0)
	s.tokenCounts = realignTokenCounts(preCarryHistory, preCarryCounts, s.History)
	s.totalTokens = 0
	for _, count := range s.tokenCounts {
		s.totalTokens += count
	}

	// Remove any checkpoints that referenced indices beyond the new length.
	// Compare against the post-orphan-removal length in each checkpoint's own
	// index basis, NOT the original index idx. Removal can shorten History below
	// idx, so a checkpoint in (newLen, idx] — including the restore
	// target itself — would otherwise survive pointing past len(History), making
	// a later RestoreCheckpoint to it a silent no-op that still returns true.
	for cpName, cpIdx := range s.Checkpoints {
		limit := legacyNewLen
		if s.checkpointRawIndices[cpName] {
			limit = rawNewLen
		}
		if cpIdx > limit {
			delete(s.Checkpoints, cpName)
			delete(s.checkpointInvokedSkills, cpName)
			delete(s.checkpointRawIndices, cpName)
		}
	}
	// Rewinding history invalidates the request context attached to any delayed
	// retry. Retaining its mutation ledger would resume an old request against a
	// different conversation root. The rollback itself is the user's explicit
	// recovery choice, so cancel all automatic side-effect replay generations.
	s.toolCheckpoints = nil
	s.pendingRecoveries = nil

	s.version++
	return true
}

// ListCheckpoints returns checkpoint names sorted by their history index.
func (s *Session) ListCheckpoints() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.Checkpoints == nil {
		return nil
	}

	type cpEntry struct {
		name string
		idx  int
	}
	entries := make([]cpEntry, 0, len(s.Checkpoints))
	for name, idx := range s.Checkpoints {
		entries = append(entries, cpEntry{name: name, idx: idx})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].idx < entries[j].idx
	})

	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.name
	}
	return names
}

// --- Tool Pair Safety ---

// adjustBoundaryForToolPairs shifts a trim boundary so that FunctionCall/FunctionResponse
// pairs are not split. Scans ±10 messages around boundary.
// This is a local copy of context.AdjustBoundaryForToolPairs to avoid an import cycle
// (chat cannot import internal/context).
func adjustBoundaryForToolPairs(history []*genai.Content, boundary int) int {
	if boundary <= 0 || boundary >= len(history) {
		return boundary
	}

	adjusted := boundary

	for iter := 0; iter < 3; iter++ {
		prev := adjusted

		rightCallIDs, rightResponseIDs := collectToolIDs(history, adjusted)

		// Case 1: FunctionCall left of boundary with FunctionResponse on right — move left
		for i := adjusted - 1; i >= 0 && i >= adjusted-10; i-- {
			if history[i] == nil {
				continue
			}
			for _, part := range history[i].Parts {
				if part != nil && part.FunctionCall != nil && part.FunctionCall.ID != "" {
					if rightResponseIDs[part.FunctionCall.ID] {
						if i < adjusted {
							adjusted = i
						}
					}
				}
			}
		}

		// Case 2: orphaned FunctionResponse at/after boundary — move right past it
		if adjusted != prev {
			rightCallIDs, _ = collectToolIDs(history, adjusted)
		}
		scanStart := adjusted
		for i := scanStart; i < len(history) && i < scanStart+10; i++ {
			if history[i] == nil {
				continue
			}
			hasOrphan := false
			for _, part := range history[i].Parts {
				if part != nil && part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
					if !rightCallIDs[part.FunctionResponse.ID] {
						hasOrphan = true
					}
				}
			}
			if hasOrphan {
				adjusted = i + 1
			} else {
				break
			}
		}

		if adjusted == prev {
			break
		}
	}

	if adjusted < 0 {
		adjusted = 0
	}
	if adjusted > len(history) {
		adjusted = len(history)
	}
	return adjusted
}

// collectToolIDs collects FunctionCall IDs and FunctionResponse IDs from history[boundary:].
func collectToolIDs(history []*genai.Content, boundary int) (callIDs, responseIDs map[string]bool) {
	callIDs = make(map[string]bool)
	responseIDs = make(map[string]bool)
	for i := boundary; i < len(history); i++ {
		if history[i] == nil {
			continue
		}
		for _, part := range history[i].Parts {
			if part == nil {
				continue
			}
			if part.FunctionCall != nil && part.FunctionCall.ID != "" {
				callIDs[part.FunctionCall.ID] = true
			}
			if part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
				responseIDs[part.FunctionResponse.ID] = true
			}
		}
	}
	return
}

// removeOrphanedToolParts removes trailing orphaned FunctionResponse messages
// that have no matching FunctionCall in history. Used after right-truncation (:idx).
func removeOrphanedToolParts(history []*genai.Content) []*genai.Content {
	// Collect all FunctionCall IDs in the history
	callIDs := make(map[string]bool)
	for _, msg := range history {
		if msg == nil {
			continue
		}
		for _, part := range msg.Parts {
			if part != nil && part.FunctionCall != nil && part.FunctionCall.ID != "" {
				callIDs[part.FunctionCall.ID] = true
			}
		}
	}

	// Remove trailing messages that only contain orphaned FunctionResponses
	for i := len(history) - 1; i >= 0; i-- {
		if history[i] == nil {
			history = history[:i]
			continue
		}
		allOrphaned := true
		hasFuncResp := false
		for _, part := range history[i].Parts {
			if part == nil {
				continue
			}
			if part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
				hasFuncResp = true
				if callIDs[part.FunctionResponse.ID] {
					allOrphaned = false
				}
			} else if part.Text != "" || part.FunctionCall != nil {
				allOrphaned = false
			}
		}
		if hasFuncResp && allOrphaned {
			history = history[:i]
		} else {
			break
		}
	}
	return history
}

// --- Sensitive Data Redaction ---

var sessionRedactor = security.NewSecretRedactor()

// redactSensitiveData scans text and replaces sensitive patterns with [REDACTED].
func redactSensitiveData(text string) string {
	return sessionRedactor.Redact(text)
}

// --- Export ---

// ExportMarkdown exports the conversation as markdown.
// Each message is formatted with a ## User or ## Assistant header.
// Tool calls are formatted as code blocks.
func (s *Session) ExportMarkdown() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Session %s\n\n", s.ID)
	fmt.Fprintf(&sb, "**Started:** %s\n\n", s.StartTime.Format("2006-01-02 15:04:05"))
	if s.WorkDir != "" {
		fmt.Fprintf(&sb, "**Working Directory:** %s\n\n", s.WorkDir)
	}
	sb.WriteString("---\n\n")

	for _, content := range s.History {
		role := "Assistant"
		if content.Role == string(genai.RoleUser) {
			role = "User"
		}

		fmt.Fprintf(&sb, "## %s\n\n", role)

		for _, part := range content.Parts {
			if part.FunctionCall != nil {
				fmt.Fprintf(&sb, "**Tool Call:** `%s`\n\n", part.FunctionCall.Name)
				if part.FunctionCall.Args != nil {
					argsJSON, err := json.MarshalIndent(part.FunctionCall.Args, "", "  ")
					if err == nil {
						redacted := redactSensitiveData(string(argsJSON))
						sb.WriteString("```json\n")
						sb.WriteString(redacted)
						sb.WriteString("\n```\n\n")
					}
				}
			} else if part.FunctionResponse != nil {
				fmt.Fprintf(&sb, "**Tool Response:** `%s`\n\n", part.FunctionResponse.Name)
				if part.FunctionResponse.Response != nil {
					respJSON, err := json.MarshalIndent(part.FunctionResponse.Response, "", "  ")
					if err == nil {
						redacted := redactSensitiveData(string(respJSON))
						sb.WriteString("```json\n")
						sb.WriteString(redacted)
						sb.WriteString("\n```\n\n")
					}
				}
			} else if part.Text != "" {
				redacted := redactSensitiveData(part.Text)
				sb.WriteString(redacted)
				sb.WriteString("\n\n")
			}
		}
	}

	return sb.String()
}

// ExportJSON exports the session as JSON with history, metadata, and timestamps.
// Sensitive data is redacted before export.
func (s *Session) ExportJSON() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Serialize history
	history := make([]SerializedContent, len(s.History))
	for i, content := range s.History {
		history[i] = SerializeContent(content)
	}

	// Redact sensitive data in serialized history
	for i := range history {
		for j := range history[i].Parts {
			history[i].Parts[j].Text = redactSensitiveData(history[i].Parts[j].Text)
			if history[i].Parts[j].FunctionCall != nil {
				redactMapValues(history[i].Parts[j].FunctionCall.Args)
			}
			if history[i].Parts[j].FunctionResp != nil {
				redactMapValues(history[i].Parts[j].FunctionResp.Response)
			}
		}
	}

	export := struct {
		ID          string              `json:"id"`
		StartTime   time.Time           `json:"start_time"`
		ExportedAt  time.Time           `json:"exported_at"`
		WorkDir     string              `json:"work_dir,omitempty"`
		History     []SerializedContent `json:"history"`
		TotalTokens int                 `json:"total_tokens"`
		Version     int64               `json:"version"`
		Scratchpad  string              `json:"scratchpad,omitempty"`
	}{
		ID:          s.ID,
		StartTime:   s.StartTime,
		ExportedAt:  time.Now(),
		WorkDir:     s.WorkDir,
		History:     history,
		TotalTokens: s.totalTokens,
		Version:     s.version,
		Scratchpad:  redactSensitiveData(s.scratchpad),
	}

	return json.MarshalIndent(export, "", "  ")
}

// redactMapValues recursively redacts sensitive data in map string values.
func redactMapValues(m map[string]any) {
	if m == nil {
		return
	}
	for k, v := range m {
		switch val := v.(type) {
		case string:
			m[k] = sessionRedactor.Redact(val)
		case map[string]any:
			redactMapValues(val)
		case []any:
			redactSliceValues(val)
		}
	}
}

// redactSliceValues recursively redacts sensitive data in slice values.
func redactSliceValues(s []any) {
	for i, v := range s {
		switch val := v.(type) {
		case string:
			s[i] = sessionRedactor.Redact(val)
		case map[string]any:
			redactMapValues(val)
		case []any:
			redactSliceValues(val)
		}
	}
}
