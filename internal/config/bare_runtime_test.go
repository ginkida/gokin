package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestBareRuntimeMarkerIsNotSerialized(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Bare = true
	cfg.Debug = true
	cfg.DebugFile = "/tmp/private-debug.jsonl"
	cfg.DebugFilter = "api,mcp"
	cfg.DebugLevel = "debug"
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(data)), "bare:") {
		t.Fatalf("runtime bare marker leaked into YAML:\n%s", data)
	}
	for _, forbidden := range []string{
		"debug:", "debug_file:", "debug_filter:", "debug_level:",
		"private-debug.jsonl",
	} {
		if strings.Contains(strings.ToLower(string(data)), forbidden) {
			t.Fatalf("runtime debug field %q leaked into YAML:\n%s", forbidden, data)
		}
	}
}
