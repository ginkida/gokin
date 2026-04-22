package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	appcontext "gokin/internal/context"
	"gokin/internal/format"
	"gokin/internal/mcp"
)

// StatsCommand shows detailed session statistics.
type StatsCommand struct{}

func (c *StatsCommand) Name() string        { return "stats" }
func (c *StatsCommand) Description() string { return "Show detailed session statistics" }
func (c *StatsCommand) Usage() string       { return "/stats" }
func (c *StatsCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category:    CategorySession,
		Icon:        "stats",
		Priority:    60,
		RequiresAPI: true,
	}
}

func (c *StatsCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	var sb strings.Builder

	// Get token stats
	tokenStats := app.GetTokenStats()

	// Get config
	cfg := app.GetConfig()

	// Get project info
	projectInfo := app.GetProjectInfo()

	// Header
	sb.WriteString("📊 Session Statistics\n")
	sb.WriteString(strings.Repeat("─", 50))
	sb.WriteString("\n\n")

	// Token Usage
	sb.WriteString("💰 Token Usage\n")
	fmt.Fprintf(&sb, "  Input Tokens:     %s\n", formatNumber(int64(tokenStats.InputTokens)))
	fmt.Fprintf(&sb, "  Output Tokens:    %s\n", formatNumber(int64(tokenStats.OutputTokens)))
	fmt.Fprintf(&sb, "  Total Tokens:     %s\n", formatNumber(int64(tokenStats.TotalTokens)))

	// Calculate cost using per-model pricing from TokenCounter
	var totalCost float64
	contextManager := app.GetContextManager()
	if contextManager != nil {
		tc := contextManager.GetTokenCounter()
		if tc != nil {
			totalCost = tc.CalculateCost(tokenStats.InputTokens, tokenStats.OutputTokens)
		}
	}
	fmt.Fprintf(&sb, "  Est. Cost:       %s\n\n", appcontext.FormatCost(totalCost))

	// Model Info
	sb.WriteString("🤖 Model\n")
	fmt.Fprintf(&sb, "  Name:            %s\n", cfg.Model.Name)
	fmt.Fprintf(&sb, "  Temperature:     %.1f\n", cfg.Model.Temperature)
	fmt.Fprintf(&sb, "  Max Tokens:      %s\n\n", formatNumber(int64(cfg.Model.MaxOutputTokens)))

	// Context Info
	sb.WriteString("📚 Context\n")
	if contextManager == nil {
		contextManager = app.GetContextManager()
	}
	if contextManager != nil {
		metrics := contextManager.GetMetrics()
		summary := metrics.GetSummary()

		fmt.Fprintf(&sb, "  Requests:        %d\n", summary.Requests)
		fmt.Fprintf(&sb, "  Optimizations:   %d\n", summary.Optimizations)
		fmt.Fprintf(&sb, "  Summaries:       %d\n", summary.Summaries)
		fmt.Fprintf(&sb, "  Tokens Processed: %s\n", formatNumber(summary.TokensProcessed))
		fmt.Fprintf(&sb, "  Tokens Saved:     %s\n", formatNumber(summary.TokensSaved))
		fmt.Fprintf(&sb, "  Cache Hit Rate:  %.1f%%\n\n", summary.CacheHitRate*100)
	} else {
		sb.WriteString("  (context manager not available)\n\n")
	}

	// Session Info
	sb.WriteString("💬 Session\n")
	if session := app.GetSession(); session != nil {
		history := session.GetHistory()
		fmt.Fprintf(&sb, "  Messages:        %d\n", len(history))
	}

	// Project Info
	sb.WriteString("\n📁 Project\n")
	if projectInfo != nil {
		fmt.Fprintf(&sb, "  Name:            %s\n", projectInfo.Name)
		fmt.Fprintf(&sb, "  Type:            %s\n", projectInfo.Type)
		sb.WriteString("\n")
	} else {
		sb.WriteString("  (no project info available)\n\n")
	}

	// Session Duration
	sessionStartTime := ctx.Value("session_start")
	if sessionStartTime != nil {
		if startTime, ok := sessionStartTime.(time.Time); ok {
			duration := time.Since(startTime)
			sb.WriteString("⏱️  Duration\n")
			fmt.Fprintf(&sb, "  Session Length:  %s\n\n", format.Duration(duration))
		}
	}

	// Performance stats (phase latency + tool call breakdown)
	if perf := app.GetPerformanceStats(); perf != "" {
		sb.WriteString(perf)
	}

	// MCP section — only shown when at least one server is configured so we
	// don't clutter /stats for users who never opted in.
	if mgr := app.GetMCPManager(); mgr != nil {
		if mcpSection := formatMCPStatsSection(mgr); mcpSection != "" {
			sb.WriteString(mcpSection)
		}
	}

	// Footer
	sb.WriteString(strings.Repeat("─", 50))
	sb.WriteString("\n")
	sb.WriteString("💡 Tip: Use /cost to see real-time token usage")

	return sb.String(), nil
}

// formatMCPStatsSection summarizes MCP server state for /stats. Returns an
// empty string when no servers are configured so callers can skip the header.
func formatMCPStatsSection(mgr *mcp.Manager) string {
	statuses := mgr.GetServerStatus()
	if len(statuses) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("🔌 MCP\n")
	connected := 0
	tools := 0
	unhealthy := 0
	for _, s := range statuses {
		if s.Connected {
			connected++
			if !s.Healthy {
				unhealthy++
			}
		}
		tools += s.ToolCount
	}
	fmt.Fprintf(&sb, "  Servers:         %d total, %d connected\n", len(statuses), connected)
	fmt.Fprintf(&sb, "  Tools exposed:   %d\n", tools)
	if unhealthy > 0 {
		fmt.Fprintf(&sb, "  Unhealthy:       %d (run /mcp status for detail)\n", unhealthy)
	}
	sb.WriteByte('\n')
	return sb.String()
}
func formatNumber(n int64) string {
	in := fmt.Sprintf("%d", n)
	out := make([]byte, 0, len(in)+(len(in)/3))

	i := len(in)
	j := 0
	for i > 0 {
		if j == 3 {
			out = append([]byte{','}, out...)
			j = 0
		}
		i--
		out = append([]byte{in[i]}, out...)
		j++
	}

	return string(out)
}
