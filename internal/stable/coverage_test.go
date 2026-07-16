package stable

import (
	"strings"
	"testing"
)

// --- EncodeMap edge cases ---

func TestEncodeMap_Nil(t *testing.T) {
	if got := EncodeMap(nil); got != "null" {
		t.Fatalf("EncodeMap(nil) = %q, want %q", got, "null")
	}
}

func TestEncodeMap_Empty(t *testing.T) {
	if got := EncodeMap(map[string]any{}); got != "" {
		t.Fatalf("EncodeMap(empty) = %q, want %q", got, "")
	}
}

// --- EncodeAny ---

func TestEncodeAny_Nil(t *testing.T) {
	if got := EncodeAny(nil); got != "<nil>" {
		t.Fatalf("EncodeAny(nil) = %q, want %q", got, "<nil>")
	}
}

func TestEncodeAny_Primitives(t *testing.T) {
	// int
	if got := EncodeAny(42); !strings.Contains(got, "int:") {
		t.Fatalf("EncodeAny(42) = %q, want type-tagged int", got)
	}
	// string
	if got := EncodeAny("hello"); !strings.Contains(got, "string:") {
		t.Fatalf("EncodeAny(\"hello\") = %q, want type-tagged string", got)
	}
	// bool
	if got := EncodeAny(true); !strings.Contains(got, "bool:") {
		t.Fatalf("EncodeAny(true) = %q, want type-tagged bool", got)
	}
}

func TestEncodeAny_NestedMap(t *testing.T) {
	v := map[string]any{"a": 1, "b": []any{"x", "y"}}
	got := EncodeAny(v)
	if !strings.HasPrefix(got, "map[string]any{") {
		t.Fatalf("expected map prefix, got %q", got)
	}
}

func TestEncodeAny_SliceAny(t *testing.T) {
	got := EncodeAny([]any{"a", "b"})
	if !strings.HasPrefix(got, "[]any[") {
		t.Fatalf("expected []any[ prefix, got %q", got)
	}
}

func TestEncodeAny_SliceString(t *testing.T) {
	got := EncodeAny([]string{"x", "y"})
	if !strings.HasPrefix(got, "[]string[") {
		t.Fatalf("expected []string[ prefix, got %q", got)
	}
}

func TestEncodeAny_EmptySlices(t *testing.T) {
	if got := EncodeAny([]any{}); got != "[]any[]" {
		t.Fatalf("EncodeAny(empty []any) = %q, want %q", got, "[]any[]")
	}
	if got := EncodeAny([]string{}); got != "[]string[]" {
		t.Fatalf("EncodeAny(empty []string) = %q, want %q", got, "[]string[]")
	}
}

// --- CloneMap nil ---

func TestCloneMap_Nil(t *testing.T) {
	if got := CloneMap(nil); got != nil {
		t.Fatalf("CloneMap(nil) = %v, want nil", got)
	}
}

func TestCloneMap_Empty(t *testing.T) {
	clone := CloneMap(map[string]any{})
	if clone == nil || len(clone) != 0 {
		t.Fatalf("CloneMap(empty) = %v, want empty map", clone)
	}
}

// --- CloneAny ---

func TestCloneAny_Primitives(t *testing.T) {
	// Primitives are returned as-is (immutable)
	if got := CloneAny(42); got != 42 {
		t.Fatalf("CloneAny(42) = %v, want 42", got)
	}
	if got := CloneAny("hello"); got != "hello" {
		t.Fatalf("CloneAny(\"hello\") = %v, want \"hello\"", got)
	}
	if got := CloneAny(true); got != true {
		t.Fatalf("CloneAny(true) = %v, want true", got)
	}
	if got := CloneAny(nil); got != nil {
		t.Fatalf("CloneAny(nil) = %v, want nil", got)
	}
}

func TestCloneAny_SliceAny(t *testing.T) {
	original := []any{"a", "b", "c"}
	clone := CloneAny(original).([]any)

	// Mutate original, clone should be unaffected
	original[0] = "mutated"
	if clone[0] != "a" {
		t.Fatalf("CloneAny did not deep-copy []any: clone[0] = %v, want \"a\"", clone[0])
	}
}

func TestCloneAny_SliceString(t *testing.T) {
	original := []string{"x", "y"}
	clone := CloneAny(original).([]string)

	original[0] = "mutated"
	if clone[0] != "x" {
		t.Fatalf("CloneAny did not deep-copy []string: clone[0] = %v, want \"x\"", clone[0])
	}
}

func TestCloneAny_NestedMap(t *testing.T) {
	original := map[string]any{"nested": map[string]any{"k": "v"}}
	clone := CloneAny(original).(map[string]any)

	original["nested"].(map[string]any)["k"] = "mutated"
	if clone["nested"].(map[string]any)["k"] != "v" {
		t.Fatalf("CloneAny did not deep-copy nested map")
	}
}

func TestCloneAny_NestedSliceInMap(t *testing.T) {
	original := map[string]any{"items": []any{map[string]any{"k": "v"}}}
	clone := CloneMap(original)

	original["items"].([]any)[0].(map[string]any)["k"] = "mutated"
	if clone["items"].([]any)[0].(map[string]any)["k"] != "v" {
		t.Fatalf("CloneMap did not deep-copy nested slice-of-maps")
	}
}
