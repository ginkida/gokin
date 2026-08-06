package app

import (
	"context"
	"fmt"
	"time"

	"gokin/internal/config"
	"gokin/internal/logging"
	"gokin/internal/ui"
)

const (
	stepWatchdogInterval = 20 * time.Second
	stepStuckTimeout     = config.DefaultModelWatchdogFloor
)

func (a *App) planStepStuckBudget() time.Duration {
	modelRound := config.DefaultModelRoundTimeout
	if a != nil && a.executor != nil {
		modelRound = a.executor.ModelRoundTimeout()
	} else if a != nil && a.config != nil && a.config.Tools.ModelRoundTimeout > 0 {
		modelRound = a.config.Tools.ModelRoundTimeout
	}
	return config.ModelWatchdogTimeout(modelRound)
}

func (a *App) touchStepHeartbeat() {
	a.stepHeartbeatMu.Lock()
	a.lastStepHeartbeat = time.Now()
	a.stepHeartbeatMu.Unlock()
}

func (a *App) stepHeartbeatAge() time.Duration {
	a.stepHeartbeatMu.RLock()
	defer a.stepHeartbeatMu.RUnlock()
	if a.lastStepHeartbeat.IsZero() {
		return 0
	}
	return time.Since(a.lastStepHeartbeat)
}

func (a *App) startPlanWatchdog(ctx context.Context, cancel context.CancelFunc, planID string) {
	a.touchStepHeartbeat()
	stuckBudget := a.planStepStuckBudget()

	a.safeGo("plan-watchdog", func() {
		ticker := time.NewTicker(stepWatchdogInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if a.planManager == nil || !a.planManager.IsExecuting() {
					continue
				}

				stepID := a.planManager.GetCurrentStepID()
				if stepID <= 0 {
					continue
				}

				age := a.stepHeartbeatAge()
				if age > 0 && age > stuckBudget {
					logging.Warn("plan step appears stuck; pausing execution",
						"plan_id", planID, "step_id", stepID, "idle", age.String())

					a.planManager.PausePlan()
					a.journalEvent("plan_watchdog_pause", map[string]any{
						"plan_id": planID,
						"step_id": stepID,
						"idle":    age.Round(time.Second).String(),
					})
					if a.reliability != nil {
						a.reliability.RecordFailure()
					}

					a.safeSendToProgram(ui.StreamTextMsg(
						fmt.Sprintf("\n⏸ Step %d paused by watchdog after %v without progress. Use /resume-plan to continue safely.\n",
							stepID, age.Round(time.Second))))
					if a.planManager != nil {
						if p := a.planManager.GetCurrentPlan(); p != nil {
							a.safeSendToProgram(ui.PlanProgressMsg{
								PlanID:        p.ID,
								CurrentStepID: stepID,
								TotalSteps:    p.StepCount(),
								Completed:     p.CompletedCount(),
								Progress:      p.Progress(),
								Status:        "paused",
								Reason:        fmt.Sprintf("watchdog timeout after %v", age.Round(time.Second)),
							})
						}
					}
					cancel()
					return
				}
			}
		}
	})
}
