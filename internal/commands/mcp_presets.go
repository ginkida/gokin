package commands

import (
	"fmt"
	"sort"
	"strings"
)

// MCPPreset describes a ready-to-add MCP server from the built-in catalog.
// Users can add one with `/mcp preset NAME` instead of typing the full
// command + args.
type MCPPreset struct {
	Description string
	Params      MCPAddParams
}

// mcpPresets is the catalog of popular MCP servers. Keys are lowercase.
// Each entry uses the official @modelcontextprotocol npm package (or uvx for
// Python servers) so `npx -y …` / `uvx …` fetches the latest version without
// requiring a global install.
var mcpPresets = map[string]MCPPreset{
	"github": {
		Description: "GitHub — repos, issues, PRs, code search (needs GITHUB_PERSONAL_ACCESS_TOKEN)",
		Params: MCPAddParams{
			Transport: "stdio",
			Command:   "npx",
			Args:      []string{"-y", "@modelcontextprotocol/server-github"},
		},
	},
	"filesystem": {
		Description: "Filesystem — read/write files in allowed directories",
		Params: MCPAddParams{
			Transport: "stdio",
			Command:   "npx",
			Args:      []string{"-y", "@modelcontextprotocol/server-filesystem"},
		},
	},
	"sqlite": {
		Description: "SQLite — query and inspect SQLite databases",
		Params: MCPAddParams{
			Transport: "stdio",
			Command:   "uvx",
			Args:      []string{"mcp-server-sqlite"},
		},
	},
	"brave-search": {
		Description: "Brave Search — web search (needs BRAVE_API_KEY)",
		Params: MCPAddParams{
			Transport: "stdio",
			Command:   "npx",
			Args:      []string{"-y", "@modelcontextprotocol/server-brave-search"},
		},
	},
	"puppeteer": {
		Description: "Puppeteer — browser automation and web scraping",
		Params: MCPAddParams{
			Transport: "stdio",
			Command:   "npx",
			Args:      []string{"-y", "@modelcontextprotocol/server-puppeteer"},
		},
	},
	"memory": {
		Description: "Memory — persistent knowledge graph for long-term context",
		Params: MCPAddParams{
			Transport: "stdio",
			Command:   "npx",
			Args:      []string{"-y", "@modelcontextprotocol/server-memory"},
		},
	},
	"fetch": {
		Description: "Fetch — retrieve and process web content",
		Params: MCPAddParams{
			Transport: "stdio",
			Command:   "uvx",
			Args:      []string{"mcp-server-fetch"},
		},
	},
	"sequential-thinking": {
		Description: "Sequential Thinking — structured step-by-step reasoning",
		Params: MCPAddParams{
			Transport: "stdio",
			Command:   "npx",
			Args:      []string{"-y", "@modelcontextprotocol/server-sequential-thinking"},
		},
	},
	"time": {
		Description: "Time — current time, timezone conversion, reminders",
		Params: MCPAddParams{
			Transport: "stdio",
			Command:   "uvx",
			Args:      []string{"mcp-server-time"},
		},
	},
	"postgres": {
		Description: "PostgreSQL — read-only SQL queries against a Postgres DB",
		Params: MCPAddParams{
			Transport: "stdio",
			Command:   "npx",
			Args:      []string{"-y", "@modelcontextprotocol/server-postgres"},
		},
	},
}

// LookupMCPPreset returns the add-params for a named preset. The name lookup
// is case-insensitive. Returns ok=false if the preset doesn't exist.
func LookupMCPPreset(name string) (MCPAddParams, bool) {
	p, ok := mcpPresets[strings.ToLower(name)]
	if !ok {
		return MCPAddParams{}, false
	}
	return p.Params, true
}

// FormatMCPPresets renders the catalog as a user-facing list, sorted
// alphabetically by name.
func FormatMCPPresets() string {
	names := make([]string, 0, len(mcpPresets))
	for k := range mcpPresets {
		names = append(names, k)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("Available MCP presets:\n\n")
	for _, name := range names {
		p := mcpPresets[name]
		b.WriteString(fmt.Sprintf("  %-22s %s\n", name, p.Description))
	}
	b.WriteString("\nAdd one with: /mcp preset NAME")
	return b.String()
}
