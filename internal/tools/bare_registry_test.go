package tools

import (
	"slices"
	"testing"
)

func TestBareRegistryContainsOnlyReadEditAndBash(t *testing.T) {
	registry := BareRegistry(t.TempDir())
	want := []string{"bash", "edit", "read"}
	if got := registry.Names(); !slices.Equal(got, want) {
		t.Fatalf("BareRegistry names = %v, want %v", got, want)
	}
	if got := declarationOrder(registry.Declarations()); !slices.Equal(got, want) {
		t.Fatalf("BareRegistry declarations = %v, want %v", got, want)
	}
}
