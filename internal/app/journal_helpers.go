package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"gokin/internal/chat"
	"gokin/internal/logging"
	"gokin/internal/tools"
)

func (a *App) journalEvent(event string, details map[string]any) {
	if a == nil || a.journal == nil {
		return
	}
	a.journal.Append(event, details)
}

func (a *App) saveRecoverySnapshot() {
	if a == nil || a.journal == nil || a.session == nil {
		return
	}

	pending := a.pendingSnapshot()
	snap := RecoverySnapshot{
		SessionID:       a.session.ID,
		PendingMessages: pending,
		HistoryLen:      a.session.MessageCount(),
	}
	// Legacy single-message field: keep the head so older tooling reading
	// pending_message still sees the next-up message.
	if len(pending) > 0 {
		snap.PendingMessage = pending[0]
	}

	a.mu.Lock()
	snap.Processing = a.processing
	a.mu.Unlock()

	if a.planManager != nil {
		if p := a.planManager.GetCurrentPlan(); p != nil {
			snap.PlanID = p.ID
		}
		snap.CurrentStepID = a.planManager.GetCurrentStepID()
	}
	a.journal.SaveRecovery(snap)
}

func (a *App) journalSessionArchive(archived int, reason string) {
	a.sessionArchiveMu.Lock()
	a.sessionArchivedMessages += archived
	a.sessionArchiveOperations++
	a.lastSessionArchive = time.Now()
	a.sessionArchiveMu.Unlock()
	a.journalEvent("session_archive", map[string]any{
		"archived_messages": archived,
		"reason":            reason,
	})
}

// restoreToolCheckpoints loads persisted tool checkpoints from the session back
// into the executor's checkpoint journal after session restore.
func (a *App) restoreToolCheckpoints() {
	if a == nil || a.executor == nil || a.session == nil {
		return
	}
	journal := a.executor.GetCheckpointJournal()
	if journal == nil {
		return
	}
	journal.Clear()
	for _, checkpoint := range deserializeToolCheckpoints(a.session.GetToolCheckpoints()) {
		journal.RecordSerializedResult(
			checkpoint.CallID,
			checkpoint.ToolName,
			checkpoint.Args,
			checkpoint.Result,
			checkpoint.Signature,
			checkpoint.Timestamp,
		)
	}
}

func deserializeToolCheckpoints(checkpoints []chat.SerializedToolCheckpoint) []tools.ToolCheckpoint {
	if len(checkpoints) == 0 {
		return nil
	}
	out := make([]tools.ToolCheckpoint, 0, len(checkpoints))
	for _, cp := range checkpoints {
		var result tools.ToolResult
		if cp.ResultV2 == nil {
			content := cp.Result
			if content == "" {
				content = "[restored from session]"
			}
			// Legacy checkpoints omitted outcome metadata. Treat the operation as
			// completed: synthesizing a failure would invite a duplicate write.
			result = tools.ToolResult{Success: true, Content: content}
		} else {
			result = tools.ToolResult{
				Success: cp.ResultV2.Success,
				Content: cp.ResultV2.Content,
				Error:   cp.ResultV2.Error,
			}
			if cp.ResultV2.PolicyBlock != nil {
				result.PolicyBlock = &tools.PolicyBlock{
					Kind:   tools.PolicyBlockKind(cp.ResultV2.PolicyBlock.Kind),
					Reason: cp.ResultV2.PolicyBlock.Reason,
				}
			}
			if len(cp.ResultV2.Data) > 0 {
				data, err := decodeCheckpointResultData(cp.ResultV2.Data)
				if err != nil {
					logging.Warn("ignored malformed persisted tool checkpoint data",
						"tool", cp.ToolName, "call_id", cp.CallID, "error", err)
				} else if data == nil && (cp.ResultV2.DataPresent || len(cp.ResultV2.Data) > 0) {
					// Preserve model-visible `data:null` as present. A nil interface
					// would make ToolResult.ToMap omit the data key entirely.
					result.Data = json.RawMessage("null")
				} else {
					result.Data = data
				}
			}
		}
		out = append(out, tools.ToolCheckpoint{
			CallID: cp.CallID, ToolName: cp.ToolName, Args: cp.Args,
			Result: result, Signature: cp.Signature, Timestamp: cp.Timestamp,
		})
	}
	return out
}

func decodeCheckpointResultData(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	// float64 decoding silently rounds identifiers above 2^53. json.Number
	// preserves the exact decimal token so replay and re-marshalling remain
	// byte-value faithful for transaction IDs, byte counts, and custom MCP data.
	decoder.UseNumber()
	var data any
	if err := decoder.Decode(&data); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return data, nil
}

// syncToolCheckpoints snapshots the executor's checkpoint journal into the session
// for cross-session persistence. Called before session saves.
func (a *App) syncToolCheckpoints() {
	if a == nil || a.executor == nil || a.session == nil {
		return
	}
	journal := a.executor.GetCheckpointJournal()
	if journal == nil {
		return
	}
	a.session.SetToolCheckpoints(serializeToolCheckpoints(journal.Entries()))
}

func serializeToolCheckpoints(entries []tools.ToolCheckpoint) []chat.SerializedToolCheckpoint {
	serialized, _ := serializeToolCheckpointsWithPolicy(entries, false)
	return serialized
}

// serializeToolCheckpointsExact is used for executable durable recovery state.
// Unlike the ordinary session journal, it must never silently discard an
// unencodable result payload: replaying a lossy outcome can make the model
// repeat a mutation that already happened.
func serializeToolCheckpointsExact(entries []tools.ToolCheckpoint) ([]chat.SerializedToolCheckpoint, error) {
	return serializeToolCheckpointsWithPolicy(entries, true)
}

func serializeToolCheckpointsWithPolicy(entries []tools.ToolCheckpoint, exact bool) ([]chat.SerializedToolCheckpoint, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	serialized := make([]chat.SerializedToolCheckpoint, len(entries))
	for i, e := range entries {
		signature := e.Signature
		if signature == "" {
			signature = tools.ToolCheckpointSignature(e.ToolName, e.Args)
		}
		resultV2 := &chat.SerializedToolResult{
			Success: e.Result.Success,
			Content: e.Result.Content,
			Error:   e.Result.Error,
		}
		if e.Result.PolicyBlock != nil {
			resultV2.PolicyBlock = &chat.SerializedPolicyBlock{
				Kind:   string(e.Result.PolicyBlock.Kind),
				Reason: e.Result.PolicyBlock.Reason,
			}
		}
		if e.Result.Data != nil {
			resultV2.DataPresent = true
			if data, err := json.Marshal(e.Result.Data); err != nil {
				if exact {
					return nil, fmt.Errorf("serialize %s checkpoint %q result data: %w", e.ToolName, e.CallID, err)
				}
				logging.Warn("tool checkpoint data is not JSON-serializable; persisting text outcome only",
					"tool", e.ToolName, "call_id", e.CallID, "error", err)
			} else {
				resultV2.Data = data
			}
		}
		serialized[i] = chat.SerializedToolCheckpoint{
			CallID:    e.CallID,
			ToolName:  e.ToolName,
			Args:      e.Args,
			Result:    e.Result.Content,
			ResultV2:  resultV2,
			Signature: signature,
			Timestamp: e.Timestamp,
		}
		serialized[i].OutcomeSignature = serializedToolOutcomeSignature(serialized[i])
	}
	return serialized, nil
}

func serializedToolOutcomeSignature(checkpoint chat.SerializedToolCheckpoint) string {
	payload := struct {
		Version   string                     `json:"version"`
		CallID    string                     `json:"call_id"`
		ToolName  string                     `json:"tool_name"`
		Args      map[string]any             `json:"args,omitempty"`
		Result    string                     `json:"result,omitempty"`
		ResultV2  *chat.SerializedToolResult `json:"result_v2"`
		Signature string                     `json:"signature"`
	}{
		Version: "gokin-tool-outcome-v1", CallID: checkpoint.CallID,
		ToolName: checkpoint.ToolName, Args: checkpoint.Args,
		Result: checkpoint.Result, ResultV2: checkpoint.ResultV2,
		Signature: checkpoint.Signature,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// sideEffectRecoverySnapshot captures the exact completed-call generation for
// a delayed automatic retry. It is intentionally independent of session-save
// timing: the retry timer can fire before the asynchronous persistence worker.
func (a *App) sideEffectRecoverySnapshot() []tools.ToolCheckpoint {
	if a == nil || a.executor == nil {
		return nil
	}
	journal := a.executor.GetCheckpointJournal()
	if journal == nil {
		return nil
	}
	return journal.Entries()
}

func previewForJournal(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= 160 {
		return s
	}
	return string(runes[:157]) + "..."
}
