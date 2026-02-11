package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	appcontext "gokin/internal/context"
)

// DebugDumpCommand dumps UI state to a JSON file for debugging.
type DebugDumpCommand struct{}

func (c *DebugDumpCommand) Name() string        { return "debug-dump" }
func (c *DebugDumpCommand) Description() string  { return "Dump UI state to JSON file for debugging" }
func (c *DebugDumpCommand) Usage() string        { return "/debug-dump" }

func (c *DebugDumpCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "command",
		Priority: 90,
		Hidden:   true,
	}
}

func (c *DebugDumpCommand) Execute(_ context.Context, _ []string, app AppInterface) (string, error) {
	state, err := app.GetUIDebugState()
	if err != nil {
		return "", fmt.Errorf("failed to get UI state: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to serialize UI state: %w", err)
	}

	configDir, err := appcontext.GetConfigDir()
	if err != nil {
		return "", fmt.Errorf("failed to get config dir: %w", err)
	}

	filename := fmt.Sprintf("debug-dump-%s.json", time.Now().Format("20060102-150405"))
	path := filepath.Join(configDir, filename)

	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("failed to write dump: %w", err)
	}

	return fmt.Sprintf("Debug state dumped to:\n%s", path), nil
}
