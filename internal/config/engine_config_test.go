package config

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultConfigUsesFailClosedAutoHybridEngine(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Engine.Mode != "auto" {
		t.Fatalf("engine.mode = %q, want auto", cfg.Engine.Mode)
	}
	if cfg.Engine.REPL.CellTimeout != 30*time.Second {
		t.Fatalf("cell timeout = %v", cfg.Engine.REPL.CellTimeout)
	}
	if cfg.Engine.REPL.MaxCodeBytes != 64*1024 || cfg.Engine.REPL.MaxResponseBytes != 1024*1024 ||
		cfg.Engine.REPL.MaxMemoryBytes != 256*1024*1024 {
		t.Fatalf("unexpected REPL limits: %+v", cfg.Engine.REPL)
	}
}

func TestEngineConfigYAMLOverlay(t *testing.T) {
	cfg := DefaultConfig()
	err := loadFromFile(cfg, writeTestConfig(t, `
engine:
  mode: tools
  repl:
    cell_timeout: 45s
    max_code_bytes: 131072
    max_response_bytes: 2097152
    max_memory_bytes: 536870912
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Engine.Mode != "tools" || cfg.Engine.REPL.CellTimeout != 45*time.Second ||
		cfg.Engine.REPL.MaxCodeBytes != 131072 || cfg.Engine.REPL.MaxResponseBytes != 2097152 ||
		cfg.Engine.REPL.MaxMemoryBytes != 512*1024*1024 {
		t.Fatalf("engine overlay = %+v", cfg.Engine)
	}
}

func TestEngineConfigValidation(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"mode", func(c *Config) { c.Engine.Mode = "python-only" }, "engine.mode"},
		{"timeout", func(c *Config) { c.Engine.REPL.CellTimeout = 0 }, "cell_timeout"},
		{"code lower bound", func(c *Config) { c.Engine.REPL.MaxCodeBytes = 10 }, "max_code_bytes"},
		{"response upper bound", func(c *Config) { c.Engine.REPL.MaxResponseBytes = 32 * 1024 * 1024 }, "max_response_bytes"},
		{"memory lower bound", func(c *Config) { c.Engine.REPL.MaxMemoryBytes = 32 * 1024 * 1024 }, "max_memory_bytes"},
		{"memory upper bound", func(c *Config) { c.Engine.REPL.MaxMemoryBytes = 3 * 1024 * 1024 * 1024 }, "max_memory_bytes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.API.Backend = "ollama" // Validation does not require credentials.
			tc.edit(cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate error = %v, want %q", err, tc.want)
			}
		})
	}
}

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	path := t.TempDir() + "/config.yaml"
	if err := WriteConfigFile(path, []byte(content)); err != nil {
		t.Fatal(err)
	}
	return path
}
