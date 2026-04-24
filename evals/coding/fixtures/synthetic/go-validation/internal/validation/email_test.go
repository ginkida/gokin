package validation

import "testing"

func TestValidEmail(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"normal", "user@example.com", true},
		{"missing domain", "user@", false},
		{"missing at", "user.example.com", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidEmail(tt.value); got != tt.want {
				t.Fatalf("ValidEmail(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}
