package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/commands"
	"gokin/internal/config"
	"gokin/internal/tools"
	"gokin/internal/ui"

	"google.golang.org/genai"
)

func newPendingRecoveryTestApp(t *testing.T, session *chat.Session) *App {
	t.Helper()
	manager, err := chat.NewSessionManager(session, chat.DefaultSessionManagerConfig())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &App{
		ctx:            ctx,
		session:        session,
		sessionManager: manager,
		executor:       tools.NewExecutor(tools.NewRegistry(), nil, time.Second),
	}
}

func validSerializedRecovery(id, sessionID, state string) chat.SerializedPendingRecovery {
	args := map[string]any{"file_path": "x.go"}
	recovery := chat.SerializedPendingRecovery{
		ID:                 id,
		SessionID:          sessionID,
		Message:            "original request",
		Kind:               "auto_resume",
		Attempt:            1,
		AutoResumeAttempts: 1,
		State:              state,
		Checkpoints: []chat.SerializedToolCheckpoint{{
			CallID:    "write-one",
			ToolName:  "write",
			Args:      args,
			ResultV2:  &chat.SerializedToolResult{Success: true, Content: "written"},
			Signature: tools.ToolCheckpointSignature("write", args),
		}},
	}
	recovery.Checkpoints[0].OutcomeSignature = serializedToolOutcomeSignature(recovery.Checkpoints[0])
	recovery.GenerationSignature = chat.PendingRecoveryGenerationSignature(recovery)
	return recovery
}

func TestPendingRecoveryPersistsExactRequestAndResultAcrossRestart(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	workDir := t.TempDir()
	session := chat.NewSession()
	session.SetID("recovery-session")
	session.SetWorkDir(workDir)
	session.AddUserMessage("original request")
	application := newPendingRecoveryTestApp(t, session)

	call := &genai.FunctionCall{
		ID:   "write-original",
		Name: "write",
		Args: map[string]any{"file_path": "x.go", "content": "package x"},
	}
	wantResult := tools.ToolResult{
		Success: true,
		Content: "written once",
		Data:    map[string]any{"bytes": float64(9)},
	}
	application.executor.GetCheckpointJournal().Record(call, wantResult)
	generation := application.sideEffectRecoverySnapshot()
	recovery, err := application.persistPendingRecovery(
		"rate_limit",
		"expanded executable request",
		"original request",
		generation,
		1,
		time.Hour,
		"",
		"recovery-session",
		application.recoveryEpoch.Load(),
	)
	if err != nil {
		t.Fatalf("persistPendingRecovery: %v", err)
	}
	if recovery.State != chat.PendingRecoveryScheduled || len(recovery.Checkpoints) != 1 {
		t.Fatalf("persisted recovery = %+v", recovery)
	}

	history, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}
	state, err := history.LoadFull("recovery-session")
	if err != nil {
		t.Fatalf("LoadFull: %v", err)
	}
	restartedSession := chat.NewSession()
	if err := restartedSession.RestoreFromState(state); err != nil {
		t.Fatalf("RestoreFromState: %v", err)
	}
	restarted := newPendingRecoveryTestApp(t, restartedSession)

	// A different prompt is rejected in headless mode and, critically, does
	// not receive or consume the saved checkpoint generation.
	if _, err := restarted.pendingRecoveryForHeadlessPrompt("different new task"); err == nil {
		t.Fatal("unrelated prompt was allowed to bypass a scheduled recovery")
	}
	if got := restarted.executor.GetCheckpointJournal().Len(); got != 0 {
		t.Fatalf("unrelated prompt touched recovery journal: len=%d", got)
	}

	matched, err := restarted.pendingRecoveryForHeadlessPrompt("original request")
	if err != nil || matched == nil || matched.ID != recovery.ID {
		t.Fatalf("exact prompt match = %+v, err=%v", matched, err)
	}
	claimEpoch := restarted.recoveryEpoch.Load()
	claimed, checkpoints, err := restarted.claimPendingRecovery(matched.ID, matched.SessionID, claimEpoch)
	if err != nil {
		t.Fatalf("claimPendingRecovery: %v", err)
	}
	if claimed.State != chat.PendingRecoveryClaimed || len(checkpoints) != 1 {
		t.Fatalf("claim = %+v checkpoints=%+v", claimed, checkpoints)
	}
	if _, _, err := restarted.claimPendingRecovery(matched.ID, matched.SessionID, claimEpoch); !errors.Is(err, errPendingRecoveryUnavailable) {
		t.Fatalf("second claim error = %v, want unavailable", err)
	}

	// A provider may regenerate a call ID after restart. Signature replay must
	// still return the exact successful ResultV2 instead of executing again.
	restarted.executor.PrepareSideEffectRecovery(checkpoints)
	repeated := &genai.FunctionCall{
		ID:   "write-regenerated",
		Name: call.Name,
		Args: call.Args,
	}
	got, matchType, ok := restarted.executor.GetCheckpointJournal().ConsumeReplay(repeated)
	if !ok || matchType != "checkpoint_signature" {
		t.Fatalf("replay miss: ok=%v match=%q", ok, matchType)
	}
	if !got.Success || got.Content != wantResult.Content {
		t.Fatalf("replayed result = %+v", got)
	}
	data, ok := got.Data.(map[string]any)
	bytes, numberOK := data["bytes"].(json.Number)
	if !ok || !numberOK || bytes.String() != "9" {
		t.Fatalf("replayed ResultV2.Data = %#v", got.Data)
	}

	restarted.clearPendingRecovery(claimed.ID, claimed.SessionID, "test_success")
	if got := restartedSession.GetPendingRecoveries(); len(got) != 0 {
		t.Fatalf("successful recovery was not cleared: %+v", got)
	}
}

func TestPendingRecoveryPersistsOrderedDuplicateCheckpointSignatures(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("duplicate-checkpoint-recovery")
	session.SetWorkDir(t.TempDir())
	application := newPendingRecoveryTestApp(t, session)
	args := map[string]any{"command": "go test ./..."}
	signature := tools.ToolCheckpointSignature("bash", args)

	recovery, err := application.persistPendingRecovery(
		"auto_resume", "repeat the verification", "repeat the verification",
		[]tools.ToolCheckpoint{
			{CallID: "bash-before", ToolName: "bash", Args: args,
				Result: tools.NewSuccessResult("failed before edit"), Signature: signature},
			{CallID: "bash-after", ToolName: "bash", Args: args,
				Result: tools.NewSuccessResult("passed after edit"), Signature: signature},
		},
		1, time.Hour, "", session.GetID(), application.recoveryEpoch.Load(),
	)
	if err != nil {
		t.Fatalf("persist duplicate checkpoint generation: %v", err)
	}
	if len(recovery.Checkpoints) != 2 ||
		recovery.Checkpoints[0].Signature != recovery.Checkpoints[1].Signature {
		t.Fatalf("persisted generation = %+v", recovery.Checkpoints)
	}

	history, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}
	state, err := history.LoadFull(session.GetID())
	if err != nil {
		t.Fatalf("LoadFull: %v", err)
	}
	if len(state.PendingRecoveries) != 1 {
		t.Fatalf("pending recoveries = %+v", state.PendingRecoveries)
	}
	persisted := state.PendingRecoveries[0]
	if err := validatePendingRecovery(persisted); err != nil {
		t.Fatalf("validate duplicate checkpoint generation: %v", err)
	}

	journal := tools.NewCheckpointJournal()
	for _, checkpoint := range deserializeToolCheckpoints(persisted.Checkpoints) {
		journal.RecordSerializedResult(
			checkpoint.CallID, checkpoint.ToolName, checkpoint.Args,
			checkpoint.Result, checkpoint.Signature, checkpoint.Timestamp)
	}
	journal.BeginReplay()
	for i, want := range []string{"failed before edit", "passed after edit"} {
		got, _, ok := journal.ConsumeReplay(&genai.FunctionCall{
			ID: "regenerated-" + string(rune('1'+i)), Name: "bash", Args: args,
		})
		if !ok || got.Content != want {
			t.Fatalf("durable replay %d = %+v ok=%v, want %q", i, got, ok, want)
		}
	}
	if got, reason, ok := journal.ConsumeReplay(&genai.FunctionCall{
		ID: "regenerated-3", Name: "bash", Args: args,
	}); ok {
		t.Fatalf("durable checkpoint reused after queue exhaustion: %+v reason=%q", got, reason)
	}
}

func TestClaimedRecoveryIsNeverSelectedForAutomaticHeadlessReplay(t *testing.T) {
	session := chat.NewSession()
	session.SetID("claimed-session")
	session.AddPendingRecovery(validSerializedRecovery(
		"claimed-one", "claimed-session", chat.PendingRecoveryClaimed), "")
	application := &App{session: session}

	if recovery, err := application.pendingRecoveryForHeadlessPrompt("original"); err == nil || recovery != nil {
		t.Fatalf("claimed recovery was selected: recovery=%+v err=%v", recovery, err)
	}
}

func TestInteractiveRepeatClaimsExactRecoveryInsteadOfRunningFresh(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("interactive-recovery")
	session.SetWorkDir(t.TempDir())
	session.AddUserMessage("original request")
	application := newPendingRecoveryTestApp(t, session)
	checkpoint := tools.ToolCheckpoint{
		CallID: "write-once", ToolName: "write",
		Args:   map[string]any{"file_path": "x.go"},
		Result: tools.NewSuccessResult("written"),
	}
	recovery, err := application.persistPendingRecovery(
		"auto_resume", "expanded request", "original request",
		[]tools.ToolCheckpoint{checkpoint}, 1, time.Hour, "",
		"interactive-recovery", application.recoveryEpoch.Load())
	if err != nil {
		t.Fatalf("persistPendingRecovery: %v", err)
	}

	if application.tryResumeScheduledRecovery("different request") {
		t.Fatal("unrelated interactive prompt matched the recovery")
	}
	if got := session.GetPendingRecoveries(); len(got) != 1 || got[0].State != chat.PendingRecoveryScheduled {
		t.Fatalf("unrelated prompt changed recovery: %+v", got)
	}

	// Keep another foreground operation busy so the claimed request lands in
	// the queue and can be inspected without constructing a model client.
	application.processing = true
	if !application.tryResumeScheduledRecovery("original request") {
		t.Fatal("exact interactive repetition did not claim recovery")
	}
	request, _, ok := application.dequeuePendingRequest()
	if !ok || request.message != "expanded request" || request.recoveryID != recovery.ID || request.recoverySessionID != "interactive-recovery" {
		t.Fatalf("queued recovery = %+v ok=%v", request, ok)
	}
	if len(request.recoveryCheckpoints) != 1 || request.recoveryCheckpoints[0].CallID != "write-once" {
		t.Fatalf("queued generation = %+v", request.recoveryCheckpoints)
	}
	if got := session.GetPendingRecoveries(); len(got) != 1 || got[0].State != chat.PendingRecoveryClaimed {
		t.Fatalf("recovery was not durably claimed: %+v", got)
	}
}

func TestPendingQueueDefersRecoveryFromPreviousSessionWithoutDiscardingIt(t *testing.T) {
	application := &App{}
	checkpoints := []tools.ToolCheckpoint{{CallID: "old-write", ToolName: "write"}}
	application.enqueueRecoveryPending("old retry", "old raw request", "old-recovery", "old-session", 0, checkpoints)
	application.enqueuePending("new-session message")

	request, remaining, ok, stale := application.dequeuePendingRequestForSession("new-session", 0)
	if !ok || request.message != "new-session message" || remaining != 1 || stale != 1 {
		t.Fatalf("dequeue = %+v remaining=%d ok=%v stale=%d", request, remaining, ok, stale)
	}
	request, remaining, ok, stale = application.dequeuePendingRequestForSession("old-session", 0)
	if !ok || request.recoveryID != "old-recovery" || remaining != 0 || stale != 0 {
		t.Fatalf("switch-back dequeue = %+v remaining=%d ok=%v stale=%d", request, remaining, ok, stale)
	}
}

func TestCancelledRecoveryTurnKeepsClaimedMarker(t *testing.T) {
	session := chat.NewSession()
	session.SetID("cancelled-recovery")
	recovery := validSerializedRecovery(
		"claimed-cancel", "cancelled-recovery", chat.PendingRecoveryClaimed)
	session.AddPendingRecovery(recovery, "")
	application := &App{
		session:  session,
		executor: tools.NewExecutor(tools.NewRegistry(), nil, time.Second),
	}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = withPersistedSideEffectRecovery(ctx, recovery.ID, recovery.SessionID,
		deserializeToolCheckpoints(recovery.Checkpoints))
	cancel()

	application.processMessageWithMemoryQuery(ctx, recovery.Message, recovery.UserMessage)
	got := session.GetPendingRecoveries()
	if len(got) != 1 || got[0].ID != recovery.ID || got[0].State != chat.PendingRecoveryClaimed {
		t.Fatalf("cancelled recovery lost claimed marker: %+v", got)
	}
}

func TestClearConversationBoundaryIsDurableAndInvalidatesQueuedRecovery(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("clear-recovery")
	session.SetWorkDir(t.TempDir())
	session.AddUserMessage("old task")
	recovery := validSerializedRecovery(
		"scheduled-clear", "clear-recovery", chat.PendingRecoveryScheduled)
	session.AddPendingRecovery(recovery, "")
	session.SetToolCheckpoints(recovery.Checkpoints)
	application := newPendingRecoveryTestApp(t, session)
	if err := application.sessionManager.Save(); err != nil {
		t.Fatalf("save pre-clear state: %v", err)
	}
	application.enqueueRecoveryPending(
		recovery.Message, recovery.UserMessage, recovery.ID, recovery.SessionID,
		application.recoveryEpoch.Load(), deserializeToolCheckpoints(recovery.Checkpoints))
	application.enqueuePending("keep ordinary type-ahead")

	dropped, err := application.clearConversationPersistenceBoundary()
	if err != nil {
		t.Fatalf("clearConversationPersistenceBoundary: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("dropped recoveries = %d, want 1", dropped)
	}
	message, _, ok := application.dequeuePending()
	if !ok || message != "keep ordinary type-ahead" {
		t.Fatalf("ordinary queue item was not preserved: %q ok=%v", message, ok)
	}

	history, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}
	state, err := history.LoadFull("clear-recovery")
	if err != nil {
		t.Fatalf("LoadFull after clear: %v", err)
	}
	if len(state.History) != 0 || len(state.PendingRecoveries) != 0 || len(state.ToolCheckpoints) != 0 {
		t.Fatalf("durable clear retained executable state: %+v", state)
	}
}

func TestRecoveryEpochRejectsTimerAfterClear(t *testing.T) {
	session := chat.NewSession()
	session.SetID("same-session")
	application := &App{session: session, processing: true}
	oldEpoch := application.recoveryEpoch.Load()
	application.recoveryEpoch.Add(1) // /clear keeps the same session ID.

	application.handleRecoveryResubmit(
		"old request", "old request", "", "same-session", oldEpoch,
		[]tools.ToolCheckpoint{{CallID: "old-write", ToolName: "write"}})
	if got := application.pendingCount(); got != 0 {
		t.Fatalf("old-epoch timer queued after clear: %d", got)
	}
}

func TestMalformedRecoverySignatureAndDuplicateBatchFailClosed(t *testing.T) {
	recovery := validSerializedRecovery(
		"duplicate-id", "corrupt-session", chat.PendingRecoveryScheduled)
	recovery.Checkpoints[0].Signature = "forged"
	if err := validatePendingRecovery(recovery); err == nil {
		t.Fatal("forged checkpoint signature was accepted")
	}

	recovery = validSerializedRecovery(
		"duplicate-id", "corrupt-session", chat.PendingRecoveryScheduled)
	if _, err := validatePendingRecoveryBatch(
		"corrupt-session", []chat.SerializedPendingRecovery{recovery, recovery}); err == nil {
		t.Fatal("duplicate recovery IDs were accepted as an auto-run batch")
	}
	overlap := validSerializedRecovery(
		"different-id", "corrupt-session", chat.PendingRecoveryScheduled)
	if _, err := validatePendingRecoveryBatch(
		"corrupt-session", []chat.SerializedPendingRecovery{recovery, overlap}); err == nil {
		t.Fatal("overlapping recovery prompt identities were accepted as an auto-run batch")
	}

	recovery.Checkpoints[0].ResultV2.Data = []byte("{not-json")
	if err := validatePendingRecovery(recovery); err == nil {
		t.Fatal("malformed ResultV2 data was accepted")
	}

	recovery.Checkpoints[0].ResultV2.Data = []byte("1 2")
	if err := validatePendingRecovery(recovery); err == nil {
		t.Fatal("multiple ResultV2 JSON values were accepted")
	}
}

func TestPendingRecoveryOutcomeSignatureRejectsTamperedResult(t *testing.T) {
	recovery := validSerializedRecovery(
		"tampered-outcome", "integrity-session", chat.PendingRecoveryScheduled)
	recovery.Checkpoints[0].ResultV2.Content = "forged successful outcome"
	// An attacker/corruption repair that updates only the outer generation hash
	// must not make a stale per-call outcome signature executable.
	recovery.GenerationSignature = chat.PendingRecoveryGenerationSignature(recovery)

	err := validatePendingRecovery(recovery)
	if err == nil || !strings.Contains(err.Error(), "outcome signature") {
		t.Fatalf("tampered ResultV2 error = %v, want stale outcome-signature rejection", err)
	}
}

func TestPendingRecoveryGenerationSignatureRejectsDeletedCheckpoint(t *testing.T) {
	recovery := validSerializedRecovery(
		"deleted-checkpoint", "integrity-session", chat.PendingRecoveryScheduled)
	second := recovery.Checkpoints[0]
	second.CallID = "write-two"
	second.OutcomeSignature = serializedToolOutcomeSignature(second)
	recovery.Checkpoints = append(recovery.Checkpoints, second)
	recovery.GenerationSignature = chat.PendingRecoveryGenerationSignature(recovery)

	// Keep every surviving checkpoint internally valid but delete one from the
	// signed ordered generation. Per-entry hashes alone cannot detect this.
	recovery.Checkpoints = recovery.Checkpoints[:1]
	err := validatePendingRecovery(recovery)
	if err == nil || !strings.Contains(err.Error(), "generation signature") {
		t.Fatalf("deleted checkpoint error = %v, want generation-signature rejection", err)
	}
}

func TestCheckpointDataPreservesLargeJSONIntegersExactly(t *testing.T) {
	recovery := validSerializedRecovery(
		"large-integer", "number-session", chat.PendingRecoveryScheduled)
	recovery.Checkpoints[0].ResultV2.Data = json.RawMessage(
		`{"nested":{"transaction_id":9007199254740993}}`)
	recovery.Checkpoints[0].ResultV2.DataPresent = true
	recovery.Checkpoints[0].OutcomeSignature = serializedToolOutcomeSignature(recovery.Checkpoints[0])
	recovery.GenerationSignature = chat.PendingRecoveryGenerationSignature(recovery)
	if err := validatePendingRecovery(recovery); err != nil {
		t.Fatalf("validatePendingRecovery: %v", err)
	}
	restored := deserializeToolCheckpoints(recovery.Checkpoints)
	if len(restored) != 1 {
		t.Fatalf("restored checkpoints = %+v", restored)
	}
	outer, ok := restored[0].Result.Data.(map[string]any)
	if !ok {
		t.Fatalf("restored data = %#v", restored[0].Result.Data)
	}
	nested, ok := outer["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested data = %#v", outer["nested"])
	}
	number, ok := nested["transaction_id"].(json.Number)
	if !ok || number.String() != "9007199254740993" {
		t.Fatalf("large integer was rounded: %#v", nested["transaction_id"])
	}
	reencoded, err := json.Marshal(restored[0].Result.Data)
	if err != nil {
		t.Fatalf("re-marshal restored data: %v", err)
	}
	if string(reencoded) != `{"nested":{"transaction_id":9007199254740993}}` {
		t.Fatalf("re-encoded data = %s", reencoded)
	}
}

func TestExactRecoverySerializationPreservesPolicyBlock(t *testing.T) {
	args := map[string]any{"command": "dangerous"}
	entries := []tools.ToolCheckpoint{{
		CallID:   "blocked-bash",
		ToolName: "bash",
		Args:     args,
		Result: tools.WithPolicyBlock(
			tools.NewErrorResult("permission denied"),
			tools.PolicyBlockPermission,
			"operator denied the command",
		),
		Signature: tools.ToolCheckpointSignature("bash", args),
	}}

	serialized, err := serializeToolCheckpointsExact(entries)
	if err != nil {
		t.Fatalf("serializeToolCheckpointsExact: %v", err)
	}
	if len(serialized) != 1 || serialized[0].ResultV2 == nil ||
		serialized[0].ResultV2.PolicyBlock == nil {
		t.Fatalf("serialized policy block missing: %+v", serialized)
	}
	policyRecovery := chat.SerializedPendingRecovery{
		ID: "policy-recovery", SessionID: "policy-session", Message: "retry",
		Kind: "rate_limit", Attempt: 1, RateLimitAttempts: 1, State: chat.PendingRecoveryScheduled,
		Checkpoints: serialized,
	}
	policyRecovery.GenerationSignature = chat.PendingRecoveryGenerationSignature(policyRecovery)
	if err := validatePendingRecovery(policyRecovery); err != nil {
		t.Fatalf("validate policy recovery: %v", err)
	}
	restored := deserializeToolCheckpoints(serialized)
	if len(restored) != 1 || restored[0].Result.PolicyBlock == nil ||
		restored[0].Result.PolicyBlock.Kind != tools.PolicyBlockPermission ||
		restored[0].Result.PolicyBlock.Reason != "operator denied the command" {
		t.Fatalf("restored policy block = %+v", restored)
	}
}

func TestUnsafeRecoveryGenerationNeverFallsBackToDurableOrInProcessState(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("unsafe-generation")
	session.SetWorkDir(t.TempDir())
	application := newPendingRecoveryTestApp(t, session)
	args := map[string]any{"file_path": "x.go"}
	checkpoint := tools.ToolCheckpoint{
		CallID: "write-unsafe", ToolName: "write", Args: args,
		Signature: tools.ToolCheckpointSignature("write", args),
		Result:    tools.ToolResult{Success: true, Content: "written", Data: func() {}},
	}

	_, err := application.persistPendingRecovery(
		"rate_limit", "retry", "retry", []tools.ToolCheckpoint{checkpoint},
		1, time.Second, "", session.GetID(), application.recoveryEpoch.Load())
	if !errors.Is(err, errUnsafeRecoveryGeneration) {
		t.Fatalf("persist error = %v, want unsafe generation", err)
	}
	if got := session.GetPendingRecoveries(); len(got) != 0 {
		t.Fatalf("unsafe generation entered durable state: %+v", got)
	}
}

func TestClaimRecoveryRestoresPersistedRetryBudget(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("budget-session")
	session.SetWorkDir(t.TempDir())
	recovery := validSerializedRecovery(
		"budget-recovery", session.GetID(), chat.PendingRecoveryScheduled)
	recovery.Kind = "rate_limit"
	recovery.Attempt = maxAutoRateLimitRetries
	recovery.RateLimitAttempts = maxAutoRateLimitRetries
	recovery.GenerationSignature = chat.PendingRecoveryGenerationSignature(recovery)
	session.AddPendingRecovery(recovery, "")
	application := newPendingRecoveryTestApp(t, session)

	if _, _, err := application.claimPendingRecovery(
		recovery.ID, recovery.SessionID, application.recoveryEpoch.Load()); err != nil {
		t.Fatalf("claimPendingRecovery: %v", err)
	}
	if attempt, _, ok := application.scheduleRateLimitAutoRetry(recovery.Message); ok || attempt != maxAutoRateLimitRetries {
		t.Fatalf("restart reset retry budget: attempt=%d scheduled=%v", attempt, ok)
	}
}

func TestClaimRecoveryFailsClosedForEpochAndOtherClaimedEntry(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("claim-guards")
	session.SetWorkDir(t.TempDir())
	claimed := validSerializedRecovery(
		"already-claimed", session.GetID(), chat.PendingRecoveryClaimed)
	scheduled := validSerializedRecovery(
		"still-scheduled", session.GetID(), chat.PendingRecoveryScheduled)
	session.AddPendingRecovery(claimed, "")
	session.AddPendingRecovery(scheduled, "")
	application := newPendingRecoveryTestApp(t, session)
	epoch := application.recoveryEpoch.Load()

	if _, _, err := application.claimPendingRecovery(
		scheduled.ID, scheduled.SessionID, epoch); !errors.Is(err, errRecoveryBlockedByClaimed) {
		t.Fatalf("claim with ambiguous entry error = %v", err)
	}
	if got := session.GetPendingRecoveries(); len(got) != 2 || got[1].State != chat.PendingRecoveryScheduled {
		t.Fatalf("blocked claim mutated state: %+v", got)
	}

	session.RemovePendingRecovery(claimed.ID)
	application.recoveryEpoch.Add(1)
	if _, _, err := application.claimPendingRecovery(
		scheduled.ID, scheduled.SessionID, epoch); !errors.Is(err, errPendingRecoveryUnavailable) {
		t.Fatalf("stale epoch claim error = %v", err)
	}
	if got := session.GetPendingRecoveries(); len(got) != 1 || got[0].State != chat.PendingRecoveryScheduled {
		t.Fatalf("stale epoch stranded recovery as claimed: %+v", got)
	}
}

func TestQueuedExactRepeatIsPromotedToPersistedRecovery(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("queued-promotion")
	session.SetWorkDir(t.TempDir())
	recovery := validSerializedRecovery(
		"queued-recovery", session.GetID(), chat.PendingRecoveryScheduled)
	session.AddPendingRecovery(recovery, "")
	application := newPendingRecoveryTestApp(t, session)

	claimed, checkpoints, matched, err := application.claimQueuedRecoveryForPrompt(
		"original request", application.recoveryEpoch.Load())
	if err != nil || !matched || claimed.ID != recovery.ID || len(checkpoints) != 1 {
		t.Fatalf("queued promotion = %+v checkpoints=%+v matched=%v err=%v",
			claimed, checkpoints, matched, err)
	}
	if got := session.GetPendingRecoveries(); len(got) != 1 || got[0].State != chat.PendingRecoveryClaimed {
		t.Fatalf("queued promotion was not durably claimed: %+v", got)
	}
}

func TestDisabledSessionPersistenceUsesOnlyTypedInProcessFallback(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("disabled-persistence")
	session.SetWorkDir(t.TempDir())
	cfg := chat.DefaultSessionManagerConfig()
	cfg.Enabled = false
	manager, err := chat.NewSessionManager(session, cfg)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	application := &App{
		session: session, sessionManager: manager,
		executor: tools.NewExecutor(tools.NewRegistry(), nil, time.Second),
	}
	checkpoint := tools.ToolCheckpoint{
		CallID: "write-once", ToolName: "write",
		Args: map[string]any{"file_path": "x.go"}, Result: tools.NewSuccessResult("written"),
	}

	_, err = application.persistPendingRecovery(
		"auto_resume", "retry", "retry", []tools.ToolCheckpoint{checkpoint},
		1, time.Second, "", session.GetID(), application.recoveryEpoch.Load())
	if !errors.Is(err, errRecoveryPersistenceFailed) {
		t.Fatalf("disabled persistence error = %v", err)
	}
	if got := session.GetPendingRecoveries(); len(got) != 0 {
		t.Fatalf("disabled persistence retained provisional recovery: %+v", got)
	}
}

func TestClearingClaimRearmsDueSiblingWithoutDuplicateTimers(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("sibling-recovery-liveness")
	session.SetWorkDir(t.TempDir())
	due := time.Now().Add(500 * time.Millisecond)
	first := validSerializedRecovery("sibling-a", session.GetID(), chat.PendingRecoveryScheduled)
	first.Message = "first interrupted request"
	first.NotBefore = due
	first.GenerationSignature = chat.PendingRecoveryGenerationSignature(first)
	second := validSerializedRecovery("sibling-b", session.GetID(), chat.PendingRecoveryScheduled)
	second.Message = "second interrupted request"
	second.NotBefore = due
	second.GenerationSignature = chat.PendingRecoveryGenerationSignature(second)
	session.AddPendingRecovery(first, "")
	session.AddPendingRecovery(second, "")
	application := newPendingRecoveryTestApp(t, session)

	for range 20 {
		application.resumePersistedRecoveries(false)
	}
	application.recoveryTimerMu.Lock()
	if got := len(application.recoveryTimers); got != 2 {
		application.recoveryTimerMu.Unlock()
		t.Fatalf("deduplicated sibling timer count = %d, want 2", got)
	}
	application.recoveryTimerMu.Unlock()

	claimed, _, err := application.claimPendingRecovery(
		first.ID, first.SessionID, application.recoveryEpoch.Load())
	if err != nil {
		t.Fatalf("claim first sibling: %v", err)
	}
	application.markRecoveryDispatched(claimed.SessionID, claimed.ID)
	application.processing = true // the re-armed sibling should enter the FIFO.
	if err := application.clearPendingRecovery(claimed.ID, claimed.SessionID, "test_success"); err != nil {
		t.Fatalf("clear first sibling: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for application.pendingCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	request, _, ok := application.dequeuePendingRequest()
	if !ok || request.recoveryID != second.ID {
		t.Fatalf("re-armed sibling request = %+v ok=%v", request, ok)
	}
	got := session.GetPendingRecoveries()
	if len(got) != 1 || got[0].ID != second.ID || got[0].State != chat.PendingRecoveryClaimed {
		t.Fatalf("sibling state after re-arm = %+v", got)
	}
	application.recoveryTimerMu.Lock()
	remainingTimers := len(application.recoveryTimers)
	application.recoveryTimerMu.Unlock()
	if remainingTimers != 0 {
		t.Fatalf("timer registry retained %d entries after sibling claim", remainingTimers)
	}
}

func TestQueueFullReleasesProvenUnstartedClaimDurably(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("queue-full-release")
	session.SetWorkDir(t.TempDir())
	recovery := validSerializedRecovery(
		"queue-full-claim", session.GetID(), chat.PendingRecoveryScheduled)
	recovery.NotBefore = time.Now().Add(time.Hour)
	recovery.GenerationSignature = chat.PendingRecoveryGenerationSignature(recovery)
	session.AddPendingRecovery(recovery, "")
	application := newPendingRecoveryTestApp(t, session)
	application.processing = true
	for i := range maxPendingQueue {
		if _, ok := application.enqueuePending("ordinary queued request " + string(rune('a'+i))); !ok {
			t.Fatalf("fill queue item %d rejected", i)
		}
	}
	claimed, checkpoints, err := application.claimPendingRecovery(
		recovery.ID, recovery.SessionID, application.recoveryEpoch.Load())
	if err != nil {
		t.Fatalf("claimPendingRecovery: %v", err)
	}
	outcome := application.handleRecoveryResubmit(
		claimed.Message, claimed.UserMessage, claimed.ID, claimed.SessionID,
		application.recoveryEpoch.Load(), checkpoints)
	if outcome != recoveryDispatchReleased {
		t.Fatalf("queue-full dispatch outcome = %v, want released", outcome)
	}
	got := session.GetPendingRecoveries()
	if len(got) != 1 || got[0].State != chat.PendingRecoveryScheduled {
		t.Fatalf("queue-full claim was not returned to scheduled: %+v", got)
	}
	history, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}
	state, err := history.LoadFull(session.GetID())
	if err != nil {
		t.Fatalf("LoadFull: %v", err)
	}
	if len(state.PendingRecoveries) != 1 || state.PendingRecoveries[0].State != chat.PendingRecoveryScheduled {
		t.Fatalf("durable queue-full release = %+v", state.PendingRecoveries)
	}
}

func TestEscBarrierReleasesClaimAwaitingDispatch(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("esc-awaiting-claim")
	session.SetWorkDir(t.TempDir())
	recovery := validSerializedRecovery(
		"esc-awaiting", session.GetID(), chat.PendingRecoveryScheduled)
	recovery.NotBefore = time.Now().Add(time.Hour)
	recovery.GenerationSignature = chat.PendingRecoveryGenerationSignature(recovery)
	session.AddPendingRecovery(recovery, "")
	application := newPendingRecoveryTestApp(t, session)
	application.processing = true
	foregroundCtx, foregroundCancel := context.WithCancel(context.Background())
	application.processingCancel = foregroundCancel
	oldEpoch := application.recoveryEpoch.Load()
	claimed, checkpoints, err := application.claimPendingRecovery(
		recovery.ID, recovery.SessionID, oldEpoch)
	if err != nil {
		t.Fatalf("claimPendingRecovery: %v", err)
	}
	// Deliberately do not call handleRecoveryResubmit yet: this is the exact
	// claim->dispatch handoff window which the real Esc lifecycle must close in
	// the same barrier as it cancels the foreground owner.
	if !application.cancelProcessing() {
		t.Fatal("cancelProcessing did not report the foreground/recovery boundary")
	}
	select {
	case <-foregroundCtx.Done():
	default:
		t.Fatal("cancelProcessing did not cancel the pre-boundary foreground owner")
	}
	if application.recoveryEpoch.Load() == oldEpoch {
		t.Fatal("Esc barrier did not invalidate the pre-cancel dispatch epoch")
	}
	if outcome := application.handleRecoveryResubmit(
		claimed.Message, claimed.UserMessage, claimed.ID, claimed.SessionID,
		oldEpoch, checkpoints); outcome != recoveryDispatchIgnored {
		t.Fatalf("pre-Esc claim dispatch outcome = %v, want ignored", outcome)
	}
	// Model the cancelled turn's late defer. It must observe the cancellation
	// gate and cannot install a new owner or run anything from FIFO.
	application.finishForegroundProcessing(nil)
	if application.pendingCount() != 0 || application.processing {
		t.Fatalf("pre-Esc claim started or queued: pending=%d processing=%v",
			application.pendingCount(), application.processing)
	}
	got := session.GetPendingRecoveries()
	if len(got) != 1 || got[0].State != chat.PendingRecoveryScheduled {
		t.Fatalf("Esc barrier durable state = %+v", got)
	}
}

func TestRecoveryDispatchCannotStartThroughActiveCancelGate(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("cancel-gated-dispatch")
	session.SetWorkDir(t.TempDir())
	recovery := validSerializedRecovery(
		"cancel-gated-claim", session.GetID(), chat.PendingRecoveryScheduled)
	recovery.NotBefore = time.Now().Add(time.Hour)
	recovery.GenerationSignature = chat.PendingRecoveryGenerationSignature(recovery)
	session.AddPendingRecovery(recovery, "")
	application := newPendingRecoveryTestApp(t, session)
	application.dropSteerLeftovers = true
	epoch := application.recoveryEpoch.Load()
	claimed, checkpoints, err := application.claimPendingRecovery(
		recovery.ID, recovery.SessionID, epoch)
	if err != nil {
		t.Fatalf("claimPendingRecovery: %v", err)
	}
	if outcome := application.handleRecoveryResubmit(
		claimed.Message, claimed.UserMessage, claimed.ID, claimed.SessionID,
		epoch, checkpoints); outcome != recoveryDispatchReleased {
		t.Fatalf("cancel-gated dispatch outcome = %v, want released", outcome)
	}
	if application.processing || application.pendingCount() != 0 {
		t.Fatalf("cancel-gated recovery started or queued: processing=%v pending=%d",
			application.processing, application.pendingCount())
	}
	got := session.GetPendingRecoveries()
	if len(got) != 1 || got[0].State != chat.PendingRecoveryScheduled {
		t.Fatalf("cancel-gated recovery state = %+v, want scheduled", got)
	}
	application.recoveryTimerMu.Lock()
	timers := len(application.recoveryTimers)
	application.recoveryTimerMu.Unlock()
	if timers != 0 {
		t.Fatalf("cancel-gated recovery armed %d timer(s), want paused", timers)
	}
}

func TestExplicitExactPromptReopensCancelGateAndQueuesRecovery(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("explicit-resume-after-esc")
	session.SetWorkDir(t.TempDir())
	recovery := validSerializedRecovery(
		"explicit-after-esc", session.GetID(), chat.PendingRecoveryScheduled)
	recovery.UserMessage = "repeat this request"
	recovery.NotBefore = time.Now().Add(time.Hour)
	recovery.GenerationSignature = chat.PendingRecoveryGenerationSignature(recovery)
	session.AddPendingRecovery(recovery, "")
	application := newPendingRecoveryTestApp(t, session)
	program, model := newCapturingProgram(t)
	application.program = program
	application.commandHandler = commands.NewHandler()
	application.processing = true
	_, foregroundCancel := context.WithCancel(context.Background())
	application.processingCancel = foregroundCancel

	if !application.cancelProcessing() {
		t.Fatal("cancelProcessing did not establish the cancel gate")
	}
	application.mu.Lock()
	cancelGate := application.dropSteerLeftovers
	application.mu.Unlock()
	if !cancelGate {
		t.Fatal("Esc did not close the foreground handoff gate")
	}

	// This is new live user intent, not a late timer. While the cancelled
	// foreground unwinds it stays provenance-marked and does not globally reopen
	// automatic retries.
	application.handleSubmit(recovery.UserMessage)
	application.mu.Lock()
	cancelGate = application.dropSteerLeftovers
	application.mu.Unlock()
	if !cancelGate {
		t.Fatal("post-Esc input reopened the cancel gate before foreground handoff")
	}
	application.pendingMu.Lock()
	queued := append([]pendingRequest(nil), application.pendingQueue...)
	application.pendingMu.Unlock()
	if len(queued) != 1 || queued[0].message != recovery.UserMessage ||
		!queued[0].postCancelUserIntent || queued[0].recoveryID != "" {
		t.Fatalf("post-Esc exact prompt staging = %+v", queued)
	}
	got := session.GetPendingRecoveries()
	if len(got) != 1 || got[0].State != chat.PendingRecoveryScheduled {
		t.Fatalf("staged explicit recovery state = %+v, want scheduled", got)
	}
	application.recoveryTimerMu.Lock()
	timers := len(application.recoveryTimers)
	application.recoveryTimerMu.Unlock()
	if timers != 0 {
		t.Fatalf("Esc re-armed %d automatic recovery timer(s)", timers)
	}

	// Let the cancelled turn's real finalizer accept the marked request, but
	// block its first UI publication so we can inspect the exact promotion before
	// the model pipeline launches.
	deadline := time.Now().Add(time.Second)
	for len(model.dump()) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("post-Esc queue acknowledgement did not reach the program")
		}
		time.Sleep(time.Millisecond)
	}
	model.mu.Lock()
	modelLocked := true
	defer func() {
		if modelLocked {
			model.mu.Unlock()
		}
	}()
	program.Send(ui.StreamTextMsg("occupy event loop for exact recovery promotion"))
	finishDone := make(chan struct{})
	go func() {
		application.finishForegroundProcessing(nil)
		close(finishDone)
	}()
	deadline = time.Now().Add(time.Second)
	for {
		got = session.GetPendingRecoveries()
		if len(got) == 1 && got[0].State == chat.PendingRecoveryClaimed &&
			application.pendingCount() == 0 {
			break
		}
		if time.Now().After(deadline) {
			model.mu.Unlock()
			modelLocked = false
			t.Fatalf("exact post-Esc prompt was not promoted: %+v", got)
		}
		time.Sleep(time.Millisecond)
	}
	if awaiting := application.awaitingRecoveryClaimsForSession(session.GetID()); len(awaiting) != 0 {
		t.Fatalf("accepted promoted recovery retained proven-unstarted ownership: %+v", awaiting)
	}
	// Cancellation after foreground acceptance is execution-ambiguous and must
	// leave the marker claimed, never release it back to automatic schedule.
	application.CancelProcessing()
	got = session.GetPendingRecoveries()
	if len(got) != 1 || got[0].State != chat.PendingRecoveryClaimed {
		t.Fatalf("Esc after promotion released active claim: %+v", got)
	}
	model.mu.Unlock()
	modelLocked = false
	select {
	case <-finishDone:
	case <-time.After(time.Second):
		t.Fatal("promoted recovery handoff did not return")
	}
}

func TestPostEscExplicitSubmitWaitsForAuthoritativeDrain(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("post-esc-submit-barrier")
	session.SetWorkDir(t.TempDir())
	application := newPendingRecoveryTestApp(t, session)
	application.processing = true
	_, foregroundCancel := context.WithCancel(context.Background())
	application.processingCancel = foregroundCancel

	// Hold the FIFO leaf lock so cancelProcessing can acquire sessionLeaseMu,
	// close the gate, and then pause inside its authoritative drain. A correct
	// explicit submit blocks on that lease instead of ACKing an item that the
	// still-running drain will erase.
	application.pendingMu.Lock()
	cancelDone := make(chan struct{})
	go func() {
		application.cancelProcessing()
		close(cancelDone)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		application.mu.Lock()
		gateClosed := application.dropSteerLeftovers
		application.mu.Unlock()
		if gateClosed {
			break
		}
		if time.Now().After(deadline) {
			application.pendingMu.Unlock()
			t.Fatal("cancelProcessing did not reach the drain barrier")
		}
		time.Sleep(time.Millisecond)
	}
	submitDone := make(chan struct{})
	go func() {
		application.handleSubmit("new task after Esc")
		close(submitDone)
	}()
	select {
	case <-submitDone:
		application.pendingMu.Unlock()
		t.Fatal("explicit submit crossed session lease before cancel drain completed")
	case <-time.After(20 * time.Millisecond):
	}
	application.pendingMu.Unlock()
	select {
	case <-cancelDone:
	case <-time.After(time.Second):
		t.Fatal("cancelProcessing did not complete after FIFO lock release")
	}
	select {
	case <-submitDone:
	case <-time.After(time.Second):
		t.Fatal("explicit submit did not continue after cancel barrier")
	}
	request, remaining, ok, discarded := application.dequeuePostCancelUserPending()
	if !ok || remaining != 0 || request.message != "new task after Esc" || !request.postCancelUserIntent {
		t.Fatalf("post-Esc accepted request = %+v remaining=%d ok=%v discarded=%d",
			request, remaining, ok, discarded)
	}
}

func TestGenericPostEscTasksRunOnceInFIFOWithoutResumingOldRecovery(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("generic-post-esc-fifo")
	session.SetWorkDir(t.TempDir())
	recovery := validSerializedRecovery(
		"paused-old-recovery", session.GetID(), chat.PendingRecoveryScheduled)
	recovery.NotBefore = time.Now().Add(-time.Minute)
	recovery.GenerationSignature = chat.PendingRecoveryGenerationSignature(recovery)
	session.AddPendingRecovery(recovery, "")
	runOrder := make(chan string, 3)
	first := &stubCommand{name: "post-esc-first", fn: func(context.Context, []string, commands.AppInterface) (string, error) {
		runOrder <- "first"
		return "first done", nil
	}}
	second := &stubCommand{name: "post-esc-second", fn: func(context.Context, []string, commands.AppInterface) (string, error) {
		runOrder <- "second"
		return "second done", nil
	}}
	program, _ := newCapturingProgram(t)
	application := newPendingRecoveryTestApp(t, session)
	application.program = program
	application.commandHandler = commands.NewHandlerWithCommands(first, second)
	application.config = config.DefaultConfig()
	application.processing = true
	_, foregroundCancel := context.WithCancel(context.Background())
	application.processingCancel = foregroundCancel
	if !application.cancelProcessing() {
		t.Fatal("cancelProcessing did not establish post-Esc gate")
	}

	application.handleSubmit("/post-esc-first")
	application.handleSubmit("/post-esc-second")
	if got := application.pendingCount(); got != 2 {
		t.Fatalf("post-Esc explicit queue count = %d, want 2", got)
	}
	application.finishForegroundProcessing(nil)
	for index, want := range []string{"first", "second"} {
		select {
		case got := <-runOrder:
			if got != want {
				t.Fatalf("post-Esc run[%d] = %q, want %q", index, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("post-Esc task %q did not run", want)
		}
	}
	select {
	case duplicate := <-runOrder:
		t.Fatalf("post-Esc task ran more than once: %q", duplicate)
	case <-time.After(30 * time.Millisecond):
	}
	deadline := time.Now().Add(time.Second)
	for {
		application.mu.Lock()
		processing := application.processing
		application.mu.Unlock()
		if !processing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("post-Esc FIFO did not release foreground")
		}
		time.Sleep(time.Millisecond)
	}
	got := session.GetPendingRecoveries()
	if len(got) != 1 || got[0].ID != recovery.ID || got[0].State != chat.PendingRecoveryScheduled {
		t.Fatalf("generic post-Esc task resumed old recovery: %+v", got)
	}
	application.recoveryTimerMu.Lock()
	timers := len(application.recoveryTimers)
	application.recoveryTimerMu.Unlock()
	if timers != 0 {
		t.Fatalf("generic post-Esc tasks armed %d old recovery timer(s)", timers)
	}
}

func TestSteerMissCommitAfterEscKeepsExplicitRequestOwned(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("steer-miss-cancel-commit")
	session.SetWorkDir(t.TempDir())
	ran := make(chan struct{}, 2)
	after := &stubCommand{name: "after-steer-miss", fn: func(context.Context, []string, commands.AppInterface) (string, error) {
		ran <- struct{}{}
		return "done", nil
	}}
	program, _ := newCapturingProgram(t)
	application := newPendingRecoveryTestApp(t, session)
	application.program = program
	application.commandHandler = commands.NewHandlerWithCommands(after)
	application.config = config.DefaultConfig()
	application.processing = true
	_, foregroundCancel := context.WithCancel(context.Background())
	application.processingCancel = foregroundCancel

	// Deterministically model Esc landing after TryQueueUserSteer returned false
	// but before fallback ownership committed.
	if !application.cancelProcessing() {
		t.Fatal("cancelProcessing did not close steer-miss boundary")
	}
	application.commitSubmitAfterSteerMiss("/after-steer-miss", true)
	application.pendingMu.Lock()
	queued := append([]pendingRequest(nil), application.pendingQueue...)
	application.pendingMu.Unlock()
	if len(queued) != 1 || !queued[0].postCancelUserIntent || queued[0].message != "/after-steer-miss" {
		t.Fatalf("steer-miss fallback ownership = %+v", queued)
	}

	application.finishForegroundProcessing(nil)
	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("owned steer-miss fallback was stranded")
	}
	select {
	case <-ran:
		t.Fatal("owned steer-miss fallback ran more than once")
	case <-time.After(30 * time.Millisecond):
	}
}

func TestEscDrainReturnsQueuedClaimToSchedule(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("esc-queued-claim")
	session.SetWorkDir(t.TempDir())
	recovery := validSerializedRecovery(
		"esc-queued", session.GetID(), chat.PendingRecoveryScheduled)
	recovery.NotBefore = time.Now().Add(time.Hour)
	recovery.GenerationSignature = chat.PendingRecoveryGenerationSignature(recovery)
	session.AddPendingRecovery(recovery, "")
	application := newPendingRecoveryTestApp(t, session)
	application.processing = true
	_, cancel := context.WithCancel(context.Background())
	application.processingCancel = cancel
	claimed, checkpoints, err := application.claimPendingRecovery(
		recovery.ID, recovery.SessionID, application.recoveryEpoch.Load())
	if err != nil {
		t.Fatalf("claimPendingRecovery: %v", err)
	}
	if outcome := application.handleRecoveryResubmit(
		claimed.Message, claimed.UserMessage, claimed.ID, claimed.SessionID,
		application.recoveryEpoch.Load(), checkpoints); outcome != recoveryDispatchQueued {
		t.Fatalf("dispatch outcome = %v, want queued", outcome)
	}
	if !application.cancelProcessing() {
		t.Fatal("cancelProcessing did not report queued recovery cancellation")
	}
	if application.pendingCount() != 0 {
		t.Fatalf("Esc retained released recovery in FIFO: %d", application.pendingCount())
	}
	got := session.GetPendingRecoveries()
	if len(got) != 1 || got[0].State != chat.PendingRecoveryScheduled {
		t.Fatalf("Esc queued release state = %+v", got)
	}
}

func TestEscDropsForeignSessionRecoveryFIFOWithoutMakingItExecutable(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	active := chat.NewSession()
	active.SetID("active-cancel-session")
	active.SetWorkDir(t.TempDir())
	application := newPendingRecoveryTestApp(t, active)
	foreign := chat.NewSession()
	foreign.SetID("foreign-claimed-session")
	foreignRecovery := validSerializedRecovery(
		"foreign-claimed", foreign.GetID(), chat.PendingRecoveryClaimed)
	foreign.AddPendingRecovery(foreignRecovery, "")
	if _, ok := application.enqueueRecoveryPending(
		foreignRecovery.Message, foreignRecovery.UserMessage,
		foreignRecovery.ID, foreignRecovery.SessionID,
		application.recoveryEpoch.Load(),
		deserializeToolCheckpoints(foreignRecovery.Checkpoints)); !ok {
		t.Fatal("enqueue foreign recovery fixture")
	}

	_, remaining, released, err := application.cancelPendingAtRecoveryBoundary()
	if err != nil || remaining != 0 || released != 0 {
		t.Fatalf("foreign cancel boundary remaining=%d released=%d err=%v", remaining, released, err)
	}
	if got := application.pendingCount(); got != 0 {
		t.Fatalf("foreign claimed recovery retained executable FIFO owner: %d", got)
	}
	// Switching back cannot dequeue the cancelled FIFO request; only its durable
	// claimed marker remains, which requires explicit /recovery review.
	application.session = foreign
	if request, _, ok, _ := application.dequeuePendingRequestForSession(
		foreign.GetID(), application.recoveryEpoch.Load()); ok {
		t.Fatalf("foreign recovery became executable after switch-back: %+v", request)
	}
	got := foreign.GetPendingRecoveries()
	if len(got) != 1 || got[0].State != chat.PendingRecoveryClaimed {
		t.Fatalf("foreign durable marker changed: %+v", got)
	}
}

func TestEscSaveFailureLeavesQueuedRecoveryClaimedAndOutOfFIFO(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	session := chat.NewSession()
	session.SetID("esc-release-save-failure")
	session.SetWorkDir(t.TempDir())
	recovery := validSerializedRecovery(
		"esc-release-claimed", session.GetID(), chat.PendingRecoveryScheduled)
	recovery.NotBefore = time.Now().Add(time.Hour)
	recovery.GenerationSignature = chat.PendingRecoveryGenerationSignature(recovery)
	session.AddPendingRecovery(recovery, "")
	application := newPendingRecoveryTestApp(t, session)
	application.processing = true
	foregroundCtx, foregroundCancel := context.WithCancel(context.Background())
	application.processingCancel = foregroundCancel

	claimed, checkpoints, err := application.claimPendingRecovery(
		recovery.ID, recovery.SessionID, application.recoveryEpoch.Load())
	if err != nil {
		t.Fatalf("claimPendingRecovery: %v", err)
	}
	if outcome := application.handleRecoveryResubmit(
		claimed.Message, claimed.UserMessage, claimed.ID, claimed.SessionID,
		application.recoveryEpoch.Load(), checkpoints); outcome != recoveryDispatchQueued {
		t.Fatalf("dispatch outcome = %v, want queued", outcome)
	}

	sessionsDir := filepath.Join(xdg, "gokin", "sessions")
	if err := os.Rename(sessionsDir, sessionsDir+".saved"); err != nil {
		t.Fatalf("rename sessions dir: %v", err)
	}
	if err := os.WriteFile(sessionsDir, []byte("block directory recreation"), 0o600); err != nil {
		t.Fatalf("create blocking sessions path: %v", err)
	}

	if !application.cancelProcessing() {
		t.Fatal("cancelProcessing did not report the cancelled queued recovery")
	}
	select {
	case <-foregroundCtx.Done():
	default:
		t.Fatal("cancelProcessing did not cancel the foreground owner")
	}
	if got := application.pendingCount(); got != 0 {
		t.Fatalf("definite release failure restored recovery to FIFO: %d", got)
	}
	got := session.GetPendingRecoveries()
	if len(got) != 1 || got[0].ID != claimed.ID || got[0].State != chat.PendingRecoveryClaimed {
		t.Fatalf("definite release failure state = %+v, want claimed", got)
	}
	application.recoveryTimerMu.Lock()
	timers := len(application.recoveryTimers)
	application.recoveryTimerMu.Unlock()
	if timers != 0 {
		t.Fatalf("definite release failure armed %d automatic timer(s)", timers)
	}

	// The cancelled foreground's real terminal handoff must not resurrect the
	// failed claim now that Esc has returned to the event loop.
	application.finishForegroundProcessing(nil)
	if application.processing || application.pendingCount() != 0 {
		t.Fatalf("late finish restarted cancelled recovery: processing=%v pending=%d",
			application.processing, application.pendingCount())
	}
}

func TestCrossKindReplacementInheritsBothPersistedBudgets(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("cross-kind-budget")
	session.SetWorkDir(t.TempDir())
	claimed := validSerializedRecovery(
		"rate-limit-maxed", session.GetID(), chat.PendingRecoveryClaimed)
	claimed.Kind = "rate_limit"
	claimed.Attempt = maxAutoRateLimitRetries
	claimed.RateLimitAttempts = maxAutoRateLimitRetries
	claimed.AutoResumeAttempts = 0
	claimed.GenerationSignature = chat.PendingRecoveryGenerationSignature(claimed)
	session.AddPendingRecovery(claimed, "")
	application := newPendingRecoveryTestApp(t, session)
	// Volatile counters intentionally remain empty, simulating a restart.
	replacement, err := application.persistPendingRecovery(
		"auto_resume", claimed.Message, claimed.UserMessage,
		deserializeToolCheckpoints(claimed.Checkpoints), 1, time.Hour,
		claimed.ID, claimed.SessionID, application.recoveryEpoch.Load())
	if err != nil {
		t.Fatalf("persist cross-kind replacement: %v", err)
	}
	if replacement.RateLimitAttempts != maxAutoRateLimitRetries || replacement.AutoResumeAttempts != 1 {
		t.Fatalf("replacement budgets rate=%d auto=%d, want %d/1",
			replacement.RateLimitAttempts, replacement.AutoResumeAttempts, maxAutoRateLimitRetries)
	}
}

func TestPendingRecoveryClearFailureRetainsClaimedMarker(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	session := chat.NewSession()
	session.SetID("clear-save-failure")
	session.SetWorkDir(t.TempDir())
	recovery := validSerializedRecovery(
		"clear-failure-claim", session.GetID(), chat.PendingRecoveryScheduled)
	session.AddPendingRecovery(recovery, "")
	application := newPendingRecoveryTestApp(t, session)
	claimed, _, err := application.claimPendingRecovery(
		recovery.ID, recovery.SessionID, application.recoveryEpoch.Load())
	if err != nil {
		t.Fatalf("claimPendingRecovery: %v", err)
	}
	application.markRecoveryDispatched(claimed.SessionID, claimed.ID)
	sessionsDir := filepath.Join(xdg, "gokin", "sessions")
	if err := os.Rename(sessionsDir, sessionsDir+".saved"); err != nil {
		t.Fatalf("rename sessions dir: %v", err)
	}
	if err := os.WriteFile(sessionsDir, []byte("block directory recreation"), 0o600); err != nil {
		t.Fatalf("create blocking sessions path: %v", err)
	}
	clearErr := application.clearPendingRecovery(claimed.ID, claimed.SessionID, "test_success")
	if clearErr == nil || errors.Is(clearErr, errRecoveryCommitUncertain) {
		t.Fatalf("clear error = %v, want definite pre-commit failure", clearErr)
	}
	got := session.GetPendingRecoveries()
	if len(got) != 1 || got[0].ID != claimed.ID || got[0].State != chat.PendingRecoveryClaimed {
		t.Fatalf("failed clear did not retain claimed marker: %+v", got)
	}
}

func TestRecoveryClearFailureLatchesHeadlessTerminalOutcome(t *testing.T) {
	for _, clearErr := range []error{
		errors.New("definite save failure"),
		fmt.Errorf("%w: directory fsync", errRecoveryCommitUncertain),
	} {
		turn := &headlessTerminalOutcome{}
		application := &App{headlessRunActive: true, headlessTerminal: turn}
		application.reportPendingRecoveryClearFailure(turn, sideEffectRecoveryContext{
			recoveryID: "headless-recovery", sessionID: "headless-session",
		}, clearErr)
		terminal := application.headlessTerminalOutcomeSnapshot()
		if terminal == nil || terminal.Kind != "recovery_persistence" || terminal.Message != clearErr.Error() {
			t.Fatalf("headless terminal for %v = %+v", clearErr, terminal)
		}
	}

	active := &headlessTerminalOutcome{}
	stale := &headlessTerminalOutcome{}
	application := &App{headlessRunActive: true, headlessTerminal: active}
	application.reportPendingRecoveryClearFailure(stale, sideEffectRecoveryContext{
		recoveryID: "stale-recovery", sessionID: "stale-session",
	}, errors.New("late stale clear failure"))
	if terminal := application.headlessTerminalOutcomeSnapshot(); terminal != nil {
		t.Fatalf("stale headless token latched terminal outcome: %+v", terminal)
	}
	application.mu.Lock()
	application.headlessRunActive = false
	application.mu.Unlock()
	application.reportPendingRecoveryClearFailure(active, sideEffectRecoveryContext{
		recoveryID: "interactive-recovery", sessionID: "interactive-session",
	}, errors.New("interactive clear failure"))
	if terminal := application.headlessTerminalOutcomeSnapshot(); terminal != nil {
		t.Fatalf("non-headless clear failure latched terminal outcome: %+v", terminal)
	}
}
