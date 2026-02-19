package tools

import (
	"context"
	"strings"
)

// Thoroughness controls the depth of explore agent investigation.
type Thoroughness string

const (
	// ThoroughnessQuick is for fast, shallow exploration (1-2 searches max).
	ThoroughnessQuick Thoroughness = "quick"
	// ThoroughnessNormal is the default exploration depth.
	ThoroughnessNormal Thoroughness = "normal"
	// ThoroughnessThorough is for deep, comprehensive exploration.
	ThoroughnessThorough Thoroughness = "thorough"
)

// ParseThoroughness parses a string into a Thoroughness level.
// Returns ThoroughnessNormal for unrecognized values.
func ParseThoroughness(s string) Thoroughness {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "quick":
		return ThoroughnessQuick
	case "thorough", "very thorough":
		return ThoroughnessThorough
	default:
		return ThoroughnessNormal
	}
}

// thoroughnessKey is a context key for passing thoroughness to spawned agents.
type thoroughnessKey struct{}

// WithThoroughness returns a context carrying the thoroughness value.
func WithThoroughness(ctx context.Context, t Thoroughness) context.Context {
	return context.WithValue(ctx, thoroughnessKey{}, t)
}

// ThoroughnessFromContext extracts thoroughness from context, returns ThoroughnessNormal if not set.
func ThoroughnessFromContext(ctx context.Context) Thoroughness {
	if v, ok := ctx.Value(thoroughnessKey{}).(Thoroughness); ok {
		return v
	}
	return ThoroughnessNormal
}
