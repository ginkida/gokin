package service

import "testing"

func TestDisplayName(t *testing.T) {
	if got := DisplayName(""); got != "anonymous" {
		t.Fatalf("DisplayName(empty) = %q, want anonymous", got)
	}
	if got := DisplayName("Ada"); got != "Ada" {
		t.Fatalf("DisplayName(Ada) = %q, want Ada", got)
	}
}
