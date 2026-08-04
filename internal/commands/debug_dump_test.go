package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type debugDumpApp struct {
	AppInterface
	state any
	err   error
}

func (a *debugDumpApp) GetUIDebugState() (any, error) { return a.state, a.err }

func TestDebugDumpIsPrivateBoundedAndRedacted(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	configDir := filepath.Join(root, "gokin")
	if err := os.Mkdir(configDir, 0o755); err != nil {
		t.Fatalf("create permissive config dir: %v", err)
	}

	opaqueToken := "opaque-value-with-no-vendor-prefix"
	bearerToken := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnopqrstuvwxyz123456"
	app := &debugDumpApp{state: map[string]any{
		"token":             opaqueToken,
		"current_tool_info": "Authorization: Bearer " + bearerToken,
		"token_usage_pct":   37.5,
	}}

	message, err := (&DebugDumpCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(message, configDir) {
		t.Fatalf("result does not name dump directory: %q", message)
	}

	matches, err := filepath.Glob(filepath.Join(configDir, "debug-dump-*.json"))
	if err != nil {
		t.Fatalf("glob dumps: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("dump count = %d, want 1 (%v)", len(matches), matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if strings.Contains(string(data), opaqueToken) || strings.Contains(string(data), bearerToken) {
		t.Fatalf("dump contains an unredacted credential: %s", data)
	}
	if !strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("dump has no redaction marker: %s", data)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("dump is not valid JSON: %v", err)
	}
	if decoded["token_usage_pct"] != 37.5 {
		t.Fatalf("non-secret diagnostic field changed: %v", decoded["token_usage_pct"])
	}

	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(configDir)
		if err != nil {
			t.Fatalf("stat config dir: %v", err)
		}
		if got := dirInfo.Mode().Perm(); got != 0o700 {
			t.Errorf("config dir mode = %o, want 700", got)
		}
		fileInfo, err := os.Stat(matches[0])
		if err != nil {
			t.Fatalf("stat dump: %v", err)
		}
		if got := fileInfo.Mode().Perm(); got != 0o600 {
			t.Errorf("dump mode = %o, want 600", got)
		}
	}
}

func TestDebugDumpRejectsOversizedStateWithoutCreatingFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	app := &debugDumpApp{state: map[string]any{
		"description": strings.Repeat("x", maxDebugDumpBytes+1),
	}}

	_, err := (&DebugDumpCommand{}).Execute(context.Background(), nil, app)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Execute error = %v, want size-limit error", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "gokin")); !os.IsNotExist(statErr) {
		t.Fatalf("oversized dump created storage, stat error = %v", statErr)
	}
}

func TestDebugDumpSerializationFailureDoesNotCreateFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	app := &debugDumpApp{state: map[string]any{"unsupported": make(chan int)}}

	_, err := (&DebugDumpCommand{}).Execute(context.Background(), nil, app)
	if err == nil || !strings.Contains(err.Error(), "serialize") {
		t.Fatalf("Execute error = %v, want serialization error", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "gokin")); !os.IsNotExist(statErr) {
		t.Fatalf("failed dump created storage, stat error = %v", statErr)
	}
}
