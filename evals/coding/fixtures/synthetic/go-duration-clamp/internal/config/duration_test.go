package config

import (
	"testing"
	"time"
)

func TestEffectiveTimeout(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero clamps to default", 0, 30 * time.Second},
		{"negative clamps to default", -5 * time.Second, 30 * time.Second},
		{"positive passes through", 10 * time.Second, 10 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectiveTimeout(Config{Timeout: tt.in}); got != tt.want {
				t.Fatalf("EffectiveTimeout(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
