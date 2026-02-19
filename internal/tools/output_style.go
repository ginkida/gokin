package tools

import (
	"context"
	"strings"
)

// OutputStyle controls the verbosity and format of agent responses.
// Orthogonal to Thoroughness — thoroughness controls how deep the agent digs,
// output style controls how the results are presented.
type OutputStyle string

const (
	// OutputStyleConcise produces minimal bullet-point output.
	OutputStyleConcise OutputStyle = "concise"
	// OutputStyleNormal is the default balanced output.
	OutputStyleNormal OutputStyle = "normal"
	// OutputStyleDetailed produces verbose output with full explanations.
	OutputStyleDetailed OutputStyle = "detailed"
)

// ParseOutputStyle parses a string into an OutputStyle.
// Returns OutputStyleNormal for unrecognized values.
func ParseOutputStyle(s string) OutputStyle {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "concise", "compact", "brief":
		return OutputStyleConcise
	case "detailed", "verbose":
		return OutputStyleDetailed
	default:
		return OutputStyleNormal
	}
}

// outputStyleKey is a context key for passing output style to spawned agents.
type outputStyleKey struct{}

// WithOutputStyle returns a context carrying the output style value.
func WithOutputStyle(ctx context.Context, s OutputStyle) context.Context {
	return context.WithValue(ctx, outputStyleKey{}, s)
}

// OutputStyleFromContext extracts output style from context, returns OutputStyleNormal if not set.
func OutputStyleFromContext(ctx context.Context) OutputStyle {
	if v, ok := ctx.Value(outputStyleKey{}).(OutputStyle); ok {
		return v
	}
	return OutputStyleNormal
}
