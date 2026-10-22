package app

import "context"

type goalTurnDeferralKey struct{}

// A schema correction is part of the worker's terminal answer. Keep the goal
// verdict unpublished until that answer is valid; correction exchanges neither
// resume a paused goal nor start another work/evaluation cycle themselves.
type goalTurnDeferral struct {
	observation                       *goalTurnObservation
	worker                            *turnUsageAccumulator
	ready                             bool
	lineage                           *conversationLineage
	correctionInput, correctionOutput int
	err                               error
}

func deferredGoalTurn(ctx context.Context) *goalTurnDeferral {
	deferred, _ := ctx.Value(goalTurnDeferralKey{}).(*goalTurnDeferral)
	return deferred
}

func (a *App) finalizeDeferredGoalTurn(ctx context.Context, deferred *goalTurnDeferral, response string, err error) {
	if deferred == nil || !deferred.ready || deferred.observation == nil {
		return
	}
	combined := turnUsageAccumulator{}
	if deferred.worker != nil {
		combined = *deferred.worker
	}
	combined.input += deferred.correctionInput
	combined.output += deferred.correctionOutput
	deferred.observation.worker = &combined
	ctx = context.WithValue(ctx, goalTurnDeferralKey{}, (*goalTurnDeferral)(nil))
	ctx = context.WithValue(ctx, goalTurnObservationKey{}, deferred.observation)
	if err != nil {
		a.failGoalTurn(ctx, &deferred.observation.source, err)
		return
	}
	a.finishGoalTurn(ctx, response, &combined)
}
