package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CheckpointCommand lists and manages agent checkpoints.
type CheckpointCommand struct{}

func (c *CheckpointCommand) Name() string        { return "checkpoints" }
func (c *CheckpointCommand) Description() string { return "List saved agent checkpoints" }
func (c *CheckpointCommand) Usage() string       { return "/checkpoints [agent-id]" }
func (c *CheckpointCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryPlanning,
		Icon:     "save",
		Priority: 75,
		Advanced: true,
	}
}

func (c *CheckpointCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	workDir := app.GetWorkDir()
	checkpointsDir := filepath.Join(workDir, ".gokin", "agents", "checkpoints")

	entries, err := os.ReadDir(checkpointsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "No checkpoints found. Checkpoints are created automatically during agent execution.", nil
		}
		return "", fmt.Errorf("failed to read checkpoints: %w", err)
	}

	if len(entries) == 0 {
		return "No checkpoints found.", nil
	}

	// Optional filter by agent ID
	filterID := ""
	if len(args) > 0 {
		filterID = args[0]
	}

	var sb strings.Builder
	sb.WriteString("Agent Checkpoints\n")
	sb.WriteString(strings.Repeat("─", 50))
	sb.WriteString("\n\n")

	count := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		if filterID != "" && !strings.Contains(name, filterID) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		age := time.Since(info.ModTime())
		ageStr := formatAge(age)
		sizeStr := formatSize(info.Size())

		sb.WriteString(fmt.Sprintf("  %s\n", name))
		sb.WriteString(fmt.Sprintf("    Created: %s ago (%s)\n", ageStr, info.ModTime().Format("2006-01-02 15:04")))
		sb.WriteString(fmt.Sprintf("    Size:    %s\n\n", sizeStr))
		count++
	}

	if count == 0 {
		if filterID != "" {
			return fmt.Sprintf("No checkpoints found for agent '%s'.", filterID), nil
		}
		return "No checkpoints found.", nil
	}

	sb.WriteString(fmt.Sprintf("Total: %d checkpoint(s)\n", count))
	return sb.String(), nil
}

func formatAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func formatSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
}
