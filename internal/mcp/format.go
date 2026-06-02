package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// FormatList renders the same text that `/mcp list` shows: one summary line
// per server plus a "N/M connected, K tools" footer. Empty input returns a
// stable "no servers" hint so callers (slash command, model-facing tool) can
// rely on identical output.
//
// Pure function — no side effects, no I/O — so it's safe to call from any
// goroutine and from tests without a fake transport.
func FormatList(mgr *Manager) string {
	if mgr == nil {
		return "MCP is not initialised."
	}
	statuses := mgr.GetServerStatus()
	if len(statuses) == 0 {
		return "No MCP servers configured. Add one with `/mcp add NAME stdio CMD ...`."
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })

	var sb strings.Builder
	sb.WriteString("MCP servers:\n")
	for _, s := range statuses {
		sb.WriteString("  ")
		sb.WriteString(FormatServerLine(s))
		sb.WriteByte('\n')
	}

	connected := 0
	totalTools := 0
	for _, s := range statuses {
		if s.Connected {
			connected++
		}
		totalTools += s.ToolCount
	}
	fmt.Fprintf(&sb, "\n%d/%d connected, %d tools exposed to the model.",
		connected, len(statuses), totalTools)
	return sb.String()
}

// formatResourcesTimeout bounds the resources/list round-trip when rendering
// the catalog — the mcp_admin list callback has no context to thread.
const formatResourcesTimeout = 15 * time.Second

// FormatResources renders the catalog of context resources exposed across all
// connected MCP servers, grouped by server, mirroring FormatList/FormatStatus.
// Self-bounds the listing call. Empty/no-resource input returns a stable hint.
func FormatResources(mgr *Manager) string {
	if mgr == nil {
		return "MCP is not initialised."
	}
	ctx, cancel := context.WithTimeout(context.Background(), formatResourcesTimeout)
	defer cancel()

	resources := mgr.ListResources(ctx)
	if len(resources) == 0 {
		return "No MCP resources available (no connected server exposes any, or none implement the resources capability)."
	}
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].Server != resources[j].Server {
			return resources[i].Server < resources[j].Server
		}
		return resources[i].URI < resources[j].URI
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "MCP resources (%d):\n", len(resources))
	currentServer := ""
	for _, r := range resources {
		if r.Server != currentServer {
			currentServer = r.Server
			fmt.Fprintf(&sb, "\n[%s]\n", r.Server)
		}
		sb.WriteString("  ")
		sb.WriteString(r.URI)
		if r.Name != "" {
			fmt.Fprintf(&sb, " — %s", r.Name)
		}
		if r.MIMEType != "" {
			fmt.Fprintf(&sb, " (%s)", r.MIMEType)
		}
		sb.WriteByte('\n')
		if r.Description != "" {
			fmt.Fprintf(&sb, "      %s\n", r.Description)
		}
	}
	sb.WriteString("\nRead one with mcp_admin{action:resources, uri:\"<uri>\"}.")
	return sb.String()
}

// FormatStatus renders the same text that `/mcp status [name]` shows. With an
// empty name it lists every server's detail; with a name it returns the
// single matching server or a "no such server" hint.
func FormatStatus(mgr *Manager, name string) string {
	if mgr == nil {
		return "MCP is not initialised."
	}
	statuses := mgr.GetServerStatus()
	if name != "" {
		for _, s := range statuses {
			if s.Name == name {
				return FormatServerDetail(s)
			}
		}
		return fmt.Sprintf("No MCP server named %q. Run /mcp list.", name)
	}

	if len(statuses) == 0 {
		return "No MCP servers configured."
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })

	var sb strings.Builder
	for i, s := range statuses {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(FormatServerDetail(s))
	}
	return sb.String()
}

// FormatServerLine renders the one-line summary used by FormatList. Exported
// so command-side tests that pin the contract can still call it from outside
// the mcp package after the move from commands/.
func FormatServerLine(s *ServerStatus) string {
	indicator := "✗ offline"
	switch {
	case s.Connected && s.Healthy:
		indicator = "✓ healthy"
	case s.Connected && !s.Healthy:
		indicator = "⚠ unhealthy"
	}
	return fmt.Sprintf("%-20s %s (%d tools)", s.Name, indicator, s.ToolCount)
}

// FormatServerDetail renders the multi-line block used by FormatStatus.
func FormatServerDetail(s *ServerStatus) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Server: %s\n", s.Name)
	fmt.Fprintf(&sb, "  Connected:  %v\n", s.Connected)
	fmt.Fprintf(&sb, "  Healthy:    %v\n", s.Healthy)
	if s.ServerInfo != nil && s.ServerInfo.Version != "" {
		fmt.Fprintf(&sb, "  Version:    %s\n", s.ServerInfo.Version)
	}
	fmt.Fprintf(&sb, "  Tools (%d):", s.ToolCount)
	if s.ToolCount == 0 {
		sb.WriteString(" (none)\n")
	} else {
		sb.WriteByte('\n')
		names := append([]string(nil), s.ToolNames...)
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(&sb, "    - %s\n", n)
		}
	}
	return sb.String()
}
