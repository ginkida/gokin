package commands

import (
	"context"
	"strings"
	"unicode"
)

type rawInvocationContextKey struct{}

// WithRawInvocation preserves the accepted slash-command text for commands
// whose semantics depend on exact quoting. Most commands should continue to
// use the normalized []string passed to Execute.
func WithRawInvocation(ctx context.Context, invocation string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, rawInvocationContextKey{}, invocation)
}

// RawInvocationArguments returns the exact text after the slash-command name,
// excluding only separator whitespace. It intentionally preserves quoting,
// backslashes, dollar signs, and trailing whitespace.
func RawInvocationArguments(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	invocation, ok := ctx.Value(rawInvocationContextKey{}).(string)
	if !ok {
		return "", false
	}
	invocation = strings.TrimLeftFunc(invocation, unicode.IsSpace)
	if !strings.HasPrefix(invocation, "/") {
		return "", false
	}
	separator := strings.IndexFunc(invocation, unicode.IsSpace)
	if separator < 0 {
		return "", true
	}
	return strings.TrimLeftFunc(invocation[separator:], unicode.IsSpace), true
}
