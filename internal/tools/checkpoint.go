package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"gokin/internal/logging"

	"google.golang.org/genai"
)

// ToolCheckpoint records a completed tool execution for recovery on API failure.
type ToolCheckpoint struct {
	CallID    string         `json:"call_id"`
	ToolName  string         `json:"tool_name"`
	Args      map[string]any `json:"args"`
	Result    ToolResult     `json:"result"`
	Signature string         `json:"signature"`
	Timestamp time.Time      `json:"timestamp"`
}

// CheckpointJournal is an append-only journal of tool executions within a single
// executeLoop invocation. When the API call fails after tools have already executed,
// the journal allows replaying cached results instead of re-executing side-effecting tools.
type CheckpointJournal struct {
	mu             sync.Mutex
	entries        []ToolCheckpoint
	byCallID       map[string]int // callID -> index in entries
	bySignature    map[string]int // signature -> index in entries
	replayByCallID map[string]int
	// replayBySignature keeps every matching checkpoint in execution order.
	// Repeated stateful calls with identical tool+args are legitimate (for
	// example, the same test command before and after an edit), so a single map
	// value would lose all but the last outcome during recovery.
	replayBySignature map[string][]int
	replayConsumed    map[int]struct{}
	// replayRemaining counts only the entries captured by BeginReplay. Entries
	// recorded by the new attempt are deliberately outside that generation and
	// must not make an exhausted replay look active again.
	replayRemaining int
}

type checkpointReplayState uint8

const (
	checkpointReplayInactive checkpointReplayState = iota
	checkpointReplayMatched
	checkpointReplayMismatch
	checkpointReplayExhausted
)

// BeginReplay snapshots the current journal as a one-shot recovery window.
// Entries recorded after this call are real work from the new attempt and are
// intentionally absent until a later retry starts a fresh replay generation.
func (j *CheckpointJournal) BeginReplay() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.replayByCallID = cloneCheckpointIndex(j.byCallID)
	j.replayBySignature = make(map[string][]int, len(j.bySignature))
	for idx, entry := range j.entries {
		if entry.Signature == "" {
			continue
		}
		j.replayBySignature[entry.Signature] = append(
			j.replayBySignature[entry.Signature], idx)
	}
	j.replayConsumed = make(map[int]struct{}, len(j.entries))
	j.replayRemaining = len(j.entries)
}

// EndReplay drops the one-shot recovery snapshot without clearing the durable
// journal used for a possible later retry.
func (j *CheckpointJournal) EndReplay() {
	j.mu.Lock()
	j.replayByCallID = nil
	j.replayBySignature = nil
	j.replayConsumed = nil
	j.replayRemaining = 0
	j.mu.Unlock()
}

func cloneCheckpointIndex(src map[string]int) map[string]int {
	dst := make(map[string]int, len(src))
	for key, idx := range src {
		dst[key] = idx
	}
	return dst
}

// NewCheckpointJournal creates a new empty checkpoint journal.
func NewCheckpointJournal() *CheckpointJournal {
	return &CheckpointJournal{
		byCallID:    make(map[string]int),
		bySignature: make(map[string]int),
	}
}

// Record adds a completed tool execution to the journal.
func (j *CheckpointJournal) Record(call *genai.FunctionCall, result ToolResult) {
	if call == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	sig := checkpointSignature(call.Name, call.Args)
	j.recordLocked(call.ID, call.Name, call.Args, result, sig, time.Now())
}

// RecordSerialized adds a pre-serialized checkpoint entry to the journal.
// Used when restoring from persisted session state.
func (j *CheckpointJournal) RecordSerialized(callID, toolName string, args map[string]any, resultContent, signature string, ts time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if signature == "" {
		signature = checkpointSignature(toolName, args)
	}
	content := resultContent
	if content == "" {
		content = "[restored from session]"
	}
	// Legacy serialized checkpoints did not carry outcome metadata. Treat them
	// as completed for replay: reporting a synthetic failure encourages the
	// model to repeat a write/bash call, which is the unsafe outcome this journal
	// exists to prevent. New snapshots use RecordSerializedResult below.
	j.recordLocked(callID, toolName, args, ToolResult{Content: content, Success: true}, signature, ts)
}

// RecordSerializedResult restores a checkpoint while preserving its exact
// success/error semantics. It is used by the versioned session representation.
func (j *CheckpointJournal) RecordSerializedResult(callID, toolName string, args map[string]any, result ToolResult, signature string, ts time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if signature == "" {
		signature = checkpointSignature(toolName, args)
	}
	j.recordLocked(callID, toolName, args, result, signature, ts)
}

// recordLocked appends a checkpoint entry. Caller must hold j.mu.
func (j *CheckpointJournal) recordLocked(callID, toolName string, args map[string]any, result ToolResult, signature string, ts time.Time) {
	idx := len(j.entries)
	j.entries = append(j.entries, ToolCheckpoint{
		CallID:    callID,
		ToolName:  toolName,
		Args:      args,
		Result:    result,
		Signature: signature,
		Timestamp: ts,
	})
	if callID != "" {
		j.byCallID[callID] = idx
	}
	if signature != "" {
		j.bySignature[signature] = idx
	}
}

// Lookup checks if a tool call has already been executed and returns the cached result.
// Returns (result, matchType, found).
func (j *CheckpointJournal) Lookup(call *genai.FunctionCall) (ToolResult, string, bool) {
	if call == nil {
		return ToolResult{}, "", false
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	// A call ID is only an index candidate. Providers may regenerate a retry
	// with the same ID but corrected tool/arguments; replaying stale output in
	// that case would skip the new mutation while claiming it ran.
	sig := checkpointSignature(call.Name, call.Args)
	if call.ID != "" {
		if idx, ok := j.byCallID[call.ID]; ok {
			entry := j.entries[idx]
			if sig != "" && entry.ToolName == call.Name && entry.Signature == sig {
				logging.Debug("checkpoint hit by call_id", "tool", call.Name, "id", call.ID)
				return entry.Result, "checkpoint_call_id", true
			}
		}
	}

	// Try signature match (same tool + same args)
	if sig != "" {
		if idx, ok := j.bySignature[sig]; ok {
			logging.Debug("checkpoint hit by signature", "tool", call.Name)
			return j.entries[idx].Result, "checkpoint_signature", true
		}
	}

	return ToolResult{}, "", false
}

// ConsumeReplay returns a matching recovery result at most once per replay
// generation. Both its call-ID and signature aliases are removed together.
func (j *CheckpointJournal) ConsumeReplay(call *genai.FunctionCall) (ToolResult, string, bool) {
	result, reason, state := j.consumeReplayState(call)
	return result, reason, state == checkpointReplayMatched
}

// consumeReplayState atomically distinguishes a replay hit from a dangerous
// divergence. A miss while captured checkpoints remain is not ordinary new
// work: executing it would change state and make the remaining saved outcomes
// stale. Callers must fail closed without consuming the replay generation.
func (j *CheckpointJournal) consumeReplayState(call *genai.FunctionCall) (ToolResult, string, checkpointReplayState) {
	if call == nil {
		return ToolResult{}, "", checkpointReplayInactive
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.replayConsumed == nil {
		return ToolResult{}, "", checkpointReplayInactive
	}

	sig := checkpointSignature(call.Name, call.Args)
	if call.ID != "" {
		if idx, ok := j.replayByCallID[call.ID]; ok {
			entry, available := j.replayEntryLocked(idx)
			if available && sig != "" && entry.ToolName == call.Name && entry.Signature == sig {
				j.consumeReplayIndexLocked(idx)
				return entry.Result, "checkpoint_call_id", checkpointReplayMatched
			}
		}
	}
	if sig != "" {
		if idx, ok := j.nextReplaySignatureIndexLocked(sig); ok {
			j.consumeReplayIndexLocked(idx)
			return j.entries[idx].Result, "checkpoint_signature", checkpointReplayMatched
		}
	}
	if j.replayRemaining > 0 {
		return ToolResult{}, "", checkpointReplayMismatch
	}
	return ToolResult{}, "", checkpointReplayExhausted
}

// ReplayRemaining returns the number of unconsumed entries in the active
// replay snapshot. It is zero both after exhaustion and outside replay mode;
// consumeReplayState distinguishes those states for executor policy.
func (j *CheckpointJournal) ReplayRemaining() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.replayRemaining
}

func (j *CheckpointJournal) replayEntryLocked(idx int) (ToolCheckpoint, bool) {
	if idx < 0 || idx >= len(j.entries) {
		return ToolCheckpoint{}, false
	}
	if _, consumed := j.replayConsumed[idx]; consumed {
		return ToolCheckpoint{}, false
	}
	return j.entries[idx], true
}

func (j *CheckpointJournal) nextReplaySignatureIndexLocked(signature string) (int, bool) {
	queue := j.replayBySignature[signature]
	for len(queue) > 0 {
		idx := queue[0]
		queue = queue[1:]
		if _, consumed := j.replayConsumed[idx]; consumed {
			continue
		}
		if len(queue) == 0 {
			delete(j.replayBySignature, signature)
		} else {
			j.replayBySignature[signature] = queue
		}
		return idx, true
	}
	delete(j.replayBySignature, signature)
	return 0, false
}

func (j *CheckpointJournal) consumeReplayIndexLocked(idx int) {
	if idx < 0 || idx >= len(j.entries) {
		return
	}
	if _, consumed := j.replayConsumed[idx]; consumed {
		return
	}
	j.replayConsumed[idx] = struct{}{}
	if j.replayRemaining > 0 {
		j.replayRemaining--
	}
	entry := j.entries[idx]
	if entry.CallID != "" {
		if mapped, ok := j.replayByCallID[entry.CallID]; ok && mapped == idx {
			delete(j.replayByCallID, entry.CallID)
		}
	}
}

// Len returns the number of entries in the journal.
func (j *CheckpointJournal) Len() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.entries)
}

// Clear resets the journal.
func (j *CheckpointJournal) Clear() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries = nil
	j.byCallID = make(map[string]int)
	j.bySignature = make(map[string]int)
	j.replayByCallID = nil
	j.replayBySignature = nil
	j.replayConsumed = nil
	j.replayRemaining = 0
}

// Entries returns a copy of all journal entries.
func (j *CheckpointJournal) Entries() []ToolCheckpoint {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]ToolCheckpoint, len(j.entries))
	copy(out, j.entries)
	return out
}

// checkpointSignature generates a deterministic signature for a tool call.
func checkpointSignature(toolName string, args map[string]any) string {
	if toolName == "" {
		return ""
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(toolName + "|" + string(encoded)))
	return hex.EncodeToString(sum[:])
}

// ToolCheckpointSignature exposes the canonical digest used by durable app
// recovery validation. Persisted signatures are untrusted input after restart;
// callers must recompute rather than accepting a non-empty stored value.
func ToolCheckpointSignature(toolName string, args map[string]any) string {
	return checkpointSignature(toolName, args)
}
