package stable

import "testing"

func TestEncodeMapStableAcrossOrder(t *testing.T) {
	a := map[string]any{
		"filter": map[string]any{"kind": "test", "max": 5},
		"path":   "internal",
	}
	b := map[string]any{
		"path":   "internal",
		"filter": map[string]any{"max": 5, "kind": "test"},
	}

	if got, want := EncodeMap(a), EncodeMap(b); got != want {
		t.Fatalf("EncodeMap should be stable across map order:\ngot  %q\nwant %q", got, want)
	}
}

func TestEncodeMapDistinguishesTypes(t *testing.T) {
	intValue := EncodeMap(map[string]any{"offset": 1})
	floatValue := EncodeMap(map[string]any{"offset": float64(1)})
	stringValue := EncodeMap(map[string]any{"offset": "1"})

	if intValue == floatValue {
		t.Fatal("EncodeMap should distinguish int and float64 values")
	}
	if intValue == stringValue {
		t.Fatal("EncodeMap should distinguish int and string values")
	}
}

func TestCloneMapDeepCopiesJSONLikeValues(t *testing.T) {
	original := map[string]any{
		"nested": map[string]any{"k": "v"},
		"items":  []any{"a", map[string]any{"b": "c"}},
		"paths":  []string{"x", "y"},
	}

	clone := CloneMap(original)
	original["nested"].(map[string]any)["k"] = "mutated"
	original["items"].([]any)[1].(map[string]any)["b"] = "mutated"
	original["paths"].([]string)[0] = "mutated"

	if clone["nested"].(map[string]any)["k"] != "v" {
		t.Fatalf("nested map was not cloned: %+v", clone["nested"])
	}
	if clone["items"].([]any)[1].(map[string]any)["b"] != "c" {
		t.Fatalf("nested slice map was not cloned: %+v", clone["items"])
	}
	if clone["paths"].([]string)[0] != "x" {
		t.Fatalf("string slice was not cloned: %+v", clone["paths"])
	}
}
