package app

import (
	"fmt"
	"strings"
	"sync"

	"gokin/internal/hooks"
	"gokin/internal/logging"
	"gokin/internal/ui"
)

// hookFailureTracker decides which hook failures get SURFACED to the user
// (deferred round-14 #6): a user-configured hook could previously fail
// forever — typo'd command, missing binary, bad permissions — with zero
// signal anywhere (the executor discards Run's results and the manager's
// SetHandler seam had no production caller).
//
// Anti-spam mirrors the v0.100.80 autosave discipline: a post_tool hook on
// `write` fires on EVERY write, so a broken one would toast per edit. Notify
// only on the healthy→failing TRANSITION per hook; the next success resets
// the streak so a NEW breakage re-notifies.
type hookFailureTracker struct {
	mu      sync.Mutex
	failing map[string]bool
}

// shouldNotify reports whether this result crosses a healthy→failing
// transition for the hook (true = surface it). Pure state machine — the
// toast/log side effects live in the caller so this stays unit-testable.
func (t *hookFailureTracker) shouldNotify(key string, failed bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failing == nil {
		t.failing = make(map[string]bool)
	}
	was := t.failing[key]
	t.failing[key] = failed
	return failed && !was
}

// onHookResult is the hooks.Manager handler (wired by the builder): the
// runtime-visibility surface for every hook execution. Failures that are
// DESIGNED control flow stay quiet here — a FailOnError pre_tool failure
// already surfaces as the "hook blocked:" tool error the model recovers
// from, and a FailOnError stop-hook failure IS the continuation mechanism
// (its output is enqueued as the next message). Everything else was
// previously invisible.
func (a *App) onHookResult(hook *hooks.Hook, output string, err error) {
	key := string(hook.Type) + ":" + hook.DisplayName()
	if err == nil {
		a.hookFailures.shouldNotify(key, false) // reset the streak
		return
	}
	if hook.FailOnError && (hook.Type == hooks.PreTool || hook.Type == hooks.Stop) {
		return // designed control flow — surfaced by its own mechanism
	}
	if !a.hookFailures.shouldNotify(key, true) {
		return // still the same failing streak — already notified
	}

	reason := strings.TrimSpace(err.Error())
	if reason == "" && output != "" {
		reason = strings.TrimSpace(output)
	}
	if runes := []rune(reason); len(runes) > 120 {
		reason = string(runes[:119]) + "…"
	}
	logging.Warn("hook failing (surfaced to UI)",
		"type", string(hook.Type), "hook", hook.DisplayName(), "error", err)
	a.safeSendToProgram(ui.StatusUpdateMsg{
		Type: ui.StatusWarning,
		Message: fmt.Sprintf("Hook %q (%s) is failing: %s — check /hooks",
			hook.DisplayName(), hook.Type, reason),
	})
}
