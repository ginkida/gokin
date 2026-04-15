package commands

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/format"
)

// SemanticStatsCommand shows semantic search statistics.
type SemanticStatsCommand struct{}

func (c *SemanticStatsCommand) Name() string        { return "semantic-stats" }
func (c *SemanticStatsCommand) Description() string { return "Show semantic search index statistics" }
func (c *SemanticStatsCommand) Usage() string       { return "/semantic-stats" }
func (c *SemanticStatsCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "stats",
		Priority: 0,
		Advanced: true,
	}
}

func (c *SemanticStatsCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	// Get semantic indexer from app
	indexer, err := app.GetSemanticIndexer()
	if err != nil {
		return fmt.Sprintf("❌ Semantic search not available: %v", err), nil
	}
	if indexer == nil {
		return "❌ Semantic search is not enabled in config", nil
	}

	var sb strings.Builder

	// Header
	sb.WriteString("🔍 Semantic Search Statistics\n")
	sb.WriteString(strings.Repeat("─", 50))
	sb.WriteString("\n\n")

	// Get index stats
	stats := indexer.GetStats()
	projectID := indexer.GetProjectID()
	workDir := app.GetWorkDir()

	// Project Info
	sb.WriteString("📁 Project\n")
	fmt.Fprintf(&sb, "  Work Dir:        %s\n", workDir)
	fmt.Fprintf(&sb, "  Project ID:      %s\n\n", projectID)

	// Index Stats
	sb.WriteString("📊 Index\n")
	fmt.Fprintf(&sb, "  Files Indexed:   %d\n", stats.FilesIndexed)
	fmt.Fprintf(&sb, "  Total Chunks:    %d\n", stats.TotalChunks)
	fmt.Fprintf(&sb, "  Cache Size:      %s\n", format.Bytes(int64(stats.CacheSizeBytes)))
	fmt.Fprintf(&sb, "  Index Size:      %s\n\n", format.Bytes(int64(stats.IndexSizeBytes)))

	// Cache Performance
	sb.WriteString("⚡ Cache Performance\n")
	fmt.Fprintf(&sb, "  Embeddings:      %d cached\n", stats.EmbeddingsCached)
	fmt.Fprintf(&sb, "  Index Loads:     %d\n", stats.IndexLoads)
	if stats.LastIndexTime > 0 {
		fmt.Fprintf(&sb, "  Last Index Age:  %s\n\n", formatTime(stats.LastIndexTime))
	} else {
		sb.WriteString("  Last Index Age:  never\n\n")
	}

	// Configuration
	cfg := app.GetConfig()
	sb.WriteString("⚙️  Configuration\n")
	fmt.Fprintf(&sb, "  Enabled:         %v\n", cfg.Semantic.Enabled)
	fmt.Fprintf(&sb, "  Chunk Size:      %d chars\n", cfg.Semantic.ChunkSize)
	fmt.Fprintf(&sb, "  Max File Size:   %s\n", format.Bytes(int64(cfg.Semantic.MaxFileSize)))
	fmt.Fprintf(&sb, "  Cache TTL:       %s\n\n", cfg.Semantic.CacheTTL)

	// Footer
	sb.WriteString(strings.Repeat("─", 50))
	sb.WriteString("\n")
	sb.WriteString("💡 Tip: Use /semantic-reindex to rebuild the index")

	return sb.String(), nil
}
