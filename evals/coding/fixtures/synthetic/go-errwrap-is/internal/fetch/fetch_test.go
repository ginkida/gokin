package fetch

import (
	"errors"
	"testing"
)

func TestLookupMissingWrapsSentinel(t *testing.T) {
	_, err := Lookup(map[string]string{"a": "body"}, "missing")
	if err == nil {
		t.Fatal("Lookup(missing) error = nil, want wrapped ErrNotFound")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("errors.Is(err, ErrNotFound) = false; err = %v", err)
	}
}

func TestLookupFound(t *testing.T) {
	body, err := Lookup(map[string]string{"a": "body"}, "a")
	if err != nil || body != "body" {
		t.Fatalf("Lookup(a) = %q, %v", body, err)
	}
}
