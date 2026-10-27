package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestToolArgumentLimitConfiguration(t *testing.T) {
	cfg := DefaultConfig()
	cfg.API.GLMKey = "offline-test"
	if err := yaml.Unmarshal([]byte("model:\n  max_tool_argument_bytes: 2097152\n"), cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Model.MaxToolArgumentBytes != 2<<20 {
		t.Fatal("configured argument limit lost")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Model.MaxToolArgumentBytes = -1
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "max_tool_argument_bytes") {
		t.Fatalf("negative limit accepted: %v", err)
	}
}
