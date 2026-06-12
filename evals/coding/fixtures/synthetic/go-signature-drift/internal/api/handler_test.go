package api

import (
	"testing"

	"example.com/go-signature-drift/internal/store"
)

func TestGreeting(t *testing.T) {
	h := NewHandler(store.New())
	if got := h.Greeting(); got != "hello" {
		t.Fatalf("Greeting() = %q, want %q", got, "hello")
	}
}
