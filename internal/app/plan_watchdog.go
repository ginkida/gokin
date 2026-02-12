package app

import (
	"context"
	"fmt"
	"time"

	"gokin/internal/logging"
	"gokin/internal/ui"
)

const (
	stepWatchdogInterval = 20 * time.Second
	stepStuckTimeout     = 3 * time.Minute
)

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

	go func() {
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
				if age > 0 && age > stepStuckTimeout {
					logging.Warn("plan step appears stuck; pausing execution",
						"plan_id", planID, "step_id", stepID, "idle", age.String())

					a.planManager.PausePlan()
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
	}()
}
