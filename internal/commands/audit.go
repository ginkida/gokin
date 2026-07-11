package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"gokin/internal/agent"
)

// AuditRunner is the narrow Runner surface /audit needs: fan out N agents in
// parallel and block until they're all done, then read each one's result.
// *agent.Runner already implements this signature-for-signature (SpawnMultiple
// + GetResult), so App.GetAuditRunner can return it directly. Interface (not
// *agent.Runner) so command tests can fake it, same pattern as
// AgentTaskRunner/BackgroundShellRunner.
type AuditRunner interface {
	SpawnMultiple(ctx context.Context, tasks []agent.AgentTask) ([]string, error)
	GetResult(agentID string) (*agent.AgentResult, bool)
}

// AuditCommand runs a hardcoded find→adversarially-verify→synthesize recipe
// over the current changes (or a given scope) using gokin's OWN multi-agent
// infrastructure — the same shape as the manual audit rounds this codebase's
// own CLAUDE.md documents (rounds 1-14: parallel finders by lens, one
// skeptical verifier per finding, defaulting to REFUTED), just productized
// as a command instead of an ad-hoc external orchestration.
//
// Deliberately NOT a user-scriptable workflow engine: the recipe is fixed Go
// code, not a script the model writes, because gokin's primary providers
// (GLM/deepseek Coding-Plan, often quota-capped at a few hours) and weaker
// models (kimi/minimax/ollama) can't reliably author/execute an open-ended
// multi-agent orchestration the way a frontier model can. A fixed recipe
// with a small, predictable agent budget (auditLenses + up to
// auditMaxVerifiers agents) is the tradeoff that actually works within those
// constraints — see the CLAUDE.md discussion this command's design follows.
type AuditCommand struct{}

func (c *AuditCommand) Name() string { return "audit" }
func (c *AuditCommand) Description() string {
	return "Multi-agent find→verify audit of the current changes (or a given scope) for real bugs"
}
func (c *AuditCommand) Usage() string { return "/audit [path or description]" }
func (c *AuditCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category:         CategorySession,
		Icon:             "audit",
		Priority:         66,
		HasArgs:          true,
		ArgHint:          "[path]",
		LongRunning:      true,
		LongRunningLabel: fmt.Sprintf("Auditing (%d finders, then verifying)...", len(auditLenses)),
	}
}

// auditLenses are the fixed finder dimensions — the same 3 angles this
// project's own manual audit rounds have used repeatedly (CLAUDE.md rounds
// 1-14): correctness, security, and concurrency/reliability. Fixed (not
// user-configurable) to keep the agent budget small and predictable.
var auditLenses = []struct {
	name   string
	prompt string
}{
	{
		name:   "correctness",
		prompt: "Correctness bugs: logic errors, off-by-one, wrong conditionals, incorrect behavior under edge-case or hostile inputs, crashes.",
	},
	{
		name:   "security",
		prompt: "Security issues: injection (command/SQL/path), path traversal, unsafe deserialization, secrets or credentials handled unsafely, missing validation at a trust boundary.",
	},
	{
		name:   "reliability",
		prompt: "Concurrency and reliability issues: data races, deadlocks, panics on nil/empty input, resource leaks (goroutines, file handles, contexts never cancelled), unchecked errors that silently corrupt state.",
	},
}

const (
	// auditFinderMaxTurns bounds a finder's tool budget — enough to read a
	// diff and grep around it, not enough to explore an entire repo.
	auditFinderMaxTurns = 8
	// auditVerifierMaxTurns bounds a verifier's tool budget — it only needs
	// to read the specific code a finding cites.
	auditVerifierMaxTurns = 4
	// auditMaxRawFindings caps how many raw candidates advance to the verify
	// phase, bounding total agent count (and cost) regardless of how many a
	// finder reports.
	auditMaxRawFindings = 12
)

// findingLineRe matches a finder's `FINDING: file:line | summary | scenario`
// output line. Lenient: line number and the trailing scenario are optional,
// so a weaker model that drops a field still parses.
var findingLineRe = regexp.MustCompile(`(?i)^\s*FINDING:\s*(.+?)(?:\s*\|\s*(.+?))?(?:\s*\|\s*(.+))?\s*$`)

// verdictRe matches a verifier's `VERDICT: CONFIRMED|REFUTED ...` line.
var verdictRe = regexp.MustCompile(`(?i)VERDICT:\s*(CONFIRMED|REFUTED)`)

type auditFinding struct {
	lens     string
	location string // "file:line" or a free-form location the model reported
	summary  string
	scenario string
}

func (c *AuditCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	runner := app.GetAuditRunner()
	if runner == nil {
		return "Audit is unavailable in this build (agent runner not wired).", nil
	}

	scope := strings.TrimSpace(strings.Join(args, " "))
	var scopeDesc string
	if scope == "" {
		scopeDesc = "the CURRENT uncommitted changes in this git repository (run `git diff` and `git status` yourself to see them; if there are none, audit the most recently modified source files instead)"
	} else {
		scopeDesc = fmt.Sprintf("%q", scope)
	}

	// --- Find phase: one agent per lens, run in parallel. ---
	findTasks := make([]agent.AgentTask, len(auditLenses))
	for i, lens := range auditLenses {
		findTasks[i] = agent.AgentTask{
			Type:     agent.AgentTypeExplore,
			MaxTurns: auditFinderMaxTurns,
			Prompt:   buildFinderPrompt(scopeDesc, lens.prompt),
		}
	}
	findIDs, _ := runner.SpawnMultiple(ctx, findTasks)
	if err := ctx.Err(); err != nil {
		return "", err
	}

	var raw []auditFinding
	var lensesRun int
	for i, id := range findIDs {
		res, ok := runner.GetResult(id)
		if !ok || res == nil {
			continue
		}
		lensesRun++
		found := parseFindings(auditLenses[i].name, res.Output)
		raw = append(raw, found...)
	}

	if len(raw) == 0 {
		return fmt.Sprintf("Audit found no candidate issues (%d lens(es) checked: %s).",
			lensesRun, lensNames()), nil
	}

	truncatedCount := 0
	if len(raw) > auditMaxRawFindings {
		truncatedCount = len(raw) - auditMaxRawFindings
		raw = raw[:auditMaxRawFindings]
	}

	// --- Verify phase: one skeptical verifier per candidate, in parallel. ---
	verifyTasks := make([]agent.AgentTask, len(raw))
	for i, f := range raw {
		verifyTasks[i] = agent.AgentTask{
			Type:     agent.AgentTypeExplore,
			MaxTurns: auditVerifierMaxTurns,
			Prompt:   buildVerifierPrompt(scopeDesc, f),
		}
	}
	verifyIDs, _ := runner.SpawnMultiple(ctx, verifyTasks)
	if err := ctx.Err(); err != nil {
		return "", err
	}

	var confirmed []auditFinding
	for i, id := range verifyIDs {
		res, ok := runner.GetResult(id)
		if !ok || res == nil {
			continue // an agent that never produced a result is NOT confirmed
		}
		if isConfirmed(res.Output) {
			confirmed = append(confirmed, raw[i])
		}
	}

	return renderAuditReport(scopeDesc, len(raw), truncatedCount, confirmed), nil
}

func lensNames() string {
	names := make([]string, len(auditLenses))
	for i, l := range auditLenses {
		names[i] = l.name
	}
	return strings.Join(names, ", ")
}

func buildFinderPrompt(scopeDesc, lens string) string {
	return fmt.Sprintf(`You are one lens of a multi-agent code audit. Investigate %s using your read-only tools (read, grep, glob, and bash for read-only commands like git diff/git status/go vet — do not modify anything).

Your lens: %s

Be selective — only report something you are FAIRLY CONFIDENT is a real, concrete problem, not a style preference. For each real issue found, output ONE line in EXACTLY this format (one finding per line, no other text around it):

FINDING: <file:line> | <one-sentence summary of the defect> | <concrete failure scenario: specific input/state -> wrong output or crash>

If you find nothing worth reporting for this lens, output exactly: NO FINDINGS`, scopeDesc, lens)
}

func buildVerifierPrompt(scopeDesc string, f auditFinding) string {
	return fmt.Sprintf(`You are an ADVERSARIAL verifier for one claimed finding from a code audit of %s. Your default assumption is that the claim is WRONG (audits over-report). Read the actual code at the cited location and trace through the claimed failure scenario yourself before deciding.

Claimed location: %s
Claimed defect: %s
Claimed failure scenario: %s

Reply with EXACTLY one line: "VERDICT: CONFIRMED" only if you personally traced the code and the failure scenario is real, or "VERDICT: REFUTED" otherwise — followed by a one-sentence reason.`,
		scopeDesc, f.location, f.summary, f.scenario)
}

// parseFindings extracts FINDING lines from a finder's raw output. Lenient
// by design (matches the project's weak-model-tuning philosophy): if the
// model didn't follow the delimited format but clearly reported something
// (non-empty output that isn't the literal "NO FINDINGS"), the whole output
// is kept as one finding with an unknown location rather than silently
// dropping a real signal a weaker model failed to format correctly.
func parseFindings(lens, output string) []auditFinding {
	output = strings.TrimSpace(output)
	if output == "" || strings.EqualFold(output, "NO FINDINGS") {
		return nil
	}

	var findings []auditFinding
	for _, line := range strings.Split(output, "\n") {
		m := findingLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		findings = append(findings, auditFinding{
			lens:     lens,
			location: strings.TrimSpace(m[1]),
			summary:  strings.TrimSpace(m[2]),
			scenario: strings.TrimSpace(m[3]),
		})
	}

	if len(findings) == 0 {
		// Didn't follow the format but said SOMETHING non-trivial — keep it
		// as a single low-confidence finding rather than discarding signal.
		return []auditFinding{{lens: lens, location: "(see agent output)", summary: firstLineTrimmed(output, 200)}}
	}
	return findings
}

// isConfirmed reads a verifier's verdict. Defaults to NOT confirmed on any
// ambiguity (missing/malformed verdict, "REFUTED") — the whole point of the
// adversarial-verify step is that uncertainty should lose, not win.
func isConfirmed(output string) bool {
	m := verdictRe.FindStringSubmatch(output)
	if m == nil {
		return false
	}
	return strings.EqualFold(m[1], "CONFIRMED")
}

func renderAuditReport(scopeDesc string, rawCount, truncatedCount int, confirmed []auditFinding) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Audit of %s\n", scopeDesc)
	fmt.Fprintf(&sb, "%d candidate(s) found, %d confirmed after adversarial verification.\n", rawCount, len(confirmed))
	if truncatedCount > 0 {
		fmt.Fprintf(&sb, "(%d additional raw candidate(s) skipped — capped at %d before verification)\n", truncatedCount, auditMaxRawFindings)
	}
	sb.WriteString("\n")

	if len(confirmed) == 0 {
		sb.WriteString("No findings survived verification — nothing actionable found.")
		return sb.String()
	}

	for i, f := range confirmed {
		fmt.Fprintf(&sb, "%d. [%s] %s\n", i+1, f.lens, f.location)
		if f.summary != "" {
			fmt.Fprintf(&sb, "   %s\n", f.summary)
		}
		if f.scenario != "" {
			fmt.Fprintf(&sb, "   Scenario: %s\n", f.scenario)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
