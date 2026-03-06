package config

import (
	"testing"
	"time"
)

func TestValidateRetryConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
	}{
		{
			name: "valid defaults",
			cfg: &Config{
				API: APIConfig{
					Retry: RetryConfig{
						MaxRetries:  10,
						RetryDelay:  1 * time.Second,
						HTTPTimeout: 120 * time.Second,
					},
				},
			},
			wantErr: false,
		},
		{
			name:    "nil config",
			cfg:     nil,
			wantErr: false,
		},
		{
			name: "negative MaxRetries",
			cfg: &Config{
				API: APIConfig{
					Retry: RetryConfig{MaxRetries: -1},
				},
			},
			wantErr: true,
		},
		{
			name: "negative RetryDelay",
			cfg: &Config{
				API: APIConfig{
					Retry: RetryConfig{RetryDelay: -1 * time.Second},
				},
			},
			wantErr: true,
		},
		{
			name: "RetryDelay too small",
			cfg: &Config{
				API: APIConfig{
					Retry: RetryConfig{RetryDelay: 50 * time.Millisecond}, // < 100ms
				},
			},
			wantErr: true,
		},
		{
			name: "RetryDelay too large",
			cfg: &Config{
				API: APIConfig{
					Retry: RetryConfig{RetryDelay: 10 * time.Minute}, // > 5min
				},
			},
			wantErr: true,
		},
		{
			name: "RetryDelay zero is allowed (disabled)",
			cfg: &Config{
				API: APIConfig{
					Retry: RetryConfig{RetryDelay: 0},
				},
			},
			wantErr: false,
		},
		{
			name: "HTTPTimeout too small",
			cfg: &Config{
				API: APIConfig{
					Retry: RetryConfig{HTTPTimeout: 500 * time.Millisecond}, // < 1s
				},
			},
			wantErr: true,
		},
		{
			name: "HTTPTimeout too large",
			cfg: &Config{
				API: APIConfig{
					Retry: RetryConfig{HTTPTimeout: 60 * time.Minute}, // > 30min
				},
			},
			wantErr: true,
		},
		{
			name: "HTTPTimeout zero is allowed (use default)",
			cfg: &Config{
				API: APIConfig{
					Retry: RetryConfig{HTTPTimeout: 0},
				},
			},
			wantErr: false,
		},
		{
			name: "StreamIdleTimeout too small",
			cfg: &Config{
				API: APIConfig{
					Retry: RetryConfig{StreamIdleTimeout: 2 * time.Second}, // < 5s
				},
			},
			wantErr: true,
		},
		{
			name: "valid provider override",
			cfg: &Config{
				API: APIConfig{
					Retry: RetryConfig{
						Providers: map[string]ProviderRetryConfig{
							"anthropic": {HTTPTimeout: 5 * time.Minute},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "unknown provider in override",
			cfg: &Config{
				API: APIConfig{
					Retry: RetryConfig{
						Providers: map[string]ProviderRetryConfig{
							"nonexistent": {HTTPTimeout: 5 * time.Minute},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid timeout in provider override",
			cfg: &Config{
				API: APIConfig{
					Retry: RetryConfig{
						Providers: map[string]ProviderRetryConfig{
							"gemini": {HTTPTimeout: 500 * time.Millisecond}, // too small
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "boundary: RetryDelay at minimum",
			cfg: &Config{
				API: APIConfig{
					Retry: RetryConfig{RetryDelay: 100 * time.Millisecond},
				},
			},
			wantErr: false,
		},
		{
			name: "boundary: RetryDelay at maximum",
			cfg: &Config{
				API: APIConfig{
					Retry: RetryConfig{RetryDelay: 5 * time.Minute},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRetryConfig(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRetryConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateTimeoutRange(t *testing.T) {
	tests := []struct {
		name    string
		value   time.Duration
		min     time.Duration
		max     time.Duration
		wantErr bool
	}{
		{"zero allowed", 0, time.Second, time.Minute, false},
		{"at minimum", time.Second, time.Second, time.Minute, false},
		{"at maximum", time.Minute, time.Second, time.Minute, false},
		{"in range", 30 * time.Second, time.Second, time.Minute, false},
		{"below minimum", 500 * time.Millisecond, time.Second, time.Minute, true},
		{"above maximum", 2 * time.Minute, time.Second, time.Minute, true},
		{"negative", -1 * time.Second, time.Second, time.Minute, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTimeoutRange("test", tt.value, tt.min, tt.max)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTimeoutRange(%v, %v, %v) error = %v, wantErr %v", tt.value, tt.min, tt.max, err, tt.wantErr)
			}
		})
	}
}
