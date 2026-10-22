package permission

import "context"

type noPermissionPromptKey struct{}

// ContextWithoutPermissionPrompt denies calls requiring new approval while
// retaining existing allows, session decisions, deny rules and safety floors.
// It does not change the manager or affect concurrent interactive requests.
func ContextWithoutPermissionPrompt(ctx context.Context) context.Context {
	return context.WithValue(ctx, noPermissionPromptKey{}, true)
}

func PermissionPromptSuppressed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	disabled, _ := ctx.Value(noPermissionPromptKey{}).(bool)
	return disabled
}
