package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEnvironmentCredentialsRemainRuntimeOnlyAcrossSaveAndClone(t *testing.T) {
	for _, provider := range Providers {
		for _, env := range provider.EnvVars {
			t.Run(env, func(t *testing.T) {
				for _, p := range Providers {
					for _, name := range p.EnvVars {
						t.Setenv(name, "")
					}
				}
				t.Setenv("GOKIN_API_KEY", "")
				t.Setenv(env, "synthetic-environment-secret-12345")
				file := filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(file, []byte("api: {active_provider: glm}\n"), 0600); err != nil {
					t.Fatal(err)
				}
				cfg, err := LoadFrom(file)
				if err != nil {
					t.Fatal(err)
				}
				if provider.GetKey(&cfg.API) != "synthetic-environment-secret-12345" {
					t.Fatal("environment credential is unavailable at runtime")
				}
				// Removing the environment before Save must not erase provenance.
				t.Setenv(env, "")
				clone := cfg.Clone()
				clone.UI.ShowWelcome = false
				if err := clone.Save(); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "synthetic-environment-secret") {
					t.Fatal("ordinary settings save persisted an environment credential")
				}
				if provider.GetKey(&cfg.API) != "synthetic-environment-secret-12345" || provider.GetKey(&clone.API) != "synthetic-environment-secret-12345" {
					t.Fatal("saving changed the effective runtime credential")
				}
			})
		}
	}
}

func TestEnvironmentOverridePreservesDiskCredentialAndExplicitLogin(t *testing.T) {
	t.Setenv("GOKIN_GLM_KEY", "synthetic-env-credential")
	t.Setenv("GOKIN_API_KEY", "synthetic-legacy-environment")
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, []byte("api: {glm_key: saved-provider-key, api_key: saved-legacy-key}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(file)
	if err != nil {
		t.Fatal(err)
	}
	readSaved := func() APIConfig {
		t.Helper()
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var saved struct {
			API APIConfig `yaml:"api"`
		}
		if err := yaml.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		return saved.API
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if saved := readSaved(); saved.GLMKey != "saved-provider-key" || saved.APIKey != "saved-legacy-key" {
		t.Fatal("environment override replaced existing saved credentials")
	}
	// /login is an explicit persistence choice even when its key equals the env value.
	clone := cfg.Clone()
	clone.API.SetProviderKey("glm", "synthetic-env-credential")
	if err := clone.Save(); err != nil {
		t.Fatal(err)
	}
	if saved := readSaved(); saved.GLMKey != "synthetic-env-credential" || saved.APIKey != "saved-legacy-key" {
		t.Fatal("explicit login did not retain its chosen key")
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if saved := readSaved(); saved.GLMKey != "saved-provider-key" {
		t.Fatal("clone credential metadata leaked into parent")
	}
	clone = cfg.Clone()
	clone.API.SetProviderKey("glm", "")
	if err := clone.Save(); err != nil {
		t.Fatal(err)
	}
	if readSaved().GLMKey != "" {
		t.Fatal("explicit logout resurrected the old credential")
	}
}
