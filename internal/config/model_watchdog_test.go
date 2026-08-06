package config

import (
	"testing"
	"time"
)

func TestModelWatchdogTimeout(t *testing.T) {
	tests := []struct {
		name  string
		round time.Duration
		want  time.Duration
	}{
		{name: "unset uses default floor", round: 0, want: 15 * time.Minute},
		{name: "short round keeps floor", round: 5 * time.Minute, want: 15 * time.Minute},
		{name: "default round keeps headroom", round: 14 * time.Minute, want: 15 * time.Minute},
		{name: "raised round grows watchdog", round: 40 * time.Minute, want: 41 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ModelWatchdogTimeout(tt.round); got != tt.want {
				t.Fatalf("ModelWatchdogTimeout(%v) = %v, want %v", tt.round, got, tt.want)
			}
		})
	}
}
