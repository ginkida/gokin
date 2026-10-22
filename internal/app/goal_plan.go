package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gokin/internal/client"
	"gokin/internal/goals"
	"gokin/internal/plan"
)

type goalPlanProgressKey struct{}
type goalPlanProgress struct {
	err       error
	completed bool
}

func goalPlanProgressFromContext(ctx context.Context) *goalPlanProgress {
	progress, _ := ctx.Value(goalPlanProgressKey{}).(*goalPlanProgress)
	return progress
}

type goalPlanStoppedError struct{ cause error }

func (err *goalPlanStoppedError) Error() string        { return err.cause.Error() }
func (err *goalPlanStoppedError) Unwrap() error        { return err.cause }
func (*goalPlanStoppedError) AutomaticRetrySafe() bool { return false }

// Only the intentional plan boundary may advance this turn's goal owner. A
// replacement goal, user clear or another conversation change cannot be adopted.
func (a *App) adoptGoalPlanBoundary(ctx context.Context, before conversationLineage) (context.Context, bool) {
	observation := goalObservation(ctx)
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	if a.session.GetID() != before.sessionID || a.recoveryEpoch.Load() != before.epoch+1 {
		return ctx, false
	}
	lineage := conversationLineage{sessionID: before.sessionID, epoch: before.epoch + 1}
	ctx = withConversationLineage(ctx, lineage.sessionID, lineage.epoch)
	if deferred := deferredGoalTurn(ctx); deferred != nil {
		deferred.lineage = &lineage
	}
	goal := a.session.GoalState().Current
	if observation != nil && goal != nil && goal.Status == goals.Active && goal.ID == observation.source.goalID && a.session.GetID() == observation.source.sessionID && a.recoveryEpoch.Load() == observation.source.epoch+1 {
		observation.source.epoch = a.recoveryEpoch.Load()
	}
	return ctx, true
}

func (a *App) finishGoalPlan(ctx context.Context, p *plan.Plan, started time.Time, counter *client.TokenUsageCounter, progress *goalPlanProgress) {
	observation := goalObservation(ctx)
	if observation != nil {
		combined := turnUsageAccumulator{}
		if observation.worker != nil {
			combined = *observation.worker
		}
		input, output := counter.Snapshot()
		combined.input += input
		combined.output += output
		observation.worker = &combined
	}
	if p.GetStatus() != plan.StatusCompleted || progress == nil || !progress.completed || ctx.Err() != nil {
		err := ctx.Err()
		if err == nil && progress != nil {
			err = progress.err
		}
		if err == nil {
			for _, step := range p.GetStepsSnapshot() {
				if step.Error != "" {
					err = fmt.Errorf("plan step %d: %s", step.ID, step.Error)
					break
				}
			}
		}
		if err == nil {
			err = fmt.Errorf("approved plan did not complete; inspect the plan and explicitly resume it")
		}
		err = &goalPlanStoppedError{cause: err}
		if observation != nil {
			observation.finished = true
			a.failGoalTurn(ctx, &observation.source, err)
		}
		a.reportHeadlessStopFailure(ctx, a.activeHeadlessTerminalToken(), err)
		return
	}
	if observation != nil {
		a.sessionLeaseMu.Lock()
		current := a.goalSourceMatchesLocked(&observation.source)
		a.sessionLeaseMu.Unlock()
		if !current {
			observation.finished = true
			return
		}
	}
	summary := a.formatPlanSummary(p, time.Since(started))
	a.session.AddModelMessage(summary + "\n\nPlan step evidence (data):\n" + goalPlanEvidence(p))
	a.recordHeadlessFinalResult(summary)
	a.syncToolCheckpoints()
	if observation != nil {
		a.finishGoalTurn(ctx, summary, observation.worker)
	}
}

func goalPlanEvidence(p *plan.Plan) string {
	type evidenceStep struct {
		ID           int      `json:"id"`
		Title        string   `json:"title"`
		Status       string   `json:"status"`
		Output       string   `json:"output"`
		Evidence     []string `json:"evidence"`
		Verification string   `json:"verification"`
	}
	steps := make([]evidenceStep, 0, p.StepCount())
	for _, step := range p.GetStepsSnapshot() {
		steps = append(steps, evidenceStep{ID: step.ID, Title: step.Title, Status: step.Status.String(), Output: truncateRunesApp(step.Output, 2000), Evidence: step.Evidence, Verification: step.VerificationNote})
	}
	encoded, err := json.Marshal(steps)
	if err != nil {
		return "Plan evidence could not be serialized"
	}
	return string(encoded)
}
