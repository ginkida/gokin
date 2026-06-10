package agent

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/donegate"
	"gokin/internal/logging"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

// Sub-agent done-gate — the full version of the v0.86.3 verify-nudge.
//
// The foreground App gate (app/done_gate.go) blocks finalization and drives
// auto-fix through the executor. Sub-agents get the same shared check
// machinery (internal/donegate) but different semantics, chosen so a gate
// can never brick an agent:
//
//   - the agent loop itself is the auto-fix mechanism: on failure we inject
//     the fix prompt as a user turn and continue the loop instead of
//     re-invoking an executor;
//   - no fail-closed: restricted registries (no bash/verify_code) and exotic
//     worktrees are normal for sub-agents, and a hard block would discard
//     otherwise-useful partial work — zero buildable checks means skip;
//   - bounded: at most agentDoneGateMaxFixCap fix turns regardless of the
//     configured AutoFixAttempts, because fix turns spend the agent's own
//     turn budget;
//   - on exhaustion the agent finishes WITH an honest failure marker in its
//     output (same philosophy as the iteration-cap "may be INCOMPLETE"
//     marker) instead of erroring the whole run.

// agentGateOutcome is the result of one gate evaluation at the
// zero-function-calls break in executeLoop.
type agentGateOutcome int

const (
	agentGateSkipped     agentGateOutcome = iota // disabled / nothing mutated / no checks buildable
	agentGatePassed                              // checks ran and passed (or already passed for this mutation set)
	agentGateFixInjected                         // checks failed, fix turn injected — caller must continue the loop
	agentGateExhausted                           // checks failed and fix budget is spent — marker appended, finish anyway
)

// agentDoneGateMaxFixCap caps fix turns for sub-agents regardless of the
// configured AutoFixAttempts: fix turns consume the agent's own turn budget,
// and a stuck gate must not eat the whole run.
const agentDoneGateMaxFixCap = 2

// SetDoneGatePolicy enables the sub-agent done-gate. Called once at
// configuration time (newConfiguredAgent), before Run.
func (a *Agent) SetDoneGatePolicy(p donegate.Policy) {
	a.stateMu.Lock()
	a.doneGatePolicy = &p
	a.stateMu.Unlock()
}

func (a *Agent) doneGatePolicySnapshot() *donegate.Policy {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.doneGatePolicy
}

func agentDoneGateMaxFixes(p *donegate.Policy) int {
	if p.AutoFixAttempts <= 0 {
		return 0
	}
	if p.AutoFixAttempts > agentDoneGateMaxFixCap {
		return agentDoneGateMaxFixCap
	}
	return p.AutoFixAttempts
}

// recordToolExecution feeds the per-run usage trackers from the tool
// execution chokepoint (executeToolWithReflection). toolsUsed records every
// attempted call (a failed bash run still gave the model the chance to
// verify); touchedPaths records only successful mutations — a failed edit
// changed nothing, so it must not arm the gate.
func (a *Agent) recordToolExecution(toolName string, args map[string]any, result tools.ToolResult) {
	a.toolsMu.Lock()
	defer a.toolsMu.Unlock()
	a.toolsUsed = append(a.toolsUsed, toolName)
	if !result.Success || !donegate.IsMutationTool(toolName) {
		return
	}
	// Every successful mutation bumps the generation — including a re-edit of
	// a file already in touchedSeen, which would not grow touchedPaths. The
	// gate keys re-runs off this so a build-breaking re-edit after a passing
	// gate is still caught.
	a.mutationGen++
	paths := donegate.NormalizeTouchedPaths(a.workDir, donegate.ExtractTouchedPaths(args))
	if len(paths) == 0 {
		return
	}
	if a.touchedSeen == nil {
		a.touchedSeen = make(map[string]bool, len(paths))
	}
	for _, p := range paths {
		if a.touchedSeen[p] {
			continue
		}
		a.touchedSeen[p] = true
		a.touchedPaths = append(a.touchedPaths, p)
	}
}

// GetTouchedPaths returns the files successfully mutated during this run.
func (a *Agent) GetTouchedPaths() []string {
	a.toolsMu.Lock()
	defer a.toolsMu.Unlock()
	out := make([]string, len(a.touchedPaths))
	copy(out, a.touchedPaths)
	return out
}

// mutationGeneration returns the count of successful mutations so far.
func (a *Agent) mutationGeneration() int {
	a.toolsMu.Lock()
	defer a.toolsMu.Unlock()
	return a.mutationGen
}

// runDoneGateAtBreak evaluates the done-gate when the agent is about to
// finish. fixes counts injected fix turns; checkedAtGen remembers the mutation
// generation at the last PASSING run so a later break with no new mutations
// (e.g. after a claim-correction turn that only re-read files) doesn't re-run
// the same checks — while a re-edit of an already-touched file still bumps the
// generation and forces a re-check.
func (a *Agent) runDoneGateAtBreak(ctx context.Context, prompt string, output *strings.Builder, fixes *int, checkedAtGen *int) agentGateOutcome {
	policy := a.doneGatePolicySnapshot()
	if policy == nil || !policy.Enabled {
		return agentGateSkipped
	}
	touched := a.GetTouchedPaths()
	if len(touched) == 0 {
		return agentGateSkipped
	}
	gen := a.mutationGeneration()
	if *checkedAtGen == gen {
		return agentGatePassed
	}
	toolsUsed := a.GetToolsUsed()
	if !donegate.ShouldEnforce(prompt, toolsUsed) {
		return agentGateSkipped
	}

	profile := donegate.DetectProfileWithTouchedPaths(a.workDir, touched)
	checks := donegate.BuildChecks(a.registry, a.workDir, prompt, toolsUsed, profile, *policy)
	if len(checks) == 0 {
		return agentGateSkipped
	}

	logging.Info("sub-agent done-gate: running checks",
		"agent_id", a.ID, "type", a.Type, "checks", len(checks), "touched", len(touched))
	results := donegate.RunChecks(ctx, checks, policy.CheckTimeout, nil, nil)
	if donegate.Passed(results) {
		*checkedAtGen = gen
		logging.Info("sub-agent done-gate passed",
			"agent_id", a.ID, "checks", len(results), "fix_turns", *fixes)
		return agentGatePassed
	}

	var failed []donegate.Result
	for _, r := range results {
		if !r.Success {
			failed = append(failed, r)
		}
	}

	maxFixes := agentDoneGateMaxFixes(policy)
	if *fixes >= maxFixes {
		marker := fmt.Sprintf(
			"\n[done-gate] verification checks are still failing after %d fix attempt(s): %s\n",
			*fixes, donegate.FormatFailedSummary(failed, 3))
		output.WriteString(marker)
		a.safeOnText(marker)
		logging.Warn("sub-agent done-gate: fix budget exhausted, finishing with failure marker",
			"agent_id", a.ID, "failed_checks", len(failed), "fix_turns", *fixes)
		return agentGateExhausted
	}

	*fixes++
	fixPrompt := donegate.BuildFixPrompt(prompt, failed, *fixes, maxFixes)
	a.stateMu.Lock()
	a.history = append(a.history, genai.NewContentFromText(fixPrompt, genai.RoleUser))
	a.stateMu.Unlock()
	logging.Info("sub-agent done-gate: checks failed, fix turn injected",
		"agent_id", a.ID, "failed_checks", len(failed), "attempt", *fixes, "max", maxFixes)
	return agentGateFixInjected
}

// injectClaimCorrectionIfNeeded verifies the agent's final summary against
// git ground truth: paths the text cites that are NOT among the actual
// on-disk changes indicate a silently-failed edit or a hallucinated path.
// Mirrors the App's completion-review claim check. Conservative: only fires
// for agents that mutated code, only on the final turn's text (the
// accumulated multi-turn output legitimately mentions many read-only files),
// and the caller invokes it at most once per run.
func (a *Agent) injectClaimCorrectionIfNeeded(finalText string) bool {
	policy := a.doneGatePolicySnapshot()
	if policy == nil || !policy.Enabled {
		return false
	}
	if strings.TrimSpace(finalText) == "" || len(a.GetTouchedPaths()) == 0 {
		return false
	}
	gitChanged := donegate.DiscoverTouchedPaths(a.workDir)
	falselyClaimed := donegate.DetectFalselyClaimedPaths(finalText, gitChanged)
	if len(falselyClaimed) == 0 {
		return false
	}
	if len(falselyClaimed) > 5 {
		falselyClaimed = falselyClaimed[:5]
	}
	correction := "[guidance] Claim verification against git: your latest summary references files that are NOT among the actual on-disk changes (git diff + staged + untracked): " +
		strings.Join(falselyClaimed, ", ") +
		". Either an edit silently failed or the path is wrong. Re-check (git_status / read), re-apply the change if it was supposed to land, and make the final summary cite only files that really changed."
	a.stateMu.Lock()
	a.history = append(a.history, genai.NewContentFromText(correction, genai.RoleUser))
	a.stateMu.Unlock()
	logging.Info("sub-agent claim verification: correction turn injected",
		"agent_id", a.ID, "falsely_claimed", strings.Join(falselyClaimed, ","))
	return true
}
