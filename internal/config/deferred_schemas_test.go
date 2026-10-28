package config

import "testing"

func TestDeferredSchemasDefaultAndEnvironment(t *testing.T) {
	if !DefaultConfig().Tools.DeferredSchemas {
		t.Fatal("deferred schemas must be on by default")
	}
	for _, value := range []string{"true", "1", "false", "0"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("GOKIN_DEFERRED_SCHEMAS", value)
			cfg := DefaultConfig()
			cfg.Tools.DeferredSchemas = value == "false" || value == "0"
			loadFromEnv(cfg)
			if cfg.Tools.DeferredSchemas != (value == "true" || value == "1") {
				t.Fatalf("override %q = %v", value, cfg.Tools.DeferredSchemas)
			}
		})
	}
}
