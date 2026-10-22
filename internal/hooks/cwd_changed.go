package hooks

import "context"

type cwdChangeKey struct{}

type cwdChanges struct {
	changed func(context.Context, string, string)
	commit  func(context.Context, func() (string, string))
}

// A root shell publishes its accepted state after releasing policy locks.
// New child hook scopes mask this callback while keeping environment files.
func ContextWithCwdChanges(ctx context.Context, changed func(context.Context, string, string), commit ...func(context.Context, func() (string, string))) context.Context {
	binding := cwdChanges{changed: changed}
	if len(commit) > 0 {
		binding.commit = commit[0]
	}
	return context.WithValue(ctx, cwdChangeKey{}, binding)
}

func NotifyCwdChanged(ctx context.Context, oldCwd, newCwd string) {
	if oldCwd == newCwd || oldCwd == "" || newCwd == "" || ctx.Err() != nil || RuntimeInfoFromContext(ctx).AgentID != "" {
		return
	}
	if binding, _ := ctx.Value(cwdChangeKey{}).(cwdChanges); binding.changed != nil {
		binding.changed(ctx, oldCwd, newCwd)
	}
}

// Root callers can fence the shell publication with the conversation lease.
// Private child shells use their own policy transaction without that lease.
func CommitShellState(ctx context.Context, update func() (string, string)) {
	if binding, _ := ctx.Value(cwdChangeKey{}).(cwdChanges); binding.commit != nil && RuntimeInfoFromContext(ctx).AgentID == "" {
		binding.commit(ctx, update)
		return
	}
	oldCwd, newCwd := update()
	NotifyCwdChanged(ctx, oldCwd, newCwd)
}
