package plan

// ProgressUpdate contains information about plan execution progress.
type ProgressUpdate struct {
	PlanID        string
	CurrentStepID int
	CurrentTitle  string
	TotalSteps    int
	Completed     int
	Progress      float64 // 0.0 to 1.0
	Status        string  // "in_progress", "completed", "failed"
	SubStepInfo   string  // Sub-agent progress detail, e.g. "Agent: 3/5 nodes, depth 2"
}
