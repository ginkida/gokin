package agent

import (
	"context"
	"sync/atomic"
)

// InvocationScope is an opaque process-local identity for one top-level
// invocation. The unexported representation prevents callers from forging a
// match; scopes are created only through NewInvocationScope.
type InvocationScope struct {
	id uint64
}

type invocationScopeContextKey struct{}

var invocationScopeSequence atomic.Uint64

// NewInvocationScope returns a non-zero scope unique for the lifetime of this
// process. Wraparound would require 2^64 top-level invocations; still, skip
// zero so it remains the unscoped sentinel.
func NewInvocationScope() InvocationScope {
	for {
		if id := invocationScopeSequence.Add(1); id != 0 {
			return InvocationScope{id: id}
		}
	}
}

// IsZero reports whether no invocation scope was attached.
func (s InvocationScope) IsZero() bool { return s.id == 0 }

// WithInvocationScope attaches scope to the complete descendant context tree.
// A zero scope is intentionally a no-op.
func WithInvocationScope(ctx context.Context, scope InvocationScope) context.Context {
	if scope.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, invocationScopeContextKey{}, scope)
}

// InvocationScopeFromContext returns the exact top-level invocation identity,
// or the zero scope for interactive/background work without attribution.
func InvocationScopeFromContext(ctx context.Context) InvocationScope {
	if ctx == nil {
		return InvocationScope{}
	}
	scope, _ := ctx.Value(invocationScopeContextKey{}).(InvocationScope)
	return scope
}
