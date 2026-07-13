package agent

import (
	"context"
	"fmt"
	"testing"

	"gokin/internal/tools"
)

// A Coordinator represents one finite batch and Wait returns the result of
// every task in that batch. Completed-task bookkeeping therefore remains live
// until the batch is discarded: areDependenciesMet uses it even much later.
// The old >100-task cleanup deleted arbitrary completed IDs and could strand a
// valid dependent forever when its final prerequisite completed afterward.
func TestCoordinatorLargeGraph_RetainsPrerequisitesUntilDependentsUnblock(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	c := NewCoordinator(context.Background(), runner, &CoordinatorConfig{MaxParallel: 128})

	const earlyPrerequisites = 101
	dependencyIDs := make([]string, 0, earlyPrerequisites+1)
	for i := 0; i < earlyPrerequisites; i++ {
		taskID := fmt.Sprintf("prerequisite-%03d", i)
		agentID := fmt.Sprintf("agent-%03d", i)
		dependencyIDs = append(dependencyIDs, taskID)

		c.tasks[taskID] = &CoordinatedTask{ID: taskID, Status: TaskStatusRunning}
		c.running[agentID] = taskID
		c.dependencies[taskID] = []string{"dependent"}
		runner.results[agentID] = &AgentResult{
			AgentID: agentID, Status: AgentStatusCompleted, Completed: true,
		}
	}

	const finalTaskID = "final-prerequisite"
	const finalAgentID = "final-agent"
	dependencyIDs = append(dependencyIDs, finalTaskID)
	c.tasks[finalTaskID] = &CoordinatedTask{ID: finalTaskID, Status: TaskStatusRunning}
	c.dependencies[finalTaskID] = []string{"dependent"}
	c.tasks["dependent"] = &CoordinatedTask{
		ID: "dependent", Status: TaskStatusBlocked, Dependencies: dependencyIDs,
	}

	// First wave crosses the old cleanup threshold, while the final prerequisite
	// is deliberately not yet visible as a running/completed agent.
	c.checkCompletedAgents()
	if got := len(c.completed); got != earlyPrerequisites {
		t.Fatalf("retained completed prerequisites = %d, want %d", got, earlyPrerequisites)
	}
	if status := c.tasks["dependent"].Status; status != TaskStatusBlocked {
		t.Fatalf("dependent status after first wave = %s, want blocked", status)
	}

	runner.results[finalAgentID] = &AgentResult{
		AgentID: finalAgentID, Status: AgentStatusCompleted, Completed: true,
	}
	c.running[finalAgentID] = finalTaskID
	c.checkCompletedAgents()

	if status := c.tasks["dependent"].Status; status != TaskStatusReady {
		t.Fatalf("dependent status after final prerequisite = %s, want ready", status)
	}
	if got := len(c.completed); got != earlyPrerequisites+1 {
		t.Fatalf("completed prerequisite count = %d, want %d", got, earlyPrerequisites+1)
	}
	if got := len(c.tasks); got != earlyPrerequisites+2 {
		t.Fatalf("retained task count = %d, want %d", got, earlyPrerequisites+2)
	}
}
