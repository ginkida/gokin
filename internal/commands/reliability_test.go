package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/config"
)

// fakeAppForReports embeds fakeAppForMCP and overrides the report getters
// with recorded strings, so the reliability commands can be exercised without
// a real observability stack.
type fakeAppForReports struct {
	fakeAppForMCP
	health        string
	policy        string
	ledger        string
	planProof     string
	journal       string
	recovery      string
	observability string
	governance    string
}

func (f *fakeAppForReports) GetRuntimeHealthReport() string     { return f.health }
func (f *fakeAppForReports) GetPolicyReport() string            { return f.policy }
func (f *fakeAppForReports) GetLedgerReport() string            { return f.ledger }
func (f *fakeAppForReports) GetPlanProofReport(int) string      { return f.planProof }
func (f *fakeAppForReports) GetJournalReport() string           { return f.journal }
func (f *fakeAppForReports) GetRecoveryReport() string          { return f.recovery }
func (f *fakeAppForReports) GetObservabilityReport() string     { return f.observability }
func (f *fakeAppForReports) GetSessionGovernanceReport() string { return f.governance }

// ─── reliability.go: the 8 report-delegating commands ────────────────────────
// Each is a thin wrapper that calls a single app getter. We verify the command
// (a) returns the getter's string verbatim, (b) never errors, and (c) has the
// expected Name/Usage/GetMetadata — covering the 0% metadata arms too.

func TestHealthCommand(t *testing.T) {
	c := &HealthCommand{}
	if c.Name() != "health" {
		t.Errorf("Name = %q", c.Name())
	}
	if c.Usage() == "" {
		t.Error("Usage empty")
	}
	md := c.GetMetadata()
	if md.Category != CategoryPlanning || md.Icon != "heartbeat" {
		t.Errorf("metadata = %+v", md)
	}
	app := &fakeAppForReports{health: "all-green"}
	out, err := c.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatal(err)
	}
	if out != "all-green" {
		t.Errorf("Execute = %q, want %q", out, "all-green")
	}
}

func TestPolicyCommand(t *testing.T) {
	c := &PolicyCommand{}
	if c.Name() != "policy" {
		t.Errorf("Name = %q", c.Name())
	}
	if c.Usage() == "" {
		t.Error("Usage empty")
	}
	md := c.GetMetadata()
	if md.Category != CategoryPlanning {
		t.Errorf("metadata = %+v", md)
	}
	app := &fakeAppForReports{policy: "policy-state"}
	out, err := c.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatal(err)
	}
	if out != "policy-state" {
		t.Errorf("Execute = %q", out)
	}
}

func TestLedgerCommand(t *testing.T) {
	c := &LedgerCommand{}
	if c.Name() != "ledger" {
		t.Errorf("Name = %q", c.Name())
	}
	app := &fakeAppForReports{ledger: "ledger-data"}
	out, err := c.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatal(err)
	}
	if out != "ledger-data" {
		t.Errorf("Execute = %q", out)
	}
}

func TestJournalCommand(t *testing.T) {
	c := &JournalCommand{}
	if c.Name() != "journal" {
		t.Errorf("Name = %q", c.Name())
	}
	app := &fakeAppForReports{journal: "journal-events"}
	out, err := c.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatal(err)
	}
	if out != "journal-events" {
		t.Errorf("Execute = %q", out)
	}
}

func TestRecoveryCommand(t *testing.T) {
	c := &RecoveryCommand{}
	if c.Name() != "recovery" {
		t.Errorf("Name = %q", c.Name())
	}
	app := &fakeAppForReports{recovery: "snapshot"}
	out, err := c.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatal(err)
	}
	if out != "snapshot" {
		t.Errorf("Execute = %q", out)
	}
}

func TestObservabilityCommand(t *testing.T) {
	c := &ObservabilityCommand{}
	if c.Name() != "observability" {
		t.Errorf("Name = %q", c.Name())
	}
	app := &fakeAppForReports{observability: "dashboard"}
	out, err := c.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatal(err)
	}
	if out != "dashboard" {
		t.Errorf("Execute = %q", out)
	}
}

func TestMemoryGovernanceCommand(t *testing.T) {
	c := &MemoryGovernanceCommand{}
	if c.Name() != "memory-governance" {
		t.Errorf("Name = %q", c.Name())
	}
	app := &fakeAppForReports{governance: "mem-gov"}
	out, err := c.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatal(err)
	}
	if out != "mem-gov" {
		t.Errorf("Execute = %q", out)
	}
}

// ─── PlanProofCommand: the only one with real arg-parsing logic ──────────────

func TestPlanProofCommand_Metadata(t *testing.T) {
	c := &PlanProofCommand{}
	if c.Name() != "plan-proof" {
		t.Errorf("Name = %q", c.Name())
	}
	if c.Usage() == "" {
		t.Error("Usage empty")
	}
	md := c.GetMetadata()
	if md.Category != CategoryPlanning || !md.HasArgs {
		t.Errorf("metadata = %+v", md)
	}
}

func TestPlanProofCommand_NoArgs(t *testing.T) {
	app := &fakeAppForReports{planProof: "step-0-proof"}
	out, err := (&PlanProofCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatal(err)
	}
	if out != "step-0-proof" {
		t.Errorf("no-args should default to step 0, got %q", out)
	}
}

func TestPlanProofCommand_ValidStepID(t *testing.T) {
	app := &fakeAppForReports{planProof: "step-5-proof"}
	out, err := (&PlanProofCommand{}).Execute(context.Background(), []string{"5"}, app)
	if err != nil {
		t.Fatal(err)
	}
	if out != "step-5-proof" {
		t.Errorf("valid step_id should delegate, got %q", out)
	}
}

func TestPlanProofCommand_InvalidStepID(t *testing.T) {
	app := &fakeAppForReports{planProof: "should-not-reach"}
	cases := [][]string{
		{"abc"},  // non-numeric
		{"-1"},   // negative
		{"  "},   // whitespace
		{"3.14"}, // float
	}
	for _, args := range cases {
		out, err := c_PlanProofExecute(args, app)
		if err != nil {
			t.Fatalf("invalid step_id should not hard-error: %v", err)
		}
		if !strings.Contains(out, "Invalid step_id") {
			t.Errorf("args=%v: expected invalid message, got %q", args, out)
		}
	}
}

func TestPlanProofCommand_TooManyArgs(t *testing.T) {
	app := &fakeAppForReports{planProof: "x"}
	out, err := (&PlanProofCommand{}).Execute(context.Background(), []string{"1", "2"}, app)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Usage:") {
		t.Errorf("too many args should print usage, got %q", out)
	}
}

// c_PlanProofExecute is a thin wrapper to allow table-driven invalid-arg tests.
func c_PlanProofExecute(args []string, app *fakeAppForReports) (string, error) {
	return (&PlanProofCommand{}).Execute(context.Background(), args, app)
}

// ─── auth.go: StatusCommand + helpers ────────────────────────────────────────

// fakeAppForStatus provides just enough for StatusCommand: a config, workdir,
// version, and allowed-dirs list. Embeds fakeAppForMCP for the rest.
type fakeAppForStatus struct {
	fakeAppForMCP
	cfg       *config.Config
	workDir   string
	version   string
	allowDirs []string
}

func (f *fakeAppForStatus) GetConfig() *config.Config { return f.cfg }
func (f *fakeAppForStatus) GetWorkDir() string        { return f.workDir }
func (f *fakeAppForStatus) GetVersion() string        { return f.version }
func (f *fakeAppForStatus) ListAllowedDirs() []string { return f.allowDirs }

func TestStatusCommand_Metadata(t *testing.T) {
	c := &StatusCommand{}
	if c.Name() != "status" {
		t.Errorf("Name = %q", c.Name())
	}
	if c.Usage() == "" {
		t.Error("Usage empty")
	}
	md := c.GetMetadata()
	if md.Category != CategoryAuthSetup {
		t.Errorf("metadata = %+v", md)
	}
}

func TestStatusCommand_NilConfig(t *testing.T) {
	app := &fakeAppForStatus{cfg: nil}
	out, err := (&StatusCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatal(err)
	}
	if out != "Failed to get configuration." {
		t.Errorf("nil-config message = %q", out)
	}
}

func TestStatusCommand_Success(t *testing.T) {
	app := &fakeAppForStatus{
		cfg:       &config.Config{},
		workDir:   "/home/me/project",
		version:   "v1.0.0",
		allowDirs: []string{"/extra/dir"},
	}
	out, err := (&StatusCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Configuration Status") {
		t.Errorf("missing header: %s", out)
	}
	if !strings.Contains(out, "Provider:") {
		t.Errorf("missing provider line: %s", out)
	}
	if !strings.Contains(out, "v1.0.0") {
		t.Errorf("missing version: %s", out)
	}
	if !strings.Contains(out, "/extra/dir") {
		t.Errorf("missing granted dir: %s", out)
	}
}

// ─── auth.go helpers: maskKey, sanitizePastedKey, padSpaces ──────────────────

func TestMaskKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "****"},
		{"short", "****"},            // len <= 8
		{"12345678", "****"},         // len == 8, boundary
		{"123456789", "1234...6789"}, // len == 9, first >8 branch
		{"sk-abc-1234567890-xyz", "sk-a...-xyz"},
	}
	for _, tc := range cases {
		got := maskKey(tc.in)
		if got != tc.want {
			t.Errorf("maskKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizePastedKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"  sk-kimi-abc  ", "sk-kimi-abc"},       // surrounding whitespace
		{"\"sk-kimi-abc\"", "sk-kimi-abc"},       // double quotes
		{"'sk-kimi-abc'", "sk-kimi-abc"},         // single quotes
		{"  \"sk-kimi-abc\"  \n", "sk-kimi-abc"}, // quotes + whitespace
		{"sk-kimi-abc", "sk-kimi-abc"},           // no artifacts
		{"", ""},                                 // empty
		{"   ", ""},                              // only whitespace
		{"'unmatched\"", "'unmatched\""},         // mismatched quotes → unchanged
		{"\"\"", ""},                             // empty quotes
	}
	for _, tc := range cases {
		got := sanitizePastedKey(tc.in)
		if got != tc.want {
			t.Errorf("sanitizePastedKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPadSpaces(t *testing.T) {
	if got := padSpaces(0); got != " " {
		t.Errorf("padSpaces(0) = %q, want %q", got, " ")
	}
	if got := padSpaces(-5); got != " " {
		t.Errorf("padSpaces(-5) = %q, want %q", got, " ")
	}
	if got := padSpaces(3); got != "   " {
		t.Errorf("padSpaces(3) = %q, want %q", got, "   ")
	}
}

// ─── builtin.go: prettyHomePath ──────────────────────────────────────────────
// The $HOME-collapse branch is covered by TestPrettyHomePath in
// doctor_polish_test.go (which can't mock $HOME). Here we add the
// under-home positive case, which that test can't assert without knowing $HOME.

func TestPrettyHomePath_HomeSubpath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("cannot determine $HOME")
	}
	// Path under home → ~ prefix.
	in := filepath.Join(home, "project", "src")
	got := prettyHomePath(in)
	want := filepath.Join("~", "project", "src")
	if got != want {
		t.Errorf("prettyHomePath(home/sub) = %q, want %q", got, want)
	}
	// Path NOT under home → unchanged.
	other := "/tmp/other"
	if got := prettyHomePath(other); got != other {
		t.Errorf("prettyHomePath(non-home) = %q, want %q", got, other)
	}
	// Home itself → exactly "~".
	if got := prettyHomePath(home); got != "~" {
		t.Errorf("prettyHomePath(home) = %q, want ~", got)
	}
}
