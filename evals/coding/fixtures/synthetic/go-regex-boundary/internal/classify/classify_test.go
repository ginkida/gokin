package classify

import "testing"

func TestIsServerError(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{"http 500", "HTTP 500 returned", true},
		{"error 503", "error 503", true},
		{"bad gateway", "upstream returned 502 Bad Gateway", true},
		{"timing suffix", "request took 500ms", false},
		{"digit prefix", "queue depth 1500 exceeded", false},
		{"digit suffix", "spawned process 5039", false},
		{"clean message", "request completed", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsServerError(tt.msg); got != tt.want {
				t.Fatalf("IsServerError(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}
