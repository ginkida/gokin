package app

import (
	"testing"

	"gokin/internal/config"
)

func TestRuntimeProviderForConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{
			name: "model provider wins over stale active provider",
			cfg: &config.Config{
				API:   config.APIConfig{ActiveProvider: "glm"},
				Model: config.ModelConfig{Provider: "deepseek", Name: "deepseek-v4-pro"},
			},
			want: "deepseek",
		},
		{
			name: "model name detection wins when provider is missing",
			cfg: &config.Config{
				API:   config.APIConfig{ActiveProvider: "glm"},
				Model: config.ModelConfig{Name: "deepseek-v4-pro"},
			},
			want: "deepseek",
		},
		{
			name: "unknown model falls back to active provider",
			cfg: &config.Config{
				API:   config.APIConfig{ActiveProvider: "kimi"},
				Model: config.ModelConfig{Name: "custom-coder"},
			},
			want: "kimi",
		},
		{
			name: "auto provider allows model detection",
			cfg: &config.Config{
				API:   config.APIConfig{ActiveProvider: "glm"},
				Model: config.ModelConfig{Provider: "auto", Name: "deepseek-v4-flash"},
			},
			want: "deepseek",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runtimeProviderForConfig(tt.cfg); got != tt.want {
				t.Fatalf("runtimeProviderForConfig() = %q, want %q", got, tt.want)
			}
		})
	}
}
