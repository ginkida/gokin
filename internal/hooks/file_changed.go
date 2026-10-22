package hooks

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

const MaxFileWatchPaths = 512

func ValidateWatchPaths(paths []string) error {
	if len(paths) > MaxFileWatchPaths {
		return fmt.Errorf("watchPaths exceeds %d paths", MaxFileWatchPaths)
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) || len(path) > 4096 || strings.ContainsRune(path, 0) {
			return fmt.Errorf("watchPaths requires absolute paths of at most 4096 bytes")
		}
	}
	return nil
}

type deferredHookResultKey struct{}

// Async observational handlers update watches and notifications without
// inserting their output into a model conversation.
func ContextWithDeferredHookResults(ctx context.Context, receive func(Result)) context.Context {
	return context.WithValue(ctx, deferredHookResultKey{}, receive)
}

func (m *Manager) HookResultCurrent(hook *Hook) bool {
	if hook == nil || m.CheckReady() != nil || !m.HookEnabled(hook) {
		return false
	}
	return !hook.claudeSettings || m.asyncHookConfigured(hook, CommandFingerprint([]ClaudeCommand{*hook.Claude}))
}
