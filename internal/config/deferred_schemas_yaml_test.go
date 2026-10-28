package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// A config saved before the default flipped has no key and must inherit true;
// an explicit false must survive a save round trip.
func TestDeferredSchemasYAMLDefaultAndExplicitFalse(t *testing.T) {
	cfg := DefaultConfig()
	if err := yaml.Unmarshal([]byte("tools:\n  timeout: 1m\n"), cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Tools.DeferredSchemas {
		t.Fatal("config without the key lost the default")
	}
	cfg.Tools.DeferredSchemas = false
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := DefaultConfig()
	if err := yaml.Unmarshal(out, reloaded); err != nil {
		t.Fatal(err)
	}
	if reloaded.Tools.DeferredSchemas {
		t.Fatal("explicit false did not survive a save")
	}
}
