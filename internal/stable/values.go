package stable

import (
	"fmt"
	"sort"
)

// EncodeMap returns a stable, type-tagged representation of JSON-like map data.
// It is intended for cache keys, not for user display.
func EncodeMap(m map[string]any) string {
	if m == nil {
		return "null"
	}
	return encodeMap(m)
}

// EncodeAny returns a stable, type-tagged representation of a JSON-like value.
func EncodeAny(v any) string {
	return encodeValue(v)
}

func encodeMap(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]byte, 0, len(keys)*16)
	for _, k := range keys {
		out = fmt.Appendf(out, "%d:%s=", len(k), k)
		out = append(out, encodeValue(m[k])...)
		out = append(out, ';')
	}
	return string(out)
}

func encodeValue(v any) string {
	if v == nil {
		return "<nil>"
	}
	switch x := v.(type) {
	case map[string]any:
		return "map[string]any{" + encodeMap(x) + "}"
	case []any:
		out := []byte("[]any[")
		for i, item := range x {
			if i > 0 {
				out = append(out, ',')
			}
			out = append(out, encodeValue(item)...)
		}
		out = append(out, ']')
		return string(out)
	case []string:
		out := []byte("[]string[")
		for i, item := range x {
			if i > 0 {
				out = append(out, ',')
			}
			out = fmt.Appendf(out, "%d:%s", len(item), item)
		}
		out = append(out, ']')
		return string(out)
	default:
		return fmt.Sprintf("%T:%#v", v, v)
	}
}

// CloneMap recursively clones JSON-like maps/slices used in tool arguments and
// function responses.
func CloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	clone := make(map[string]any, len(m))
	for k, v := range m {
		clone[k] = CloneAny(v)
	}
	return clone
}

// CloneAny recursively clones JSON-like map/slice values.
func CloneAny(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return CloneMap(x)
	case []any:
		clone := make([]any, len(x))
		for i, item := range x {
			clone[i] = CloneAny(item)
		}
		return clone
	case []string:
		clone := make([]string, len(x))
		copy(clone, x)
		return clone
	default:
		return v
	}
}
