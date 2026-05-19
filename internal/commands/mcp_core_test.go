package commands

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/config"
	"gokin/internal/mcp"
	"gokin/internal/tools"
)

// MCPAddCore and MCPRemoveCore are the typed entrypoints reused by both the
// /mcp slash command and the model-facing mcp_admin tool. The existing
// slash-command tests (TestMCPAdd_*, TestMCPRemove_*) exercise these
// transitively, but only through the arg-string parser. Tests in this file
// hit Core directly with the typed param struct so a regression in the typed
// path can't slip through the slash-command shim.

// helper: empty manager + fresh registry + empty config — same shape as the
// slash-command tests use, but without needing the AppInterface wrapper.
func newCoreFixture() (*mcp.Manager, *config.Config, *tools.Registry) {
	return mcp.NewManager(nil), &config.Config{}, tools.NewRegistry()
}

func TestMCPAddCore_NilManagerIsUserError(t *testing.T) {
	cfg, reg := &config.Config{}, tools.NewRegistry()
	out, err := MCPAddCore(context.Background(), nil, cfg, reg, nil, MCPAddParams{
		Name: "x", Transport: "stdio", Command: "echo",
	})
	if err != nil {
		t.Fatalf("nil manager should be soft-fail, got err: %v", err)
	}
	if !strings.Contains(out, "MCP is not initialised") {
		t.Fatalf("output should explain MCP is not initialised, got: %q", out)
	}
}

func TestMCPAddCore_MissingRequiredFields(t *testing.T) {
	mgr, cfg, reg := newCoreFixture()
	cases := []struct {
		name   string
		params MCPAddParams
		want   string
	}{
		{"empty name", MCPAddParams{Transport: "stdio", Command: "echo"}, "name + transport"},
		{"empty transport", MCPAddParams{Name: "x", Command: "echo"}, "name + transport"},
		{"stdio missing command", MCPAddParams{Name: "x", Transport: "stdio"}, "stdio transport requires"},
		{"http missing url", MCPAddParams{Name: "x", Transport: "http"}, "http transport requires"},
		{"unknown transport", MCPAddParams{Name: "x", Transport: "telnet", Command: "x"}, "Unknown transport"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := MCPAddCore(context.Background(), mgr, cfg, reg, nil, tc.params)
			if err != nil {
				t.Fatalf("validation failures should be soft-error, got err: %v", err)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("output should contain %q, got: %q", tc.want, out)
			}
			// No side effects on the manager.
			if len(mgr.GetServerStatus()) != 0 {
				t.Fatalf("manager should be untouched on validation fail, got %d servers", len(mgr.GetServerStatus()))
			}
			// No config writes.
			if len(cfg.MCP.Servers) != 0 {
				t.Fatalf("config.MCP.Servers should be empty, got %d", len(cfg.MCP.Servers))
			}
		})
	}
}

func TestMCPAddCore_DuplicateNameRefuses(t *testing.T) {
	mgr := mcp.NewManager([]*mcp.ServerConfig{
		{Name: "existing", Transport: "stdio", Command: "echo"},
	})
	cfg, reg := &config.Config{}, tools.NewRegistry()
	out, err := MCPAddCore(context.Background(), mgr, cfg, reg, nil, MCPAddParams{
		Name: "existing", Transport: "stdio", Command: "echo",
	})
	if err != nil {
		t.Fatalf("duplicate should be soft-fail, got err: %v", err)
	}
	if !strings.Contains(out, "already exists") {
		t.Fatalf("output should mention 'already exists', got: %q", out)
	}
}

// TestMCPAddCore_NilConfigDoesNotCrash pins resilience: if a caller passes
// cfg=nil (e.g. an integration that doesn't care about persistence), Core
// should still run the manager + registry steps without panic. We exercise
// the validation path so we don't need a working subprocess.
func TestMCPAddCore_NilConfigDoesNotCrash(t *testing.T) {
	mgr, _, reg := newCoreFixture()
	_, err := MCPAddCore(context.Background(), mgr, nil, reg, nil, MCPAddParams{
		Name: "", // hits the missing-field branch before any persistence.
	})
	if err != nil {
		t.Fatalf("nil cfg + validation fail should not error: %v", err)
	}
}

func TestMCPRemoveCore_NilManager(t *testing.T) {
	cfg, reg := &config.Config{}, tools.NewRegistry()
	out, err := MCPRemoveCore(nil, cfg, reg, nil, "anything")
	if err != nil {
		t.Fatalf("nil manager should be soft-fail, got: %v", err)
	}
	if !strings.Contains(out, "not initialised") {
		t.Fatalf("output should explain MCP is not initialised, got: %q", out)
	}
}

func TestMCPRemoveCore_EmptyName(t *testing.T) {
	mgr, cfg, reg := newCoreFixture()
	out, err := MCPRemoveCore(mgr, cfg, reg, nil, "")
	if err != nil {
		t.Fatalf("empty name should be soft-fail, got: %v", err)
	}
	if !strings.Contains(out, "name is required") {
		t.Fatalf("output should ask for server name, got: %q", out)
	}
}

func TestMCPRemoveCore_UnknownServer(t *testing.T) {
	mgr, cfg, reg := newCoreFixture()
	out, err := MCPRemoveCore(mgr, cfg, reg, nil, "ghost")
	if err != nil {
		t.Fatalf("unknown server should be soft-fail, got: %v", err)
	}
	if !strings.Contains(out, "No MCP server named") {
		t.Fatalf("output should report unknown server, got: %q", out)
	}
	// Config should be untouched.
	if len(cfg.MCP.Servers) != 0 {
		t.Fatalf("config.MCP.Servers should still be empty, got %d", len(cfg.MCP.Servers))
	}
}

// Note: subprocess-spawning + connect-rollback paths are covered by
// TestMCPAdd_FailedConnectLeavesNoState in mcp_test.go (slash-command
// path). Core uses the same AddServer/Connect/RemoveServer primitives,
// so we don't re-test the rollback here — duplicating it would require
// either a fake transport (mcp package-private) or a real subprocess
// (CI-fragile).
