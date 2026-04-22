package commands

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/agent"
	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/mcp"
	"gokin/internal/permission"
	"gokin/internal/plan"
	"gokin/internal/tools"
	"gokin/internal/undo"
)

// fakeAppForMCP is a minimal AppInterface stub. Only the methods exercised by
// MCPCommand need meaningful implementations — the rest panic-safely return
// zero values via embedding of AppInterface itself isn't possible (it's
// declared in this package), so we stub every method explicitly.
type fakeAppForMCP struct {
	cfg      *config.Config
	mgr      *mcp.Manager
	registry *tools.Registry
	client   client.Client
	saveErr  error
	saveHits int
}

func (f *fakeAppForMCP) GetMCPManager() *mcp.Manager    { return f.mgr }
func (f *fakeAppForMCP) GetToolRegistry() *tools.Registry { return f.registry }
func (f *fakeAppForMCP) GetMainClient() client.Client     { return f.client }
func (f *fakeAppForMCP) GetConfig() *config.Config        { return f.cfg }

// — unused methods below (return zero values) —
func (f *fakeAppForMCP) GetSession() *chat.Session                        { return nil }
func (f *fakeAppForMCP) GetHistoryManager() (*chat.HistoryManager, error) { return nil, nil }
func (f *fakeAppForMCP) GetContextManager() *appcontext.ContextManager    { return nil }
func (f *fakeAppForMCP) GetUndoManager() *undo.Manager                    { return nil }
func (f *fakeAppForMCP) GetWorkDir() string                               { return "" }
func (f *fakeAppForMCP) ClearConversation()                               {}
func (f *fakeAppForMCP) GetTodoTool() *tools.TodoTool                     { return nil }
func (f *fakeAppForMCP) GetTokenStats() TokenStats                        { return TokenStats{} }
func (f *fakeAppForMCP) GetModelSetter() ModelSetter                      { return nil }
func (f *fakeAppForMCP) GetProjectInfo() *appcontext.ProjectInfo          { return nil }
func (f *fakeAppForMCP) GetPlanManager() *plan.Manager                    { return nil }
func (f *fakeAppForMCP) GetTreePlanner() *agent.TreePlanner               { return nil }
func (f *fakeAppForMCP) IsPlanningModeEnabled() bool                      { return false }
func (f *fakeAppForMCP) TogglePlanningMode() bool                         { return false }
func (f *fakeAppForMCP) ApplyConfig(*config.Config) error                 { return nil }
func (f *fakeAppForMCP) GetVersion() string                               { return "test" }
func (f *fakeAppForMCP) AddSystemMessage(string)                          {}
func (f *fakeAppForMCP) GetAgentTypeRegistry() *agent.AgentTypeRegistry   { return nil }
func (f *fakeAppForMCP) GetUIDebugState() (any, error)                    { return nil, nil }
func (f *fakeAppForMCP) GetRuntimeHealthReport() string                   { return "" }
func (f *fakeAppForMCP) GetPolicyReport() string                          { return "" }
func (f *fakeAppForMCP) GetLedgerReport() string                          { return "" }
func (f *fakeAppForMCP) GetPlanProofReport(int) string                    { return "" }
func (f *fakeAppForMCP) GetJournalReport() string                         { return "" }
func (f *fakeAppForMCP) GetRecoveryReport() string                        { return "" }
func (f *fakeAppForMCP) GetObservabilityReport() string                   { return "" }
func (f *fakeAppForMCP) GetSessionGovernanceReport() string               { return "" }
func (f *fakeAppForMCP) GetMemoryReport() string                          { return "" }
func (f *fakeAppForMCP) GetPerformanceStats() string                      { return "" }

func newFakeApp(t *testing.T, servers []*mcp.ServerConfig) *fakeAppForMCP {
	t.Helper()
	return &fakeAppForMCP{
		cfg:      &config.Config{},
		mgr:      mcp.NewManager(servers),
		registry: tools.NewRegistry(),
	}
}

// withStubbedSave replaces the package-level saveConfig with a stub that
// records calls and returns the configured error. Returns a cleanup.
func withStubbedSave(t *testing.T, app *fakeAppForMCP) func() {
	t.Helper()
	orig := saveConfig
	saveConfig = func(cfg *config.Config) error {
		app.saveHits++
		return app.saveErr
	}
	return func() { saveConfig = orig }
}

// ─── Subcommand dispatch ───────────────────────────────────────────────────

func TestMCPCommand_DisabledWhenManagerNil(t *testing.T) {
	cmd := &MCPCommand{}
	app := &fakeAppForMCP{cfg: &config.Config{}}
	got, err := cmd.Execute(context.Background(), []string{"list"}, app)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "MCP is disabled") {
		t.Errorf("got %q, want disabled hint", got)
	}
}

func TestMCPCommand_DefaultToList(t *testing.T) {
	cmd := &MCPCommand{}
	app := newFakeApp(t, nil)
	got, err := cmd.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "No MCP servers configured") {
		t.Errorf("got %q, want empty-list hint", got)
	}
}

func TestMCPCommand_UnknownSubcommand(t *testing.T) {
	cmd := &MCPCommand{}
	app := newFakeApp(t, nil)
	got, err := cmd.Execute(context.Background(), []string{"foo"}, app)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "Unknown subcommand") {
		t.Errorf("got %q", got)
	}
}

func TestMCPCommand_HelpShowsUsage(t *testing.T) {
	cmd := &MCPCommand{}
	app := newFakeApp(t, nil)
	for _, arg := range []string{"help", "-h", "--help"} {
		got, err := cmd.Execute(context.Background(), []string{arg}, app)
		if err != nil {
			t.Fatalf("%s err: %v", arg, err)
		}
		if !strings.Contains(got, "/mcp list") {
			t.Errorf("%s: usage missing, got %q", arg, got)
		}
	}
}

// ─── /mcp list ─────────────────────────────────────────────────────────────

func TestMCPList_ShowsConfiguredServersEvenWhenDisconnected(t *testing.T) {
	cmd := &MCPCommand{}
	app := newFakeApp(t, []*mcp.ServerConfig{
		{Name: "github", Transport: "stdio", Command: "npx"},
		{Name: "alpha", Transport: "http", URL: "http://x"},
	})

	got, err := cmd.Execute(context.Background(), []string{"list"}, app)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "github") || !strings.Contains(got, "alpha") {
		t.Errorf("expected both server names in output, got %q", got)
	}
	// Neither server is connected → both show offline indicator.
	if strings.Count(got, "offline") != 2 {
		t.Errorf("expected 2 offline markers; got %q", got)
	}
	// Totals line.
	if !strings.Contains(got, "0/2 connected") {
		t.Errorf("expected '0/2 connected' summary, got %q", got)
	}
}

func TestMCPList_StableAlphabeticalOrder(t *testing.T) {
	cmd := &MCPCommand{}
	app := newFakeApp(t, []*mcp.ServerConfig{
		{Name: "zebra"}, {Name: "apple"}, {Name: "mango"},
	})
	got, err := cmd.Execute(context.Background(), []string{"list"}, app)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// Check order of names by finding their index.
	idxA := strings.Index(got, "apple")
	idxM := strings.Index(got, "mango")
	idxZ := strings.Index(got, "zebra")
	if !(idxA < idxM && idxM < idxZ) {
		t.Errorf("expected alphabetical order apple<mango<zebra; indices = %d,%d,%d",
			idxA, idxM, idxZ)
	}
}

// ─── /mcp status ───────────────────────────────────────────────────────────

func TestMCPStatus_NamedServerNotFound(t *testing.T) {
	cmd := &MCPCommand{}
	app := newFakeApp(t, []*mcp.ServerConfig{{Name: "github"}})
	got, err := cmd.Execute(context.Background(), []string{"status", "missing"}, app)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "No MCP server named \"missing\"") {
		t.Errorf("got %q", got)
	}
}

func TestMCPStatus_AllServersWhenNameOmitted(t *testing.T) {
	cmd := &MCPCommand{}
	app := newFakeApp(t, []*mcp.ServerConfig{
		{Name: "foo"}, {Name: "bar"},
	})
	got, err := cmd.Execute(context.Background(), []string{"status"}, app)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "Server: foo") || !strings.Contains(got, "Server: bar") {
		t.Errorf("got %q, expected both servers in detail", got)
	}
}

func TestMCPStatus_EmptyManager(t *testing.T) {
	cmd := &MCPCommand{}
	app := newFakeApp(t, nil)
	got, err := cmd.Execute(context.Background(), []string{"status"}, app)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "No MCP servers configured") {
		t.Errorf("got %q", got)
	}
}

// ─── /mcp add — validation paths ───────────────────────────────────────────

func TestMCPAdd_UsageWhenArgsInsufficient(t *testing.T) {
	cmd := &MCPCommand{}
	app := newFakeApp(t, nil)
	defer withStubbedSave(t, app)()

	got, err := cmd.Execute(context.Background(), []string{"add"}, app)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "Usage: /mcp add") {
		t.Errorf("got %q", got)
	}
	if app.saveHits != 0 {
		t.Errorf("config should not save on usage error")
	}
}

func TestMCPAdd_DuplicateName(t *testing.T) {
	cmd := &MCPCommand{}
	app := newFakeApp(t, []*mcp.ServerConfig{{Name: "dup", Transport: "stdio", Command: "x"}})
	defer withStubbedSave(t, app)()

	got, err := cmd.Execute(context.Background(), []string{"add", "dup", "stdio", "foo"}, app)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "already exists") {
		t.Errorf("got %q", got)
	}
	if app.saveHits != 0 {
		t.Errorf("config should not save on duplicate")
	}
}

func TestMCPAdd_UnknownTransport(t *testing.T) {
	cmd := &MCPCommand{}
	app := newFakeApp(t, nil)
	defer withStubbedSave(t, app)()

	got, err := cmd.Execute(context.Background(), []string{"add", "x", "websocket", "ws://a"}, app)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "Unknown transport") {
		t.Errorf("got %q", got)
	}
	if app.saveHits != 0 {
		t.Errorf("config should not save")
	}
	// Bad add should not leak server into runtime config.
	if _, exists := app.mgr.GetServerConfig("x"); exists {
		t.Error("failed add must not register server")
	}
}

func TestMCPAdd_FailedConnectLeavesNoState(t *testing.T) {
	// /mcp add spawns a real subprocess via StdioTransport. Using a command
	// path that cannot exist — exec.Start will return ENOENT before any
	// process is created — verifies the rollback path cleans up cleanly
	// (no manager-level config, no config save, no registry tools).
	cmd := &MCPCommand{}
	app := newFakeApp(t, nil)
	defer withStubbedSave(t, app)()

	_, err := cmd.Execute(context.Background(),
		[]string{"add", "badsrv", "stdio", "/nonexistent/gokin-test-command"}, app)
	if err == nil {
		t.Error("expected error from /mcp add with missing command")
	}

	// Manager must not retain the broken server entry.
	if _, exists := app.mgr.GetServerConfig("badsrv"); exists {
		t.Error("manager still has badsrv after failed Connect — rollback incomplete")
	}

	// Config must not have been persisted.
	if app.saveHits != 0 {
		t.Errorf("config.Save fired %d times on failed add, want 0", app.saveHits)
	}
	if len(app.cfg.MCP.Servers) != 0 {
		t.Errorf("config.MCP.Servers got %d entries, want 0", len(app.cfg.MCP.Servers))
	}

	// Registry must not carry any MCPTool for this server.
	for _, tool := range app.registry.List() {
		if mt, ok := tool.(*mcp.MCPTool); ok && mt.GetServerName() == "badsrv" {
			t.Errorf("registry leaked MCPTool %q from failed add", mt.Name())
		}
	}

	// Permission overrides for any badsrv-prefixed tools should not persist.
	if got := permission.GetToolRiskLevel("badsrv_anything"); got != permission.RiskMedium {
		t.Errorf("permission override leaked: %v (want RiskMedium default)", got)
	}
}

// ─── /mcp remove — validation paths ────────────────────────────────────────

func TestMCPRemove_NoArgs(t *testing.T) {
	cmd := &MCPCommand{}
	app := newFakeApp(t, nil)
	defer withStubbedSave(t, app)()

	got, err := cmd.Execute(context.Background(), []string{"remove"}, app)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "Usage: /mcp remove") {
		t.Errorf("got %q", got)
	}
}

func TestMCPRemove_UnknownName(t *testing.T) {
	cmd := &MCPCommand{}
	app := newFakeApp(t, nil)
	defer withStubbedSave(t, app)()

	got, err := cmd.Execute(context.Background(), []string{"remove", "ghost"}, app)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "No MCP server named \"ghost\"") {
		t.Errorf("got %q", got)
	}
	if app.saveHits != 0 {
		t.Errorf("config should not save for non-existent server")
	}
}

func TestMCPRemove_ConfiguredButNotConnected(t *testing.T) {
	// Setup: server exists in Manager (added via AddServer) and in YAML config.
	cmd := &MCPCommand{}
	app := newFakeApp(t, []*mcp.ServerConfig{{Name: "x", Transport: "stdio", Command: "y"}})
	app.cfg.MCP.Servers = []config.MCPServerConfig{
		{Name: "x", Transport: "stdio", Command: "y"},
		{Name: "keep", Transport: "stdio", Command: "z"},
	}
	defer withStubbedSave(t, app)()

	got, err := cmd.Execute(context.Background(), []string{"remove", "x"}, app)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "Removed MCP server \"x\"") {
		t.Errorf("got %q", got)
	}
	if app.saveHits != 1 {
		t.Errorf("saveHits = %d, want 1", app.saveHits)
	}
	// YAML should now only have 'keep'.
	if len(app.cfg.MCP.Servers) != 1 || app.cfg.MCP.Servers[0].Name != "keep" {
		t.Errorf("config after remove = %+v, want [keep]", app.cfg.MCP.Servers)
	}
	// Runtime manager should no longer know about x.
	if _, exists := app.mgr.GetServerConfig("x"); exists {
		t.Error("server still present in manager after remove")
	}
}

// ─── /mcp refresh — validation paths ───────────────────────────────────────

func TestMCPRefresh_NoArgs(t *testing.T) {
	cmd := &MCPCommand{}
	app := newFakeApp(t, nil)
	got, err := cmd.Execute(context.Background(), []string{"refresh"}, app)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "Usage: /mcp refresh") {
		t.Errorf("got %q", got)
	}
}

func TestMCPRefresh_NotConnected(t *testing.T) {
	cmd := &MCPCommand{}
	app := newFakeApp(t, []*mcp.ServerConfig{{Name: "x", Transport: "stdio", Command: "y"}})
	got, err := cmd.Execute(context.Background(), []string{"refresh", "x"}, app)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// RefreshTools returns error when server isn't in clients map.
	if !strings.Contains(got, "Refresh failed") {
		t.Errorf("got %q", got)
	}
}

// ─── Pure helpers ──────────────────────────────────────────────────────────

func TestFilterOutServer_RemovesMatchingName(t *testing.T) {
	in := []config.MCPServerConfig{
		{Name: "a"}, {Name: "b"}, {Name: "a"}, {Name: "c"},
	}
	out := filterOutServer(in, "a")
	if len(out) != 2 {
		t.Fatalf("got len %d, want 2", len(out))
	}
	for _, s := range out {
		if s.Name == "a" {
			t.Errorf("result still contains removed name: %+v", out)
		}
	}
}

func TestFilterOutServer_NoMatchReturnsCopy(t *testing.T) {
	in := []config.MCPServerConfig{{Name: "a"}, {Name: "b"}}
	out := filterOutServer(in, "missing")
	if len(out) != 2 {
		t.Errorf("got %+v, want unchanged", out)
	}
	// Mutate input — output must not change.
	in[0].Name = "mutated"
	if out[0].Name != "a" {
		t.Errorf("filterOutServer must return a fresh backing array")
	}
}

func TestFormatMCPStatsSection_EmptyManager(t *testing.T) {
	mgr := mcp.NewManager(nil)
	if got := formatMCPStatsSection(mgr); got != "" {
		t.Errorf("expected empty string for no servers, got %q", got)
	}
}

func TestFormatMCPStatsSection_IncludesServerAndToolCounts(t *testing.T) {
	mgr := mcp.NewManager([]*mcp.ServerConfig{
		{Name: "a"}, {Name: "b"},
	})
	got := formatMCPStatsSection(mgr)
	if !strings.Contains(got, "🔌 MCP") {
		t.Errorf("missing section header: %q", got)
	}
	if !strings.Contains(got, "2 total, 0 connected") {
		t.Errorf("missing counts: %q", got)
	}
	if !strings.Contains(got, "Tools exposed:   0") {
		t.Errorf("missing tools line: %q", got)
	}
}

func TestFormatServerLine_Indicators(t *testing.T) {
	cases := []struct {
		name      string
		connected bool
		healthy   bool
		want      string
	}{
		{"offline", false, false, "offline"},
		{"connected healthy", true, true, "healthy"},
		{"connected unhealthy", true, false, "unhealthy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := formatServerLine(&mcp.ServerStatus{
				Name: "s", Connected: tc.connected, Healthy: tc.healthy,
			})
			if !strings.Contains(line, tc.want) {
				t.Errorf("got %q, want substring %q", line, tc.want)
			}
		})
	}
}
