package commands

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/agent"
)

// fakeAuditRunner scripts SpawnMultiple/GetResult for the /audit command.
// Each call to SpawnMultiple returns synthetic agent IDs from a queued
// batch (find phase, then verify phase); GetResult looks up by ID.
type fakeAuditRunner struct {
	batches [][]string // ordered agent-ID batches, one per SpawnMultiple call
	call    int        // how many SpawnMultiple calls have happened
	results map[string]*agent.AgentResult
	prompts []string // every prompt passed to SpawnMultiple, in call order
}

func (f *fakeAuditRunner) SpawnMultiple(ctx context.Context, tasks []agent.AgentTask) ([]string, error) {
	for _, t := range tasks {
		f.prompts = append(f.prompts, t.Prompt)
	}
	if f.call >= len(f.batches) {
		return nil, nil
	}
	ids := f.batches[f.call]
	f.call++
	return ids, nil
}

func (f *fakeAuditRunner) GetResult(id string) (*agent.AgentResult, bool) {
	r, ok := f.results[id]
	return r, ok
}

type fakeAppForAudit struct {
	*fakeAppForMCP
	runner AuditRunner
}

func (f *fakeAppForAudit) GetAuditRunner() AuditRunner { return f.runner }

func newAuditApp(runner AuditRunner) *fakeAppForAudit {
	return &fakeAppForAudit{fakeAppForMCP: &fakeAppForMCP{}, runner: runner}
}

func TestAudit_NilRunnerReportsUnavailable(t *testing.T) {
	cmd := &AuditCommand{}
	out, err := cmd.Execute(context.Background(), nil, newAuditApp(nil))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "unavailable") {
		t.Fatalf("output = %q, want unavailable message", out)
	}
}

// TestAudit_NoFindingsFastPath: when every finder reports NO FINDINGS, the
// verify phase must never run (SpawnMultiple called exactly once).
func TestAudit_NoFindingsFastPath(t *testing.T) {
	runner := &fakeAuditRunner{
		batches: [][]string{{"f1", "f2", "f3"}},
		results: map[string]*agent.AgentResult{
			"f1": {Output: "NO FINDINGS"},
			"f2": {Output: "no findings"}, // case-insensitive
			"f3": {Output: ""},
		},
	}
	cmd := &AuditCommand{}
	out, err := cmd.Execute(context.Background(), nil, newAuditApp(runner))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no candidate issues") {
		t.Fatalf("expected the fast-path no-findings message, got: %q", out)
	}
	if runner.call != 1 {
		t.Fatalf("verify phase must not run when there is nothing to verify, SpawnMultiple called %d times", runner.call)
	}
}

// TestAudit_FindThenVerify_OnlyConfirmedSurvive is the core end-to-end
// behavior: raw findings from the find phase are only reported if their
// verifier says CONFIRMED; REFUTED and unparseable verdicts are dropped.
func TestAudit_FindThenVerify_OnlyConfirmedSurvive(t *testing.T) {
	runner := &fakeAuditRunner{
		batches: [][]string{
			{"find-correctness", "find-security", "find-reliability"}, // find phase
			{"verify-1", "verify-2"},                                  // verify phase (2 raw findings total)
		},
		results: map[string]*agent.AgentResult{
			"find-correctness": {Output: "FINDING: foo.go:10 | off-by-one in the loop bound | loop reads one past the slice end and panics"},
			"find-security":    {Output: "FINDING: bar.go:22 | command built from unsanitized input | user input reaches exec.Command unescaped"},
			"find-reliability": {Output: "NO FINDINGS"},
			"verify-1":         {Output: "VERDICT: CONFIRMED — traced it, the bound really is off by one"},
			"verify-2":         {Output: "VERDICT: REFUTED — the input is validated two lines earlier"},
		},
	}
	cmd := &AuditCommand{}
	out, err := cmd.Execute(context.Background(), nil, newAuditApp(runner))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "foo.go:10") {
		t.Fatalf("confirmed finding must appear in the report:\n%s", out)
	}
	if !strings.Contains(out, "off-by-one") {
		t.Fatalf("confirmed finding's summary must appear:\n%s", out)
	}
	if strings.Contains(out, "bar.go:22") {
		t.Fatalf("refuted finding must NOT appear in the report:\n%s", out)
	}
	if !strings.Contains(out, "2 candidate(s) found, 1 confirmed") {
		t.Fatalf("report must state the raw vs confirmed counts, got:\n%s", out)
	}
	if runner.call != 2 {
		t.Fatalf("expected exactly 2 SpawnMultiple calls (find, verify), got %d", runner.call)
	}
}

// TestAudit_VerifierWithNoResultIsNotConfirmed guards a fail-safe: an agent
// that never produced a result (spawn error, panic) must NOT count as
// confirmed — ambiguity must lose, matching the adversarial-verify default.
func TestAudit_VerifierWithNoResultIsNotConfirmed(t *testing.T) {
	runner := &fakeAuditRunner{
		batches: [][]string{
			{"find-1", "find-2", "find-3"},
			{"verify-missing"},
		},
		results: map[string]*agent.AgentResult{
			"find-1": {Output: "FINDING: x.go:1 | issue | scenario"},
			"find-2": {Output: "NO FINDINGS"},
			"find-3": {Output: "NO FINDINGS"},
			// "verify-missing" deliberately absent from results.
		},
	}
	cmd := &AuditCommand{}
	out, err := cmd.Execute(context.Background(), nil, newAuditApp(runner))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "0 confirmed") {
		t.Fatalf("a missing verifier result must not be treated as confirmed:\n%s", out)
	}
}

// TestAudit_UnformattedOutputStillCapturedAsLowConfidenceFinding pins the
// weak-model graceful-degradation path: a finder that ignores the FINDING:
// format but clearly reports something must not have its signal silently
// dropped.
func TestAudit_UnformattedOutputStillCapturedAsLowConfidenceFinding(t *testing.T) {
	runner := &fakeAuditRunner{
		batches: [][]string{
			{"f1", "f2", "f3"},
			{"v1"},
		},
		results: map[string]*agent.AgentResult{
			"f1": {Output: "I noticed that the retry loop in client.go never resets the counter, which could loop forever."},
			"f2": {Output: "NO FINDINGS"},
			"f3": {Output: "NO FINDINGS"},
			"v1": {Output: "VERDICT: CONFIRMED"},
		},
	}
	cmd := &AuditCommand{}
	out, err := cmd.Execute(context.Background(), nil, newAuditApp(runner))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "retry loop") {
		t.Fatalf("unformatted-but-real signal must survive as a finding:\n%s", out)
	}
}

// TestAudit_RawFindingsCappedBeforeVerify bounds total agent cost: more raw
// findings than auditMaxRawFindings must be truncated before the verify
// phase, and the report must disclose how many were dropped.
func TestAudit_RawFindingsCappedBeforeVerify(t *testing.T) {
	manyFindings := strings.Repeat("FINDING: f.go:1 | issue | scenario\n", auditMaxRawFindings+5)
	verifyIDs := make([]string, auditMaxRawFindings)
	results := map[string]*agent.AgentResult{
		"f1": {Output: manyFindings},
		"f2": {Output: "NO FINDINGS"},
		"f3": {Output: "NO FINDINGS"},
	}
	for i := range verifyIDs {
		id := verifyIDFor(i)
		verifyIDs[i] = id
		results[id] = &agent.AgentResult{Output: "VERDICT: REFUTED"}
	}
	runner := &fakeAuditRunner{
		batches: [][]string{{"f1", "f2", "f3"}, verifyIDs},
		results: results,
	}
	cmd := &AuditCommand{}
	out, err := cmd.Execute(context.Background(), nil, newAuditApp(runner))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "5 additional raw candidate(s) skipped") {
		t.Fatalf("report must disclose the truncation, got:\n%s", out)
	}
	if len(runner.prompts) != 3+auditMaxRawFindings {
		t.Fatalf("verify phase must spawn at most auditMaxRawFindings(%d) verifiers, total prompts = %d", auditMaxRawFindings, len(runner.prompts))
	}
}

func verifyIDFor(i int) string {
	return "verify-capped-" + string(rune('a'+i))
}

// TestParseFindings_MultipleLinesAndDelimiters exercises the parser directly
// against realistic multi-finding output.
func TestParseFindings_MultipleLinesAndDelimiters(t *testing.T) {
	output := "Some preamble the model shouldn't add.\n" +
		"FINDING: a.go:5 | nil deref on empty input | calling Foo(nil) panics\n" +
		"FINDING: b.go:12 | missing bound check\n" + // no scenario field
		"NO FINDINGS trailing noise" // must not match as a finding line by itself

	got := parseFindings("correctness", output)
	if len(got) != 2 {
		t.Fatalf("expected 2 findings, got %d: %+v", len(got), got)
	}
	if got[0].location != "a.go:5" || got[0].summary != "nil deref on empty input" || got[0].scenario == "" {
		t.Fatalf("first finding parsed wrong: %+v", got[0])
	}
	if got[1].location != "b.go:12" || got[1].summary != "missing bound check" {
		t.Fatalf("second finding parsed wrong: %+v", got[1])
	}
}
