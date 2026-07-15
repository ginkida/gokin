package context

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"gokin/internal/memory"
)

func TestTaskAwareMemoryReachesSubAgentsAndPlanExecution(t *testing.T) {
	workDir := t.TempDir()
	store, err := memory.NewStore(t.TempDir(), workDir, 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	relevant := memory.NewEntry(
		"AUTH-MEMORY-SENTINEL: authentication integration tests require ./scripts/start-issuer.sh",
		memory.MemoryProject,
	).WithKey("auth-integration-tests")
	relevant.Timestamp = time.Now().Add(-80 * 24 * time.Hour)
	if err := store.Add(relevant); err != nil {
		t.Fatal(err)
	}
	// Fill the generic twelve-entry hot set with newer unrelated notes. The task
	// memory must still be found rather than relying on accidental hot-set recall.
	for i := range 15 {
		entry := memory.NewEntry(
			fmt.Sprintf("Dashboard module-%02d visual preference uses a distinct accent shade", i),
			memory.MemoryProject,
		).WithKey(fmt.Sprintf("dashboard-style-%d", i))
		if err := store.Add(entry); err != nil {
			t.Fatal(err)
		}
	}

	pb := NewPromptBuilder(workDir, &ProjectInfo{Type: ProjectTypeGo, Name: "memory-test"})
	pb.SetMemoryStore(store)
	if generic := pb.BuildSubAgentPrompt(); strings.Contains(generic, "AUTH-MEMORY-SENTINEL") {
		t.Fatalf("test setup: relevant old memory unexpectedly remained in generic hot set:\n%s", generic)
	}

	subAgent := pb.BuildSubAgentPromptForTask("repair authentication integration tests")
	if !strings.Contains(subAgent, "AUTH-MEMORY-SENTINEL") || !strings.Contains(subAgent, "Relevant Memory for This Turn") {
		t.Fatalf("task-aware memory missing from sub-agent prompt:\n%s", subAgent)
	}

	planPrompt := pb.BuildPlanExecutionPrompt(
		"Repair authentication",
		"Make the integration suite reliable",
		[]PlanStepInfo{{ID: 1, Title: "Fix authentication integration tests"}},
	)
	if !strings.Contains(planPrompt, "AUTH-MEMORY-SENTINEL") {
		t.Fatalf("task-aware memory missing from plan execution prompt:\n%s", planPrompt)
	}
}
