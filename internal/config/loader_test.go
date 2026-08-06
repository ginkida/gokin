package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMigrateLegacyModelRoundTimeout_UpgradesOnlyHistoricalDefault(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{name: "legacy generated default", in: 5 * time.Minute, want: DefaultModelRoundTimeout},
		{name: "explicit tighter value", in: 4 * time.Minute, want: 4 * time.Minute},
		{name: "explicit longer value", in: 20 * time.Minute, want: 20 * time.Minute},
		{name: "unset", in: 0, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Tools: ToolsConfig{ModelRoundTimeout: tt.in}}
			migrateLegacyModelRoundTimeout(cfg)
			if got := cfg.Tools.ModelRoundTimeout; got != tt.want {
				t.Fatalf("model round timeout = %v, want %v", got, tt.want)
			}
		})
	}

	// Loading a missing optional file can leave callers with nil-like paths;
	// keep the migration helper safe for defensive use.
	migrateLegacyModelRoundTimeout(nil)
}

func TestMigrateLegacyProviderTimeouts_ReleasesOnlyHistoricalPair(t *testing.T) {
	tests := []struct {
		name       string
		http, idle time.Duration
		wantHTTP   time.Duration
		wantIdle   time.Duration
	}{
		{name: "generated legacy pair", http: 120 * time.Second, idle: 30 * time.Second},
		{name: "custom HTTP with old idle", http: 3 * time.Minute, idle: 30 * time.Second, wantHTTP: 3 * time.Minute, wantIdle: 30 * time.Second},
		{name: "old HTTP with custom idle", http: 120 * time.Second, idle: time.Minute, wantHTTP: 120 * time.Second, wantIdle: time.Minute},
		{name: "provider defaults already selected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{API: APIConfig{Retry: RetryConfig{
				HTTPTimeout: tt.http, StreamIdleTimeout: tt.idle,
				Providers: map[string]ProviderRetryConfig{
					"glm": {HTTPTimeout: 9 * time.Minute, StreamIdleTimeout: 4 * time.Minute},
				},
			}}}
			migrateLegacyProviderTimeouts(cfg)
			if got := cfg.API.Retry.HTTPTimeout; got != tt.wantHTTP {
				t.Fatalf("HTTP timeout = %v, want %v", got, tt.wantHTTP)
			}
			if got := cfg.API.Retry.StreamIdleTimeout; got != tt.wantIdle {
				t.Fatalf("stream idle timeout = %v, want %v", got, tt.wantIdle)
			}
			if got := cfg.API.Retry.Providers["glm"]; got.HTTPTimeout != 9*time.Minute || got.StreamIdleTimeout != 4*time.Minute {
				t.Fatalf("provider override was changed: %+v", got)
			}
		})
	}
	migrateLegacyProviderTimeouts(nil)
}

func TestMigrateLegacyPlanningTimeout_ReleasesOnlyHistoricalDefault(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{name: "generated legacy default", in: time.Minute, want: 0},
		{name: "explicit tighter value", in: 45 * time.Second, want: 45 * time.Second},
		{name: "explicit longer value", in: 3 * time.Minute, want: 3 * time.Minute},
		{name: "already inherited", in: 0, want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Plan: PlanConfig{PlanningTimeout: tt.in}}
			migrateLegacyPlanningTimeout(cfg)
			if got := cfg.Plan.PlanningTimeout; got != tt.want {
				t.Fatalf("planning timeout = %v, want %v", got, tt.want)
			}
		})
	}
	migrateLegacyPlanningTimeout(nil)
}

func TestConfigValidateRejectsNegativePlanTimeouts(t *testing.T) {
	for _, tt := range []struct {
		name string
		set  func(*Config)
		want string
	}{
		{
			name: "planning timeout",
			set:  func(cfg *Config) { cfg.Plan.PlanningTimeout = -time.Second },
			want: "plan.planning_timeout",
		},
		{
			name: "step timeout",
			set:  func(cfg *Config) { cfg.Plan.DefaultStepTimeout = -time.Second },
			want: "plan.default_step_timeout",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.set(cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want %s validation", err, tt.want)
			}
		})
	}
}

func TestLoadFrom_MigratesUserTimeoutBeforeProjectOverride(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(root, "user.yaml")
	if err := os.WriteFile(globalPath, []byte("api:\n  retry:\n    http_timeout: 120s\n    stream_idle_timeout: 30s\ntools:\n  model_round_timeout: 5m\nplan:\n  planning_timeout: 1m\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	cfg, err := LoadFrom(globalPath)
	if err != nil {
		t.Fatalf("LoadFrom legacy user config: %v", err)
	}
	if got := cfg.Tools.ModelRoundTimeout; got != DefaultModelRoundTimeout {
		t.Fatalf("legacy user timeout = %v, want migrated %v", got, DefaultModelRoundTimeout)
	}
	if cfg.API.Retry.HTTPTimeout != 0 || cfg.API.Retry.StreamIdleTimeout != 0 {
		t.Fatalf("legacy provider pair was not released: %+v", cfg.API.Retry)
	}
	if cfg.Plan.PlanningTimeout != 0 {
		t.Fatalf("legacy planning timeout = %v, want inherited zero", cfg.Plan.PlanningTimeout)
	}

	projectConfigDir := filepath.Join(project, ".gokin")
	if err := os.Mkdir(projectConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	projectConfig := filepath.Join(projectConfigDir, "config.yaml")
	if err := os.WriteFile(projectConfig, []byte("api:\n  retry:\n    http_timeout: 120s\n    stream_idle_timeout: 30s\ntools:\n  model_round_timeout: 5m\nplan:\n  planning_timeout: 1m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadFrom(globalPath)
	if err != nil {
		t.Fatalf("LoadFrom with project override: %v", err)
	}
	if got := cfg.Tools.ModelRoundTimeout; got != 5*time.Minute {
		t.Fatalf("project override = %v, want explicit 5m", got)
	}
	if cfg.API.Retry.HTTPTimeout != 120*time.Second || cfg.API.Retry.StreamIdleTimeout != 30*time.Second {
		t.Fatalf("explicit project provider timeouts were migrated: %+v", cfg.API.Retry)
	}
	if cfg.Plan.PlanningTimeout != time.Minute {
		t.Fatalf("explicit project planning timeout was migrated: %v", cfg.Plan.PlanningTimeout)
	}
}

func TestMigrateLegacyKimiModelName_RewritesRetiredNames(t *testing.T) {
	cases := []struct {
		oldName string
		want    string
	}{
		{"kimi-k2.5", "kimi-for-coding"},
		{"kimi-k2-thinking-turbo", "kimi-for-coding"},
		{"kimi-k2-turbo-preview", "kimi-for-coding"},
	}
	for _, c := range cases {
		cfg := &Config{Model: ModelConfig{Name: c.oldName}}
		migrateLegacyKimiModelName(cfg)
		if cfg.Model.Name != c.want {
			t.Errorf("migrate %q → %q, want %q", c.oldName, cfg.Model.Name, c.want)
		}
	}
}

func TestMigrateLegacyKimiModelName_PreservesUnknown(t *testing.T) {
	// Non-legacy names must not be touched.
	cfg := &Config{Model: ModelConfig{Name: "glm-5"}}
	migrateLegacyKimiModelName(cfg)
	if cfg.Model.Name != "glm-5" {
		t.Errorf("unknown model name was rewritten: got %q", cfg.Model.Name)
	}
}

func TestMigrateLegacyKimiModelName_PreservesEmpty(t *testing.T) {
	cfg := &Config{}
	migrateLegacyKimiModelName(cfg)
	if cfg.Model.Name != "" {
		t.Errorf("empty name changed to %q", cfg.Model.Name)
	}
}

func TestMigrateLegacyKimiModelName_NilSafe(t *testing.T) {
	migrateLegacyKimiModelName(nil) // must not panic
}

func TestMigrateLegacyKimiModelName_SkipsWhenCustomBaseURL(t *testing.T) {
	// Regression guard from release review: users with explicit
	// CustomBaseURL (e.g., pointing at Moonshot Developer API) may still
	// be using legacy names on an endpoint that serves them. The
	// migration must not silently rewrite their model — that would
	// redirect their request to a model the endpoint doesn't serve.
	cases := []string{
		"https://api.moonshot.ai/anthropic",
		"https://custom.example/kimi",
		"  https://x.y/z  ", // whitespace-preserving detection via TrimSpace
	}
	for _, url := range cases {
		cfg := &Config{Model: ModelConfig{
			Name:          "kimi-k2.5",
			CustomBaseURL: url,
		}}
		migrateLegacyKimiModelName(cfg)
		if cfg.Model.Name != "kimi-k2.5" {
			t.Errorf("CustomBaseURL=%q should block migration; got %q", url, cfg.Model.Name)
		}
	}
}

func TestMigrateLegacyKimiModelName_EmptyCustomBaseURLTriggersMigration(t *testing.T) {
	// Whitespace-only CustomBaseURL counts as unset.
	cfg := &Config{Model: ModelConfig{
		Name:          "kimi-k2.5",
		CustomBaseURL: "   ",
	}}
	migrateLegacyKimiModelName(cfg)
	if cfg.Model.Name != "kimi-for-coding" {
		t.Errorf("whitespace CustomBaseURL should not block migration; got %q", cfg.Model.Name)
	}
}
