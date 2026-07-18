package commands

import (
	"strings"
	"testing"

	"gokin/internal/config"
	"gokin/internal/mcp"
	"gokin/internal/tools"
)

// The preset catalog is user-facing: every entry must be addable as-is
// (stdio + non-empty command), lookup must be case-insensitive, and the
// rendered list must show every name.
func TestMCPPresetCatalog(t *testing.T) {
	if len(mcpPresets) == 0 {
		t.Fatal("preset catalog is empty")
	}
	for name, p := range mcpPresets {
		if name != strings.ToLower(name) {
			t.Errorf("preset key %q must be lowercase (lookup normalizes)", name)
		}
		if p.Description == "" {
			t.Errorf("preset %q has no description", name)
		}
		if p.Params.Transport != "stdio" {
			t.Errorf("preset %q transport = %q, want stdio (catalog convention)", name, p.Params.Transport)
		}
		if p.Params.Command == "" {
			t.Errorf("preset %q has no command", name)
		}
	}

	if _, ok := LookupMCPPreset("GitHub"); !ok {
		t.Error("lookup must be case-insensitive")
	}
	if _, ok := LookupMCPPreset("no-such-preset"); ok {
		t.Error("unknown preset must return ok=false")
	}

	rendered := FormatMCPPresets()
	for name := range mcpPresets {
		if !strings.Contains(rendered, name) {
			t.Errorf("FormatMCPPresets missing %q", name)
		}
	}
	if !strings.Contains(rendered, "/mcp preset") {
		t.Error("rendered list must tell the user how to add one")
	}
}

// mcpEdit argument parsing: honest errors for malformed input, no partial
// application on bad keys.
func TestMCPEditParsing(t *testing.T) {
	mgr := mcp.NewManager(nil)
	app := &fakeAppForMCP{cfg: &config.Config{}, mgr: mgr, registry: tools.NewRegistry()}

	out, err := mcpEdit(t.Context(), mgr, app, []string{"ghost", "command=x"})
	if err != nil || !strings.Contains(out, "No MCP server") {
		t.Fatalf("unknown server: out=%q err=%v", out, err)
	}

	// Register a server to edit.
	if err := mgr.AddServer(&mcp.ServerConfig{Name: "srv", Transport: "stdio", Command: "true"}); err != nil {
		t.Fatal(err)
	}
	if out, _ := mcpEdit(t.Context(), mgr, app, []string{"srv", "notakv"}); !strings.Contains(out, "Invalid edit") {
		t.Fatalf("bad kv: %q", out)
	}
	if out, _ := mcpEdit(t.Context(), mgr, app, []string{"srv", "bogus=1"}); !strings.Contains(out, "Unknown edit key") {
		t.Fatalf("unknown key: %q", out)
	}
	if out, _ := mcpEdit(t.Context(), mgr, app, []string{"srv", "transport=carrier-pigeon"}); !strings.Contains(out, "Invalid transport") {
		t.Fatalf("bad transport: %q", out)
	}
}
