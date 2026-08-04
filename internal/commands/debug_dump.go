package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	appcontext "gokin/internal/context"
	"gokin/internal/fileutil"
	"gokin/internal/security"
)

const maxDebugDumpBytes = 4 << 20

// DebugDumpCommand dumps UI state to a JSON file for debugging.
type DebugDumpCommand struct{}

func (c *DebugDumpCommand) Name() string        { return "debug-dump" }
func (c *DebugDumpCommand) Description() string { return "Dump UI state to JSON file for debugging" }
func (c *DebugDumpCommand) Usage() string       { return "/debug-dump" }

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

	// Serialize once up front both to reject pathological snapshots before
	// running every redaction pattern and to give custom JSON marshalers a
	// single, consistent observation of their state.
	rawData, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("failed to serialize UI state: %w", err)
	}
	if len(rawData) > maxDebugDumpBytes {
		return "", fmt.Errorf("debug state exceeds %d-byte dump limit", maxDebugDumpBytes)
	}
	var genericState any
	if err := json.Unmarshal(rawData, &genericState); err != nil {
		return "", fmt.Errorf("failed to normalize UI state: %w", err)
	}

	// Task descriptions, current actions and tool information can contain
	// pasted credentials. Redact the normalized structure before pretty-printing
	// so opaque secrets are caught by field name as well as recognizable format.
	redactedState := security.NewSecretRedactor().RedactAny(genericState)
	data, err := json.MarshalIndent(redactedState, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to serialize UI state: %w", err)
	}
	if len(data) > maxDebugDumpBytes {
		return "", fmt.Errorf("debug state exceeds %d-byte dump limit", maxDebugDumpBytes)
	}

	configDir, err := appcontext.GetConfigDir()
	if err != nil {
		return "", fmt.Errorf("failed to get config dir: %w", err)
	}
	if err := fileutil.EnsurePrivateDir(configDir); err != nil {
		return "", fmt.Errorf("failed to secure config dir: %w", err)
	}

	// Nanoseconds make accidental replacement by two dumps in the same second
	// vanishingly unlikely while retaining a sortable, human-readable name.
	filename := fmt.Sprintf("debug-dump-%s.json", time.Now().Format("20060102-150405.000000000"))
	path := filepath.Join(configDir, filename)

	// Reject a pre-planted symlink/special file and replace regular targets
	// atomically. The dump can reveal paths and work-in-progress, so it must
	// never inherit the process umask as a world-readable diagnostic artifact.
	if err := fileutil.SecurePrivateFile(path); err != nil {
		return "", fmt.Errorf("failed to secure dump path: %w", err)
	}
	if err := fileutil.AtomicWrite(path, data, 0o600); err != nil {
		return "", fmt.Errorf("failed to write dump: %w", err)
	}

	return fmt.Sprintf("Debug state dumped to:\n%s", path), nil
}
