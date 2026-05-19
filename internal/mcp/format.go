package mcp

import (
	"fmt"
	"sort"
	"strings"
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
