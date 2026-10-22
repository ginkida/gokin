package hooks

import "context"

type sessionStartGateKey struct{}
type sessionStartNotesKey struct{}
type sessionEnvironmentKey struct{}

// The root loop joins startup before rendering workflows or sending requests.
// A child loop has its own hook inbox and must not consume its parent's notes.
func ContextWithSessionStartGate(ctx context.Context, gate func(context.Context) error, notes func() string) context.Context {
	ctx = context.WithValue(ctx, sessionStartGateKey{}, gate)
	return context.WithValue(ctx, sessionStartNotesKey{}, notes)
}

func AwaitSessionStart(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if gate, _ := ctx.Value(sessionStartGateKey{}).(func(context.Context) error); gate != nil {
		return gate(ctx)
	}
	return nil
}

func ContextWithSessionEnvironment(ctx context.Context, files func() []string) context.Context {
	return context.WithValue(ctx, sessionEnvironmentKey{}, files)
}

func SessionEnvironmentFiles(ctx context.Context) []string {
	if files, _ := ctx.Value(sessionEnvironmentKey{}).(func() []string); files != nil {
		return files()
	}
	return nil
}
