package chat

import (
	"encoding/json"
	"testing"
	"time"
)

func pendingRecoveryFixture() SerializedPendingRecovery {
	return SerializedPendingRecovery{
		ID:          "recovery-one",
		SessionID:   "session-one",
		Message:     "expanded executable request",
		UserMessage: "original request",
		Kind:        "rate_limit",
		Attempt:     2,
		NotBefore:   time.Unix(1234, 0),
		CreatedAt:   time.Unix(1200, 0),
		State:       PendingRecoveryScheduled,
		Checkpoints: []SerializedToolCheckpoint{{
			CallID:   "write-one",
			ToolName: "write",
			Args:     map[string]any{"file_path": "x.go"},
			ResultV2: &SerializedToolResult{
				Success: true,
				Content: "written",
				Data:    json.RawMessage(`{"bytes":17}`),
				PolicyBlock: &SerializedPolicyBlock{
					Kind: "plan", Reason: "approval declined",
				},
			},
			Signature: "signature-one",
			Timestamp: time.Unix(1220, 0),
		}},
	}
}

func TestSessionPendingRecoveryRoundTripOwnsExactGeneration(t *testing.T) {
	session := NewSession()
	session.SetID("session-one")
	recovery := pendingRecoveryFixture()
	if !session.AddPendingRecovery(recovery, "") {
		t.Fatal("AddPendingRecovery rejected a fresh id")
	}

	// Caller mutation must not alter the session-owned generation.
	recovery.Checkpoints[0].CallID = "caller-mutated"
	recovery.Checkpoints[0].Args["file_path"] = "wrong.go"
	recovery.Checkpoints[0].ResultV2.Data[0] = '['
	recovery.Checkpoints[0].ResultV2.PolicyBlock.Reason = "caller-mutated"

	encoded, err := json.Marshal(session.GetState())
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	var state SessionState
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	restored := NewSession()
	if err := restored.RestoreFromState(&state); err != nil {
		t.Fatalf("RestoreFromState: %v", err)
	}

	got := restored.GetPendingRecoveries()
	if len(got) != 1 {
		t.Fatalf("pending recoveries = %d, want 1", len(got))
	}
	cp := got[0].Checkpoints[0]
	if cp.CallID != "write-one" || cp.Args["file_path"] != "x.go" {
		t.Fatalf("checkpoint lineage changed: %+v", cp)
	}
	if cp.ResultV2 == nil || !cp.ResultV2.Success || cp.ResultV2.Content != "written" || string(cp.ResultV2.Data) != `{"bytes":17}` ||
		cp.ResultV2.PolicyBlock == nil || cp.ResultV2.PolicyBlock.Reason != "approval declined" {
		t.Fatalf("ResultV2 was not preserved: %+v", cp.ResultV2)
	}

	// Returned snapshots must also be ownership-safe.
	got[0].Checkpoints[0].Args["file_path"] = "snapshot-mutated.go"
	got[0].Checkpoints[0].ResultV2.Data[0] = '['
	got[0].Checkpoints[0].ResultV2.PolicyBlock.Reason = "snapshot-mutated"
	again := restored.GetPendingRecoveries()[0].Checkpoints[0]
	if again.Args["file_path"] != "x.go" || string(again.ResultV2.Data) != `{"bytes":17}` ||
		again.ResultV2.PolicyBlock == nil || again.ResultV2.PolicyBlock.Reason != "approval declined" {
		t.Fatalf("snapshot mutation escaped into session: %+v", again)
	}
}

func TestSessionStateUnmarshalPreservesLargeRecoveryArgumentAndSignature(t *testing.T) {
	recovery := pendingRecoveryFixture()
	recovery.Checkpoints[0].Args["transaction_id"] = int64(9007199254740993)
	recovery.GenerationSignature = PendingRecoveryGenerationSignature(recovery)
	state := &SessionState{
		ID:                "session-one",
		PendingRecoveries: []SerializedPendingRecovery{recovery},
	}

	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	var decoded SessionState
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if len(decoded.PendingRecoveries) != 1 || len(decoded.PendingRecoveries[0].Checkpoints) != 1 {
		t.Fatalf("decoded recovery = %+v", decoded.PendingRecoveries)
	}
	value, ok := decoded.PendingRecoveries[0].Checkpoints[0].Args["transaction_id"].(json.Number)
	if !ok || value.String() != "9007199254740993" {
		t.Fatalf("transaction_id = %#v, want exact json.Number", decoded.PendingRecoveries[0].Checkpoints[0].Args["transaction_id"])
	}
	got := decoded.PendingRecoveries[0]
	if got.GenerationSignature != PendingRecoveryGenerationSignature(got) {
		t.Fatal("clean JSON round-trip invalidated the durable recovery generation signature")
	}
}

func TestSessionPendingRecoveryStateMachineAndClear(t *testing.T) {
	session := NewSession()
	session.SetID("session-one")
	session.SetToolCheckpoints(pendingRecoveryFixture().Checkpoints)
	if !session.AddPendingRecovery(pendingRecoveryFixture(), "") {
		t.Fatal("AddPendingRecovery failed")
	}

	claimed, ok := session.TransitionPendingRecovery(
		"recovery-one", "session-one", PendingRecoveryScheduled, PendingRecoveryClaimed)
	if !ok || claimed.State != PendingRecoveryClaimed {
		t.Fatalf("scheduled -> claimed = %+v ok=%v", claimed, ok)
	}
	if _, ok := session.TransitionPendingRecovery(
		"recovery-one", "session-one", PendingRecoveryScheduled, PendingRecoveryClaimed); ok {
		t.Fatal("same generation was claimed twice")
	}

	session.Clear()
	if got := session.GetPendingRecoveries(); len(got) != 0 {
		t.Fatalf("/clear retained pending recovery: %+v", got)
	}
	if got := session.GetToolCheckpoints(); len(got) != 0 {
		t.Fatalf("/clear retained tool checkpoints: %+v", got)
	}
}

func TestRestoreCheckpointCancelsPendingRecoveryLineage(t *testing.T) {
	session := NewSession()
	session.SetID("session-one")
	session.AddUserMessage("before checkpoint")
	session.SaveCheckpoint("safe")
	session.AddUserMessage("after checkpoint")
	session.SetToolCheckpoints(pendingRecoveryFixture().Checkpoints)
	if !session.AddPendingRecovery(pendingRecoveryFixture(), "") {
		t.Fatal("AddPendingRecovery failed")
	}

	if !session.RestoreCheckpoint("safe") {
		t.Fatal("RestoreCheckpoint failed")
	}
	if got := session.GetPendingRecoveries(); len(got) != 0 {
		t.Fatalf("checkpoint rollback retained pending recovery: %+v", got)
	}
	if got := session.GetToolCheckpoints(); len(got) != 0 {
		t.Fatalf("checkpoint rollback retained tool replay ledger: %+v", got)
	}
}
