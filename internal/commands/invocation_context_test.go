package commands

import (
	"context"
	"testing"
)

func TestRawInvocationArgumentsPreservesArgumentText(t *testing.T) {
	ctx := WithRawInvocation(context.Background(), "  /skill   raw '$HOME'  ")
	got, ok := RawInvocationArguments(ctx)
	if !ok || got != "raw '$HOME'  " {
		t.Fatalf("RawInvocationArguments() = %q, %v", got, ok)
	}
	if _, ok := RawInvocationArguments(context.Background()); ok {
		t.Fatal("plain context unexpectedly contained a raw invocation")
	}
}
