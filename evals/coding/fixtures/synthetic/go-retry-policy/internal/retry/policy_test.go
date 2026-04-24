package retry

import (
	"testing"
	"time"
)

func TestBackoffPolicies(t *testing.T) {
	for _, fn := range []struct {
		name string
		call func(int) time.Duration
	}{
		{"api", APIBackoff},
		{"db", DBBackoff},
	} {
		t.Run(fn.name, func(t *testing.T) {
			if got := fn.call(0); got != 0 {
				t.Fatalf("attempt 0 = %v, want 0", got)
			}
			if got := fn.call(3); got != 400*time.Millisecond {
				t.Fatalf("attempt 3 = %v, want 400ms", got)
			}
			if got := fn.call(99); got != 1600*time.Millisecond {
				t.Fatalf("attempt cap = %v, want 1600ms", got)
			}
		})
	}
}
