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
	mu          sync.Mutex
	entries     []ToolCheckpoint
	byCallID    map[string]int // callID -> index in entries
	bySignature map[string]int // signature -> index in entries
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
	content := resultContent
	if content == "" {
		content = "[restored from session]"
	}
	j.recordLocked(callID, toolName, args, ToolResult{Content: content}, signature, ts)
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

	// Try call ID first (exact match)
	if call.ID != "" {
		if idx, ok := j.byCallID[call.ID]; ok {
			logging.Debug("checkpoint hit by call_id", "tool", call.Name, "id", call.ID)
			return j.entries[idx].Result, "checkpoint_call_id", true
		}
	}

	// Try signature match (same tool + same args)
	sig := checkpointSignature(call.Name, call.Args)
	if sig != "" {
		if idx, ok := j.bySignature[sig]; ok {
			logging.Debug("checkpoint hit by signature", "tool", call.Name)
			return j.entries[idx].Result, "checkpoint_signature", true
		}
	}

	return ToolResult{}, "", false
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
