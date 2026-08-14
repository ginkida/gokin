package evals

import (
	"strconv"
	"strings"
	"testing"
)

func TestBuildRunMatrixExpandsFaultProfiles(t *testing.T) {
	matrix, err := buildRunMatrix([]string{"glm"}, []string{"glm-5.2"}, nil, []string{"HTTP-429-ONCE", "after-tool-429-once"})
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

func TestBuildRunMatrixExpandsEngineModes(t *testing.T) {
	matrix, err := buildRunMatrix([]string{"glm"}, []string{"glm-5.2"}, []string{"tools", "AUTO", "auto", "hybrid"}, nil)
	if err != nil {
		t.Fatalf("buildRunMatrix: %v", err)
	}
	if len(matrix) != 3 {
		t.Fatalf("matrix length = %d, want 3", len(matrix))
	}
	for i, want := range []string{"tools", "auto", "hybrid"} {
		if matrix[i].EngineMode != want {
			t.Errorf("matrix[%d].EngineMode = %q, want %q", i, matrix[i].EngineMode, want)
		}
		if !strings.Contains(matrixLabel(matrix[i]), "engine-"+want) {
			t.Errorf("matrix label %q omits engine mode %q", matrixLabel(matrix[i]), want)
		}
	}
	if _, err := buildRunMatrix(nil, nil, []string{"python-only"}, nil); err == nil {
		t.Fatal("invalid engine mode was accepted")
	}
}

func TestExpandRunTrialsRotatesAndIsolatesMatrix(t *testing.T) {
	base, err := buildRunMatrix([]string{"glm"}, []string{"glm-5.2"}, []string{"tools", "auto", "hybrid"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	matrix, err := expandRunTrials(base, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(matrix) != 9 {
		t.Fatalf("matrix length = %d, want 9", len(matrix))
	}
	for trial := 1; trial <= 3; trial++ {
		start := (trial - 1) * 3
		for _, entry := range matrix[start : start+3] {
			if entry.Trial != trial || entry.TrialCount != 3 {
				t.Fatalf("trial block %d contains %+v", trial, entry)
			}
			if !strings.Contains(matrixLabel(entry), "trial-"+strconv.Itoa(trial)) {
				t.Fatalf("matrix label %q omits trial %d", matrixLabel(entry), trial)
			}
		}
	}
	if matrix[0].EngineMode != "tools" || matrix[3].EngineMode != "auto" || matrix[6].EngineMode != "hybrid" {
		t.Fatalf("trial order was not rotated: %q / %q / %q", matrix[0].EngineMode, matrix[3].EngineMode, matrix[6].EngineMode)
	}
	if _, err := expandRunTrials(base, -1); err == nil {
		t.Fatal("negative repeat was accepted")
	}
	if _, err := expandRunTrials(base, 101); err == nil {
		t.Fatal("repeat above safety cap was accepted")
	}
}

func TestExpandRunTrialsCounterbalancesEveryPairedCohort(t *testing.T) {
	base, err := buildRunMatrix(
		[]string{"glm", "openai"},
		[]string{"test-model"},
		[]string{"tools", "auto", "hybrid"},
		[]string{"http-429-once", "after-tool-429-once"},
	)
	if err != nil {
		t.Fatal(err)
	}
	matrix, err := expandRunTrials(base, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(matrix) != 36 {
		t.Fatalf("matrix length = %d, want 36", len(matrix))
	}

	wantModes := [][]string{
		{"tools", "auto", "hybrid"},
		{"auto", "hybrid", "tools"},
		{"hybrid", "tools", "auto"},
	}
	wantFirstCohorts := []string{
		"glm/http-429-once",
		"glm/after-tool-429-once",
		"openai/http-429-once",
	}
	const entriesPerTrial = 12
	for trial := 1; trial <= 3; trial++ {
		trialEntries := matrix[(trial-1)*entriesPerTrial : trial*entriesPerTrial]
		first := trialEntries[0].Provider + "/" + trialEntries[0].FaultProfile
		if first != wantFirstCohorts[trial-1] {
			t.Errorf("trial %d first cohort = %q, want %q", trial, first, wantFirstCohorts[trial-1])
		}
		for cohortStart := 0; cohortStart < entriesPerTrial; cohortStart += 3 {
			cohortEntries := trialEntries[cohortStart : cohortStart+3]
			firstEntry := cohortEntries[0]
			gotModes := make([]string, 0, 3)
			for _, entry := range cohortEntries {
				if entry.Provider != firstEntry.Provider || entry.Model != firstEntry.Model || entry.FaultProfile != firstEntry.FaultProfile {
					t.Fatalf("trial %d cohort is not contiguous: %+v", trial, cohortEntries)
				}
				if entry.Trial != trial || entry.TrialCount != 3 {
					t.Fatalf("trial %d contains invalid provenance: %+v", trial, entry)
				}
				gotModes = append(gotModes, entry.EngineMode)
			}
			if strings.Join(gotModes, ",") != strings.Join(wantModes[trial-1], ",") {
				t.Errorf("trial %d cohort modes = %v, want %v", trial, gotModes, wantModes[trial-1])
			}
		}
	}
}

func TestExpandRunTrialsBalancesDirectedCarryoverAcrossReversedBlock(t *testing.T) {
	base, err := buildRunMatrix(
		[]string{"glm"},
		[]string{"test-model"},
		[]string{"tools", "auto", "hybrid"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	matrix, err := expandRunTrials(base, 6)
	if err != nil {
		t.Fatal(err)
	}
	wantOrders := []string{
		"tools,auto,hybrid",
		"auto,hybrid,tools",
		"hybrid,tools,auto",
		"hybrid,auto,tools",
		"tools,hybrid,auto",
		"auto,tools,hybrid",
	}
	positions := make(map[string][3]int)
	carryover := make(map[string]int)
	for trial := 0; trial < 6; trial++ {
		entries := matrix[trial*3 : trial*3+3]
		order := []string{entries[0].EngineMode, entries[1].EngineMode, entries[2].EngineMode}
		if got := strings.Join(order, ","); got != wantOrders[trial] {
			t.Errorf("trial %d order = %q, want %q", trial+1, got, wantOrders[trial])
		}
		for position, mode := range order {
			counts := positions[mode]
			counts[position]++
			positions[mode] = counts
			if position > 0 {
				carryover[order[position-1]+">"+mode]++
			}
		}
	}
	for mode, counts := range positions {
		if counts != [3]int{2, 2, 2} {
			t.Errorf("mode %q position counts = %v, want [2 2 2]", mode, counts)
		}
	}
	for _, left := range []string{"tools", "auto", "hybrid"} {
		for _, right := range []string{"tools", "auto", "hybrid"} {
			if left == right {
				continue
			}
			pair := left + ">" + right
			if carryover[pair] != 2 {
				t.Errorf("directed carry-over %s = %d, want 2", pair, carryover[pair])
			}
		}
	}
}

func TestExpandRunTrialsKeepsTwoModeAlternation(t *testing.T) {
	base, err := buildRunMatrix(
		[]string{"glm"},
		[]string{"test-model"},
		[]string{"tools", "auto"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	matrix, err := expandRunTrials(base, 6)
	if err != nil {
		t.Fatal(err)
	}
	for trial := 0; trial < 6; trial++ {
		entries := matrix[trial*2 : trial*2+2]
		wantFirst := "tools"
		if trial%2 == 1 {
			wantFirst = "auto"
		}
		if entries[0].EngineMode != wantFirst {
			t.Errorf("trial %d first mode = %q, want %q", trial+1, entries[0].EngineMode, wantFirst)
		}
	}
}

func TestExpandRunTrialsCounterbalancesIncompleteModeSetsIndependently(t *testing.T) {
	base := []matrixEntry{
		{Provider: "a", Model: "m", EngineMode: "tools"},
		{Provider: "a", Model: "m", EngineMode: "auto"},
		{Provider: "b", Model: "m", EngineMode: "auto"},
	}
	matrix, err := expandRunTrials(base, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a/tools", "a/auto", "b/auto", "b/auto", "a/auto", "a/tools"}
	for i, entry := range matrix {
		got := entry.Provider + "/" + entry.EngineMode
		if got != want[i] {
			t.Errorf("matrix[%d] = %q, want %q", i, got, want[i])
		}
	}
}

func TestExpandRunTrialsRejectsDuplicateModeInPairedCohort(t *testing.T) {
	base := []matrixEntry{
		{Provider: "glm", Model: "m", EngineMode: "auto"},
		{Provider: "glm", Model: "m", EngineMode: "auto"},
	}
	if _, err := expandRunTrials(base, 2); err == nil || !strings.Contains(err.Error(), "repeats engine mode") {
		t.Fatalf("duplicate mode error = %v", err)
	}
}

func TestFinalizeReliabilityFailsClosedAndPassesWithEvidence(t *testing.T) {
	result := Result{
		FaultProfile: "http-429-once",
		Fault:        &FaultInjectionSummary{Injected: 1, MessageRequestsAfterInjection: 1},
		Agent:        CommandResult{Success: true, OutputPreview: "Fixed and verified."},
		Verification: []CommandResult{{Success: true}},
		Journal:      &JournalSummary{Path: "<eval-runtime>/execution_journal.jsonl", TrustedRuntime: true},
		Metrics:      map[string]bool{"task_completed": true},
	}
	finalizeReliability(&result)
	if result.Status != "passed" || result.Reliability == nil || !result.Reliability.Passed {
		t.Fatalf("reliability result = status %q, summary %+v", result.Status, result.Reliability)
	}

	result.Journal = &JournalSummary{Path: "<eval-runtime>/execution_journal.jsonl", TrustedRuntime: true,
		DuplicateSideEffectExecutions: []string{"edit:abc123"}}
	finalizeReliability(&result)
	if result.Status != "failed" || result.Reliability.NoDuplicateSideEffects {
		t.Fatalf("duplicate execution did not fail closed: status %q, summary %+v", result.Status, result.Reliability)
	}

	result.Journal = &JournalSummary{Path: ".gokin/execution_journal.jsonl"}
	finalizeReliability(&result)
	if result.Status != "failed" || result.Reliability.NoDuplicateSideEffects {
		t.Fatalf("model-writable journal was accepted as reliability evidence: status %q, summary %+v", result.Status, result.Reliability)
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
	got := resultVariant(Result{Provider: "glm", Model: "glm-5.2", EngineMode: "auto", FaultProfile: "after-tool-429-once", Trial: 2, TrialCount: 3})
	if got != "glm/glm-5.2/engine=auto/fault=after-tool-429-once/trial=2/3" {
		t.Fatalf("resultVariant = %q", got)
	}
}

func TestJournalCapturesHeadlessEfficiencyMetrics(t *testing.T) {
	ws := t.TempDir()
	writeJournal(t, ws, []string{
		`{"event":"engine_policy","details":{"mode":"auto","strategy":"aggregation","repl_enabled":true,"reason":"collection-scale aggregation request"}}`,
		`{"event":"tool_start","details":{"tool":"repl_exec","args":{"action":"execute"}}}`,
		`{"event":"tool_end","details":{"tool":"repl_exec","success":true,"repl_operations":{"count_code_many":1,"search_code":2,"invalid-name":5,"fractional":1.5,"zero":0},"repl_file_index_refreshes":1}}`,
		`{"event":"tool_end","details":{"tool":"repl_exec","success":true,"repl_operations":{"count_code_many":2},"repl_file_index_refreshes":2}}`,
		`{"event":"tool_end","details":{"tool":"read","success":true,"repl_operations":{"count_code_many":100},"repl_file_index_refreshes":100}}`,
		`{"event":"headless_metrics","details":{"input_tokens":120,"output_tokens":30,"cache_read_input_tokens":50,"total_tokens":150,"model_rounds":3,"duration_ms":4200,"estimated_usd":0.012,"cost_tracked":true}}`,
	})
	summary := summarizeExecutionJournal(ws, "", nil)
	if summary == nil || summary.HeadlessMetrics == nil {
		t.Fatal("headless metrics missing from journal summary")
	}
	if summary.HeadlessMetrics.ModelRounds != 3 || summary.HeadlessMetrics.TotalTokens != 150 ||
		!summary.HeadlessMetrics.TokenBreakdownTracked ||
		summary.ToolCounts["repl_exec"] != 1 || !summary.HeadlessMetrics.CostTracked ||
		summary.HybridPolicy == nil || !summary.HybridPolicy.REPLEligible ||
		!summary.HybridPolicy.REPLEnabled || summary.HybridPolicy.Mode != "auto" ||
		summary.HybridPolicy.Strategy != "aggregation" {
		t.Fatalf("journal summary = %+v", summary)
	}
	if len(summary.ReplOperations) != 2 || summary.ReplOperations["count_code_many"] != 3 ||
		summary.ReplOperations["search_code"] != 2 {
		t.Fatalf("REPL operations = %#v, want valid counters aggregated across tool_end events", summary.ReplOperations)
	}
	if summary.ReplFileIndexRefreshes != 3 {
		t.Fatalf("file index refreshes = %d, want 3 parent-observed callbacks", summary.ReplFileIndexRefreshes)
	}
}

func TestJournalTokenBreakdownRequiresEveryIntegerComponent(t *testing.T) {
	for _, event := range []string{
		`{"event":"headless_metrics","details":{"input_tokens":120,"output_tokens":30,"total_tokens":150}}`,
		`{"event":"headless_metrics","details":{"input_tokens":120,"output_tokens":30,"cache_read_input_tokens":1.5,"total_tokens":150}}`,
		`{"event":"headless_metrics","details":{"input_tokens":120,"output_tokens":30,"cache_read_input_tokens":"50","total_tokens":150}}`,
	} {
		ws := t.TempDir()
		writeJournal(t, ws, []string{event})
		summary := summarizeExecutionJournal(ws, "", nil)
		if summary == nil || summary.HeadlessMetrics == nil {
			t.Fatalf("headless summary missing for %s", event)
		}
		if summary.HeadlessMetrics.TokenBreakdownTracked {
			t.Errorf("incomplete/non-integer event marked as tracked: %s", event)
		}
	}
}

func TestJournalCapturesEligibleButUnavailableExposureGap(t *testing.T) {
	ws := t.TempDir()
	writeJournal(t, ws, []string{
		`{"event":"engine_policy","details":{"mode":"auto","repl_eligible":true,"repl_enabled":false,"exposure_gap":true,"reason":"collection-scale aggregation request"}}`,
	})
	summary := summarizeExecutionJournal(ws, "", nil)
	if summary == nil || summary.HybridPolicy == nil {
		t.Fatal("hybrid policy missing from journal summary")
	}
	policy := summary.HybridPolicy
	if !policy.REPLEligible || policy.REPLEnabled || !policy.ExposureGap {
		t.Fatalf("exposure gap summary = %+v", policy)
	}
}
