package plan

import (
	"context"
	"errors"
	"testing"
	"time"
)

// --- Status tests ---

func TestStatusString(t *testing.T) {
	tests := []struct {
		status Status
		want   string
	}{
		{StatusPending, "pending"},
		{StatusInProgress, "in_progress"},
		{StatusCompleted, "completed"},
		{StatusFailed, "failed"},
		{StatusSkipped, "skipped"},
		{StatusPaused, "paused"},
		{Status(99), "unknown"},
	}
	for _, tt := range tests {
		got := tt.status.String()
		if got != tt.want {
			t.Errorf("Status(%d).String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestStatusIcon(t *testing.T) {
	tests := []struct {
		status Status
		want   string
	}{
		{StatusPending, "○"},
		{StatusInProgress, "◐"},
		{StatusCompleted, "●"},
		{StatusFailed, "✗"},
		{StatusSkipped, "⊘"},
		{StatusPaused, "⏸"},
		{Status(99), "?"},
	}
	for _, tt := range tests {
		got := tt.status.Icon()
		if got != tt.want {
			t.Errorf("Status(%d).Icon() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

// --- Step tests ---

func TestStepDuration(t *testing.T) {
	s := &Step{}
	if s.Duration() != 0 {
		t.Error("unstarted step should have 0 duration")
	}

	s.StartTime = time.Now().Add(-100 * time.Millisecond)
	// EndTime zero — returns time.Since(StartTime)
	d := s.Duration()
	if d < 90*time.Millisecond {
		t.Errorf("running step duration = %v, want >= 90ms", d)
	}

	s.EndTime = s.StartTime.Add(200 * time.Millisecond)
	if s.Duration() != 200*time.Millisecond {
		t.Errorf("completed step duration = %v, want 200ms", s.Duration())
	}
}

func TestStepCanRetry(t *testing.T) {
	s := &Step{Status: StatusFailed, MaxRetries: 3, RetryCount: 0}
	if !s.CanRetry() {
		t.Error("failed step with retries remaining should be retryable")
	}

	s.RetryCount = 3
	if s.CanRetry() {
		t.Error("step at max retries should not be retryable")
	}

	s.Status = StatusCompleted
	s.RetryCount = 0
	if s.CanRetry() {
		t.Error("completed step should not be retryable")
	}

	s2 := &Step{Status: StatusFailed, MaxRetries: 0}
	if s2.CanRetry() {
		t.Error("step with MaxRetries=0 should not be retryable")
	}
}

func TestStepHasTimedOut(t *testing.T) {
	// No timeout set
	s := &Step{Status: StatusInProgress, StartTime: time.Now().Add(-time.Hour)}
	if s.HasTimedOut() {
		t.Error("step without timeout should not time out")
	}

	// Has timeout, not yet exceeded
	s.Timeout = 2 * time.Hour
	s.StartTime = time.Now()
	if s.HasTimedOut() {
		t.Error("step within timeout should not time out")
	}

	// Has timed out
	s.Timeout = time.Millisecond
	s.StartTime = time.Now().Add(-time.Second)
	if !s.HasTimedOut() {
		t.Error("step past timeout should time out")
	}

	// Not in progress
	s.Status = StatusCompleted
	if s.HasTimedOut() {
		t.Error("completed step should not time out")
	}
}

func TestStepEnsureContractDefaults(t *testing.T) {
	s := &Step{Title: "  Build  ", Description: "  desc  "}
	s.EnsureContractDefaults()

	if s.Title != "Build" {
		t.Errorf("Title = %q, want trimmed", s.Title)
	}
	if s.Description != "desc" {
		t.Errorf("Description = %q, want trimmed", s.Description)
	}
	if len(s.Inputs) == 0 {
		t.Error("Inputs should have default")
	}
	if s.ExpectedArtifact == "" {
		t.Error("ExpectedArtifact should have default")
	}
	if len(s.SuccessCriteria) == 0 {
		t.Error("SuccessCriteria should have default")
	}
	if len(s.VerifyCommands) == 0 {
		t.Error("VerifyCommands should have default")
	}
	if s.Rollback == "" {
		t.Error("Rollback should have default")
	}
}

func TestStepEnsureContractDefaultsPreservesExisting(t *testing.T) {
	s := &Step{
		Title:            "Title",
		Description:      "Desc",
		Inputs:           []string{"input1"},
		ExpectedArtifact: "artifact",
		SuccessCriteria:  []string{"criteria"},
		VerifyCommands:   []string{"make test"},
		Rollback:         "git checkout .",
	}
	s.EnsureContractDefaults()

	if len(s.Inputs) != 1 || s.Inputs[0] != "input1" {
		t.Error("should preserve existing Inputs")
	}
	if s.ExpectedArtifact != "artifact" {
		t.Error("should preserve existing ExpectedArtifact")
	}
	if len(s.VerifyCommands) != 1 || s.VerifyCommands[0] != "make test" {
		t.Error("should preserve existing VerifyCommands")
	}
}

// --- Plan tests ---

func TestNewPlan(t *testing.T) {
	p := NewPlan("Test Plan", "A test plan")
	if p.Title != "Test Plan" {
		t.Errorf("Title = %q", p.Title)
	}
	if p.Status != StatusPending {
		t.Errorf("Status = %v", p.Status)
	}
	if p.Lifecycle != LifecycleDraft {
		t.Errorf("Lifecycle = %v", p.Lifecycle)
	}
	if len(p.Steps) != 0 {
		t.Error("should start with no steps")
	}
	if p.RunLedger == nil {
		t.Error("RunLedger should be initialized")
	}
}

func TestPlanAddStep(t *testing.T) {
	p := NewPlan("Test", "")
	s := p.AddStep("Step 1", "First step")
	if s.ID != 1 {
		t.Errorf("first step ID = %d, want 1", s.ID)
	}
	if s.Status != StatusPending {
		t.Errorf("Status = %v", s.Status)
	}
	if p.StepCount() != 1 {
		t.Errorf("StepCount = %d", p.StepCount())
	}

	s2 := p.AddStep("Step 2", "Second step")
	if s2.ID != 2 {
		t.Errorf("second step ID = %d, want 2", s2.ID)
	}
}

func TestPlanAddStepWithOptions(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "first")
	s2 := p.AddStepWithOptions("Step 2", "second", true, []int{1})

	if !s2.Parallel {
		t.Error("step should be parallel")
	}
	if len(s2.DependsOn) != 1 || s2.DependsOn[0] != 1 {
		t.Errorf("DependsOn = %v", s2.DependsOn)
	}
}

func TestPlanAddStepFull(t *testing.T) {
	p := NewPlan("Test", "")
	s := p.AddStepFull("Step 1", "desc", false, nil, 3, 5*time.Minute)
	if s.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d", s.MaxRetries)
	}
	if s.Timeout != 5*time.Minute {
		t.Errorf("Timeout = %v", s.Timeout)
	}
}

func TestPlanGetStep(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "first")

	s := p.GetStep(1)
	if s == nil {
		t.Fatal("should find step 1")
	}
	if s.Title != "Step 1" {
		t.Errorf("Title = %q", s.Title)
	}

	// Deep copy — modifying returned step should not affect plan
	s.Title = "Modified"
	orig := p.GetStep(1)
	if orig.Title == "Modified" {
		t.Error("GetStep should return deep copy")
	}

	if p.GetStep(99) != nil {
		t.Error("nonexistent step should return nil")
	}
}

func TestPlanCurrentStep(t *testing.T) {
	p := NewPlan("Test", "")
	if p.CurrentStep() != nil {
		t.Error("empty plan should have no current step")
	}

	p.AddStep("Step 1", "")
	p.AddStep("Step 2", "")

	cs := p.CurrentStep()
	if cs == nil || cs.ID != 1 {
		t.Error("should return first pending step")
	}

	p.StartStep(1)
	cs = p.CurrentStep()
	if cs == nil || cs.ID != 1 {
		t.Error("should return in-progress step")
	}
}

func TestPlanNextStep(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.AddStep("Step 2", "")

	ns := p.NextStep()
	if ns == nil || ns.ID != 1 {
		t.Error("should return first pending")
	}

	p.StartStep(1)
	// Step 1 is now in_progress, NextStep should return step 2
	ns = p.NextStep()
	if ns == nil || ns.ID != 2 {
		t.Errorf("should return step 2, got %v", ns)
	}
}

// --- Step state transitions ---

func TestPlanStartStep(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.StartStep(1)

	s := p.GetStep(1)
	if s.Status != StatusInProgress {
		t.Errorf("Status = %v, want in_progress", s.Status)
	}
	if s.StartTime.IsZero() {
		t.Error("StartTime should be set")
	}
	if p.Status != StatusInProgress {
		t.Error("plan status should be in_progress")
	}
}

func TestPlanCompleteStepRequiresEvidence(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.StartStep(1)

	// Complete without evidence — should pause
	p.CompleteStep(1, "output")
	s := p.GetStep(1)
	if s.Status != StatusPaused {
		t.Errorf("Status = %v, want paused (missing evidence)", s.Status)
	}
}

func TestPlanCompleteStepWithEvidence(t *testing.T) {
	p := NewPlan("Test", "")
	step := p.AddStep("Step 1", "")
	p.StartStep(1)

	// Record evidence + verify proof
	p.RecordStepVerification(1, []string{
		"diff applied",
		"contract.verify_commands_proof=true",
	}, "verified")

	p.CompleteStep(1, "done")
	s := p.GetStep(1)
	if s.Status != StatusCompleted {
		t.Errorf("Status = %v, want completed", s.Status)
	}
	if s.Output != "done" {
		t.Errorf("Output = %q", s.Output)
	}

	// Verify plan completes when all steps done
	if p.Status != StatusCompleted {
		t.Errorf("plan Status = %v, want completed", p.Status)
	}
	_ = step
}

func TestPlanFailStep(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.StartStep(1)
	p.FailStep(1, "something broke")

	s := p.GetStep(1)
	if s.Status != StatusFailed {
		t.Errorf("Status = %v", s.Status)
	}
	if s.Error != "something broke" {
		t.Errorf("Error = %q", s.Error)
	}
	if p.Status != StatusFailed {
		t.Error("plan should be failed")
	}
}

func TestPlanSkipStep(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.SkipStep(1)

	s := p.GetStep(1)
	if s.Status != StatusSkipped {
		t.Errorf("Status = %v", s.Status)
	}
}

func TestPlanPauseStep(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.StartStep(1)
	p.PauseStep(1, "checkpoint required")

	s := p.GetStep(1)
	if s.Status != StatusPaused {
		t.Errorf("Status = %v", s.Status)
	}
	if s.Error != "checkpoint required" {
		t.Errorf("Error = %q", s.Error)
	}
}

func TestPlanRetryStep(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStepFull("Step 1", "", false, nil, 3, 0)
	p.StartStep(1)
	p.FailStep(1, "error")

	ok := p.RetryStep(1)
	if !ok {
		t.Error("retry should succeed")
	}
	s := p.GetStep(1)
	if s.Status != StatusPending {
		t.Errorf("retried step Status = %v", s.Status)
	}
	if s.RetryCount != 1 {
		t.Errorf("RetryCount = %d", s.RetryCount)
	}
	if s.Error != "" {
		t.Error("error should be cleared on retry")
	}

	// Retry non-failed step should fail
	ok = p.RetryStep(1)
	if ok {
		t.Error("retrying pending step should fail")
	}
}

func TestPlanRetryStepExhausted(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStepFull("Step 1", "", false, nil, 1, 0)
	p.StartStep(1)
	p.FailStep(1, "err")
	p.RetryStep(1) // retry 1 of 1
	p.StartStep(1)
	p.FailStep(1, "err again")

	ok := p.RetryStep(1)
	if ok {
		t.Error("should fail when retries exhausted")
	}
}

// --- Progress and metrics ---

func TestPlanProgress(t *testing.T) {
	p := NewPlan("Test", "")
	if p.Progress() != 0 {
		t.Error("empty plan progress should be 0")
	}

	p.AddStep("Step 1", "")
	p.AddStep("Step 2", "")

	if p.Progress() != 0 {
		t.Error("all pending progress should be 0")
	}

	p.SkipStep(1)
	if p.Progress() != 0.5 {
		t.Errorf("progress = %v, want 0.5", p.Progress())
	}
}

func TestPlanCompletedCount(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.AddStep("Step 2", "")

	if p.CompletedCount() != 0 {
		t.Error("should be 0")
	}
}

func TestPlanPendingCount(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.AddStep("Step 2", "")

	if p.PendingCount() != 2 {
		t.Errorf("PendingCount = %d", p.PendingCount())
	}
}

func TestPlanIsComplete(t *testing.T) {
	p := NewPlan("Test", "")
	if p.IsComplete() {
		t.Error("new plan should not be complete")
	}

	p.Status = StatusCompleted
	if !p.IsComplete() {
		t.Error("completed plan should be complete")
	}

	p.Status = StatusFailed
	if !p.IsComplete() {
		t.Error("failed plan should be complete")
	}
}

func TestPlanStepCount(t *testing.T) {
	p := NewPlan("Test", "")
	if p.StepCount() != 0 {
		t.Error("should be 0")
	}
	p.AddStep("s1", "")
	p.AddStep("s2", "")
	if p.StepCount() != 2 {
		t.Errorf("StepCount = %d", p.StepCount())
	}
}

// --- Snapshots ---

func TestPlanGetStepsSnapshot(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")

	snap := p.GetStepsSnapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d", len(snap))
	}
	// Modify snapshot — should not affect plan
	snap[0].Title = "Modified"
	orig := p.GetStep(1)
	if orig.Title == "Modified" {
		t.Error("snapshot should be a deep copy")
	}
}

func TestPlanContextSnapshot(t *testing.T) {
	p := NewPlan("Test", "")
	p.SetContextSnapshot("context data")
	if p.GetContextSnapshot() != "context data" {
		t.Errorf("snapshot = %q", p.GetContextSnapshot())
	}
}

func TestPlanGetRunLedgerSnapshot(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.StartStep(1)

	snap := p.GetRunLedgerSnapshot()
	if len(snap) == 0 {
		t.Error("ledger should have entry after StartStep")
	}
}

// --- NextReadySteps ---

func TestNextReadyStepsEmpty(t *testing.T) {
	p := NewPlan("Test", "")
	if p.NextReadySteps() != nil {
		t.Error("empty plan should return nil")
	}
}

func TestNextReadyStepsSingle(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")

	ready := p.NextReadySteps()
	if len(ready) != 1 {
		t.Fatalf("ready = %d, want 1", len(ready))
	}
	if ready[0].ID != 1 {
		t.Errorf("ready[0].ID = %d", ready[0].ID)
	}
}

func TestNextReadyStepsUnmetDependency(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.AddStepWithOptions("Step 2", "", false, []int{1})

	ready := p.NextReadySteps()
	// Should return only step 1 (step 2 depends on 1)
	if len(ready) != 1 || ready[0].ID != 1 {
		t.Errorf("should only return step 1, got %v", ready)
	}
}

func TestNextReadyStepsDependencySatisfied(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.AddStepWithOptions("Step 2", "", false, []int{1})

	// Complete step 1
	p.StartStep(1)
	p.RecordStepVerification(1, []string{"done", "contract.verify_commands_proof=true"}, "ok")
	p.CompleteStep(1, "done")

	ready := p.NextReadySteps()
	if len(ready) != 1 || ready[0].ID != 2 {
		t.Errorf("step 2 should be ready after step 1 completed, got %v", ready)
	}
}

func TestNextReadyStepsParallel(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStepWithOptions("Step 1", "", true, nil)
	p.AddStepWithOptions("Step 2", "", true, nil)
	p.AddStepWithOptions("Step 3", "", true, nil)

	ready := p.NextReadySteps()
	if len(ready) != 3 {
		t.Errorf("should return all 3 parallel steps, got %d", len(ready))
	}
}

func TestNextReadyStepsParallelMaxFour(t *testing.T) {
	p := NewPlan("Test", "")
	for i := 0; i < 6; i++ {
		p.AddStepWithOptions("Step", "", true, nil)
	}

	ready := p.NextReadySteps()
	if len(ready) > 4 {
		t.Errorf("parallel steps capped at 4, got %d", len(ready))
	}
}

func TestNextReadyStepsNonParallelFirst(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStepWithOptions("Step 1", "", false, nil) // non-parallel
	p.AddStepWithOptions("Step 2", "", true, nil)  // parallel

	ready := p.NextReadySteps()
	if len(ready) != 1 || ready[0].ID != 1 {
		t.Error("non-parallel first step should block others")
	}
}

func TestNextReadyStepsDeepCopy(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "original")

	ready := p.NextReadySteps()
	ready[0].Title = "Modified"

	s := p.GetStep(1)
	if s.Title == "Modified" {
		t.Error("NextReadySteps should return deep copies")
	}
}

// --- Conditional steps ---

func TestStepShouldSkipNoCondition(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	s := p.GetStep(1)
	if s.ShouldSkip(p) {
		t.Error("step without condition should not skip")
	}
}

func TestStepShouldSkipConditionFailed(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.AddStep("Fallback", "")

	// Set condition on step 2: run only if step 1 failed
	p.mu.Lock()
	p.Steps[1].Condition = "step_1_failed"
	p.mu.Unlock()

	s2 := p.GetStep(2)
	// Step 1 is pending (not failed), so condition "step_1_failed" is NOT met → skip
	if !s2.ShouldSkip(p) {
		t.Error("should skip when referenced step is not failed")
	}

	p.StartStep(1)
	p.FailStep(1, "error")
	s2 = p.GetStep(2)
	if s2.ShouldSkip(p) {
		t.Error("should not skip when referenced step IS failed")
	}
}

func TestStepShouldSkipConditionSucceeded(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.AddStep("Post-success", "")

	p.mu.Lock()
	p.Steps[1].Condition = "step_1_succeeded"
	p.mu.Unlock()

	s2 := p.GetStep(2)
	// Step 1 pending — condition not met
	if !s2.ShouldSkip(p) {
		t.Error("should skip when step 1 not completed")
	}
}

// --- Paused steps ---

func TestPlanHasPausedSteps(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")

	if p.HasPausedSteps() {
		t.Error("no paused steps initially")
	}

	p.StartStep(1)
	p.PauseStep(1, "paused")
	if !p.HasPausedSteps() {
		t.Error("should have paused steps")
	}
}

func TestPlanResumePausedSteps(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.AddStep("Step 2", "")

	p.StartStep(1)
	p.PauseStep(1, "checkpoint required")

	count := p.ResumePausedSteps()
	if count != 1 {
		t.Errorf("resumed = %d, want 1", count)
	}

	s := p.GetStep(1)
	if s.Status != StatusPending {
		t.Errorf("resumed step Status = %v", s.Status)
	}
	if s.Error != "" {
		t.Error("error should be cleared after resume")
	}
	if !s.CheckpointPassed {
		t.Error("checkpoint should be passed after resume with 'checkpoint required' reason")
	}
}

// --- Timeouts ---

func TestPlanCheckTimeouts(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStepFull("Step 1", "", false, nil, 0, time.Millisecond)
	p.StartStep(1)

	// Wait for timeout
	time.Sleep(5 * time.Millisecond)

	timedOut := p.CheckTimeouts()
	if len(timedOut) != 1 || timedOut[0] != 1 {
		t.Errorf("timedOut = %v, want [1]", timedOut)
	}

	s := p.GetStep(1)
	if s.Status != StatusFailed {
		t.Errorf("timed out step Status = %v", s.Status)
	}
}

func TestPlanCheckTimeoutsNoTimeout(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.StartStep(1)

	timedOut := p.CheckTimeouts()
	if len(timedOut) != 0 {
		t.Error("step without timeout should not time out")
	}
}

// --- RunLedger ---

func TestPlanRecordStepEffect(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.StartStep(1)

	p.RecordStepEffect(1, "write", map[string]any{
		"file_path": "/src/main.go",
		"content":   "hello",
	})

	snap := p.GetRunLedgerSnapshot()
	entry := snap[1]
	if entry == nil {
		t.Fatal("ledger entry should exist")
	}
	if entry.ToolCalls != 1 {
		t.Errorf("ToolCalls = %d", entry.ToolCalls)
	}
	if len(entry.FilesTouched) != 1 || entry.FilesTouched[0] != "/src/main.go" {
		t.Errorf("FilesTouched = %v", entry.FilesTouched)
	}
}

func TestPlanHasDuplicateRisk(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.StartStep(1)

	if p.HasDuplicateRisk(1) {
		t.Error("should not have duplicate risk initially")
	}

	// Record same effect multiple times
	args := map[string]any{"file_path": "/src/main.go", "content": "x"}
	p.RecordStepEffect(1, "write", args)
	p.RecordStepEffect(1, "write", args)
	p.RecordStepEffect(1, "write", args)

	if !p.HasDuplicateRisk(1) {
		t.Error("should have duplicate risk after 3 identical effects")
	}
}

func TestPlanHasPartialEffects(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")
	p.StartStep(1)

	p.RecordStepEffect(1, "write", map[string]any{"file_path": "/a.go"})

	if !p.HasPartialEffects(1) {
		t.Error("should have partial effects before completion")
	}
}

func TestPlanRecordStepEffectZeroStepID(t *testing.T) {
	p := NewPlan("Test", "")
	p.RecordStepEffect(0, "write", map[string]any{})
	// Should not panic or create entry
	if len(p.GetRunLedgerSnapshot()) != 0 {
		t.Error("zero stepID should not create entry")
	}
}

// --- Verification ---

func TestPlanRecordStepVerification(t *testing.T) {
	p := NewPlan("Test", "")
	p.AddStep("Step 1", "")

	ok := p.RecordStepVerification(1, []string{"  evidence 1  ", "", "evidence 2"}, "  note  ")
	if !ok {
		t.Error("should return true for existing step")
	}

	s := p.GetStep(1)
	if len(s.Evidence) != 2 {
		t.Errorf("Evidence = %v (should filter empty)", s.Evidence)
	}
	if s.VerificationNote != "note" {
		t.Errorf("VerificationNote = %q (should be trimmed)", s.VerificationNote)
	}

	ok = p.RecordStepVerification(99, nil, "")
	if ok {
		t.Error("nonexistent step should return false")
	}
}

// --- Error classification ---

func TestErrorCategoryString(t *testing.T) {
	tests := []struct {
		cat  ErrorCategory
		want string
	}{
		{ErrorTransient, "transient"},
		{ErrorLogic, "logic"},
		{ErrorFatal, "fatal"},
		{ErrorUnknown, "unknown"},
	}
	for _, tt := range tests {
		if got := tt.cat.String(); got != tt.want {
			t.Errorf("ErrorCategory(%d).String() = %q, want %q", tt.cat, got, tt.want)
		}
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		errMsg string
		want   ErrorCategory
	}{
		{"deadline exceeded", context.DeadlineExceeded, "", ErrorTransient},
		{"context canceled", context.Canceled, "", ErrorTransient},
		{"rate limit msg", nil, "Rate limit exceeded", ErrorTransient},
		{"timeout msg", nil, "Request timed out: Timeout", ErrorTransient},
		{"connection refused", nil, "connection refused", ErrorTransient},
		{"eof", nil, "unexpected eof", ErrorTransient},
		{"503", nil, "503 Service Unavailable", ErrorTransient},
		{"429", nil, "429 Too Many Requests", ErrorTransient},
		{"permission denied", nil, "permission denied", ErrorFatal},
		{"not found", nil, "file not found", ErrorFatal},
		{"no such file", nil, "no such file or directory", ErrorFatal},
		{"access denied", nil, "Access Denied", ErrorFatal},
		{"forbidden", nil, "403 Forbidden", ErrorFatal},
		{"validation error", nil, "validation error: field required", ErrorLogic},
		{"invalid argument", nil, "invalid argument for flag", ErrorLogic},
		{"syntax error", nil, "syntax error near token", ErrorLogic},
		{"parse error", nil, "parse error in config", ErrorLogic},
		{"unknown", nil, "something happened", ErrorUnknown},
		{"nil error empty msg", nil, "", ErrorUnknown},
		{"error with no match", errors.New("weird issue"), "", ErrorUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.err, tt.errMsg)
			if got != tt.want {
				t.Errorf("ClassifyError(%v, %q) = %v, want %v", tt.err, tt.errMsg, got, tt.want)
			}
		})
	}
}

// --- EnsureStepContracts ---

func TestPlanEnsureStepContracts(t *testing.T) {
	p := NewPlan("Test", "")
	p.mu.Lock()
	p.Steps = append(p.Steps, &Step{ID: 1, Title: "  Step  "})
	p.RunLedger = nil // Simulate loaded plan without RunLedger
	p.mu.Unlock()

	p.EnsureStepContracts()

	s := p.GetStep(1)
	if s.Title != "Step" {
		t.Errorf("Title not trimmed: %q", s.Title)
	}
	if len(s.VerifyCommands) == 0 {
		t.Error("contract defaults should be applied")
	}

	// RunLedger should be initialized
	snap := p.GetRunLedgerSnapshot()
	if snap == nil {
		t.Error("RunLedger should be initialized")
	}
}

// --- Format ---

func TestPlanRenderTree(t *testing.T) {
	p := NewPlan("My Plan", "A description")
	p.AddStep("Step 1", "first")
	p.AddStepWithOptions("Step 2", "second", true, nil)

	output := p.RenderTree()
	if output == "" {
		t.Error("render should produce output")
	}
	if !contains(output, "My Plan") {
		t.Error("should contain title")
	}
	if !contains(output, "Step 1") {
		t.Error("should contain step 1")
	}
	if !contains(output, "parallel") {
		t.Error("should indicate parallel step")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsSubstr(s, substr)
}

func containsSubstr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
