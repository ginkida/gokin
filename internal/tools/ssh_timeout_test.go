package tools

import "testing"

// A model-supplied SSH timeout must be bounded: an absurd value would overflow
// time.Duration (int64 ns) to a negative duration (instant-fail) or hang the op
// for days. This mirrors the clamping every sibling tool already does.
func TestClampSSHTimeoutSeconds(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{30, 30},                                  // normal
		{maxSSHTimeoutSeconds, maxSSHTimeoutSeconds},
		{maxSSHTimeoutSeconds + 1, maxSSHTimeoutSeconds}, // over the cap
		{999999999, maxSSHTimeoutSeconds},                // absurd (would overflow if multiplied raw)
		{1, 1},
		{0, 1},  // floor (call site guards >0, but the helper is defensive)
		{-5, 1}, // negative
	}
	for _, c := range cases {
		if got := clampSSHTimeoutSeconds(c.in); got != c.want {
			t.Errorf("clampSSHTimeoutSeconds(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
