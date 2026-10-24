package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"gokin/internal/donegate"
	"gokin/internal/permission"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

func (a *App) absentMutationClaim(ctx context.Context, response string) []string {
	paths := donegate.ClaimedMutationPaths(response)
	if len(paths) == 0 {
		if !donegate.ClaimsFileMutation(response) {
			return nil
		}
		paths = []string{"the requested files"}
	}
	denied := donegate.NormalizeTouchedPaths(a.workDir, tools.HeadlessDeniedMutationPaths(ctx))
	if len(denied) > 0 {
		var unexplained []string
		for _, path := range paths {
			normalized := donegate.NormalizeTouchedPaths(a.workDir, []string{path})
			explained := len(normalized) == 1 && slices.Contains(denied, normalized[0])
			if !explained {
				unexplained = append(unexplained, path)
			}
		}
		paths = unexplained
		if len(paths) == 0 {
			return nil
		}
	}
	a.mu.Lock()
	mutations := a.responseMutationCount
	a.mu.Unlock()
	if mutations > 0 {
		return nil
	}
	a.mu.Lock()
	baseline := a.responseClaimBaseline
	a.mu.Unlock()
	var shellWrite bool
	for _, command := range a.snapshotResponseCommands() {
		if !permission.IsReadOnlyBashCommand(command) && !tools.IsVerificationCommand(command) {
			shellWrite = true
			break
		}
	}
	if baseline != nil && !baseline.unchanged(a.workDir, paths, shellWrite) {
		return nil
	}

	return paths
}

// The advisory check runs even when optional self-review is disabled. Its one
// correction exchange shares the invocation's turn/cost caps and is time-bounded.
func (a *App) correctAbsentMutationClaim(ctx context.Context, userMessage string, response *string, usage *turnUsageAccumulator) (handled, ok bool) {
	a.mu.Lock()
	headless := a.headlessRunActive
	a.mu.Unlock()
	if !headless {
		return false, true
	}
	a.mu.Lock()
	requested := a.responseClaimRequested
	a.mu.Unlock()
	if !requestedFileChange(userMessage) && !requested {
		return false, true
	}
	paths := a.absentMutationClaim(ctx, *response)
	if len(paths) == 0 {
		a.mu.Lock()
		a.responseClaimMismatch = false
		a.mu.Unlock()
		return false, true
	}
	a.mu.Lock()
	attempted := a.responseClaimCorrectionAttempted
	denied := a.responsePermissionDenied
	a.responseClaimCorrectionAttempted = true
	a.responseClaimMismatch = true
	a.mu.Unlock()
	rounds, _ := tools.InvocationModelTurnsClaimed(ctx)
	maxTurns, limited := tools.MaxTurnsFromContext(ctx)
	capped := limited && maxTurns > 0 && rounds >= maxTurns
	if !attempted && !denied && !capped && ctx.Err() == nil && !tools.HeadlessPermissionDenied(ctx) {
		prompt := fmt.Sprintf("Claim mismatch: you claimed changes to %s, but this turn recorded zero successful file mutations. Actually perform the requested change, or correct your final answer to say no file was changed. Do not invent edits, diff reviews, checks or permission denials. This is the only claim-correction continuation.\nOriginal request: %s", strings.Join(paths, ", "), userMessage)
		prior := a.session.GetHistory()
		correctionCtx, cancel := context.WithTimeout(ctx, time.Minute)
		if isStructuredOutputCorrection(ctx) {
			// Completion gates and Stop already ran. This advisory may correct
			// the answer only; it must never introduce unfinalized tool effects.
			correctionCtx = tools.ContextWithToolCapabilityCeiling(correctionCtx, []string{})
			prompt = "Claim mismatch: no successful file mutation was recorded. Correct your answer to describe only the work actually performed. Do not call tools or change files; completion checks already finished. Return exactly one JSON value matching the required schema. This is the only claim-correction continuation."
		}
		resumeSteering := a.executor.SuspendUserSteering()
		history, corrected, err := func() ([]*genai.Content, string, error) {
			if isStructuredOutputCorrection(ctx) {
				a.advisoryCorrectionActive.Store(true)
				defer a.advisoryCorrectionActive.Store(false)
			}
			return a.executeTrackedContinuation(correctionCtx, prior, prompt, usage)
		}()
		resumeSteering()
		cancel()
		if len(history) >= len(prior) {
			a.session.SetHistory(history)
			a.applyToolOutputHygiene()
		}
		if err == nil && strings.TrimSpace(corrected) != "" {
			*response = corrected
		}

		paths = a.absentMutationClaim(ctx, *response)
	}
	a.mu.Lock()
	a.responseClaimMismatch = len(paths) > 0
	a.mu.Unlock()

	return true, true
}
