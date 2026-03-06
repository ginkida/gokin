package format

import (
	"testing"
	"time"
)

func TestDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "< 1ms"},
		{500 * time.Microsecond, "< 1ms"},
		{1 * time.Millisecond, "1ms"},
		{50 * time.Millisecond, "50ms"},
		{999 * time.Millisecond, "999ms"},
		{1 * time.Second, "1.0s"},
		{1500 * time.Millisecond, "1.5s"},
		{59*time.Second + 900*time.Millisecond, "59.9s"},
		{1 * time.Minute, "1m 0s"},
		{2*time.Minute + 30*time.Second, "2m 30s"},
		{59*time.Minute + 59*time.Second, "59m 59s"},
		{1 * time.Hour, "1h 0m"},
		{2*time.Hour + 15*time.Minute, "2h 15m"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := Duration(tt.d)
			if got != tt.want {
				t.Errorf("Duration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

func TestBytes(t *testing.T) {
	tests := []struct {
		b    int64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{1099511627776, "1.0 TB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := Bytes(tt.b)
			if got != tt.want {
				t.Errorf("Bytes(%d) = %q, want %q", tt.b, got, tt.want)
			}
		})
	}
}
