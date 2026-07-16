package evals

import "testing"

func TestBuildRunMatrixExpandsFaultProfiles(t *testing.T) {
	matrix, err := buildRunMatrix([]string{"glm"}, []string{"glm-5.2"}, []string{"HTTP-429-ONCE", "after-tool-429-once"})
	if err != nil {
		t.Fatalf("buildRunMatrix: %v", err)
	}
	if len(matrix) != 2 {
		t.Fatalf("matrix length = %d, want 2", len(matrix))
	}
	if matrix[0].FaultProfile != "http-429-once" || matrix[1].FaultProfile != "after-tool-429-once" {
		t.Fatalf("fault profiles = %q, %q", matrix[0].FaultProfile, matrix[1].FaultProfile)
	}
	if matrixLabel(matrix[0]) == matrixLabel(matrix[1]) {
		t.Fatalf("fault variants share workspace label %q", matrixLabel(matrix[0]))
	}
}

func TestFinalizeReliabilityFailsClosedAndPassesWithEvidence(t *testing.T) {
	result := Result{
		FaultProfile: "http-429-once",
		Fault:        &FaultInjectionSummary{Injected: 1, MessageRequestsAfterInjection: 1},
		Agent:        CommandResult{Success: true, OutputPreview: "Fixed and verified."},
		Verification: []CommandResult{{Success: true}},
		Metrics:      map[string]bool{"task_completed": true},
	}
	finalizeReliability(&result)
	if result.Status != "passed" || result.Reliability == nil || !result.Reliability.Passed {
		t.Fatalf("reliability result = status %q, summary %+v", result.Status, result.Reliability)
	}

	result.Journal = &JournalSummary{DuplicateSideEffectExecutions: []string{"edit:abc123"}}
	finalizeReliability(&result)
	if result.Status != "failed" || result.Reliability.NoDuplicateSideEffects {
		t.Fatalf("duplicate execution did not fail closed: status %q, summary %+v", result.Status, result.Reliability)
	}
}

func TestJournalReliabilityEvidenceAndDuplicateExecution(t *testing.T) {
	ws := t.TempDir()
	writeJournal(t, ws, []string{
		`{"event":"request_started"}`,
		`{"event":"tool_start","details":{"tool":"edit","args":{"file_path":"a.go","old_string":"a","new_string":"b"}}}`,
		`{"event":"request_failed"}`,
		`{"event":"side_effect_recovery_persisted"}`,
		`{"event":"rate_limit_auto_retry_scheduled"}`,
		`{"event":"side_effect_recovery_claimed"}`,
		`{"event":"request_started"}`,
		`{"event":"retry_safety","details":{"kind":"checkpoint_replayed"}}`,
		`{"event":"tool_start","details":{"tool":"edit","args":{"file_path":"a.go","old_string":"a","new_string":"b"}}}`,
		`{"event":"side_effect_recovery_cleared"}`,
	})
	summary := summarizeExecutionJournal(ws, "", nil)
	if summary.RequestFailures != 1 || summary.RetriesScheduled != 1 || summary.CheckpointReplays != 1 {
		t.Fatalf("retry evidence = %+v", summary)
	}
	if summary.RecoveriesPersisted != 1 || summary.RecoveriesClaimed != 1 || summary.RecoveriesCleared != 1 {
		t.Fatalf("recovery evidence = %+v", summary)
	}
	if len(summary.DuplicateSideEffectExecutions) != 1 {
		t.Fatalf("duplicate executions = %v, want one", summary.DuplicateSideEffectExecutions)
	}
}

func TestResultVariantIncludesFaultProfile(t *testing.T) {
	got := resultVariant(Result{Provider: "glm", Model: "glm-5.2", FaultProfile: "after-tool-429-once"})
	if got != "glm/glm-5.2/fault=after-tool-429-once" {
		t.Fatalf("resultVariant = %q", got)
	}
}
