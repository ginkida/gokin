package app

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"gokin/internal/client"
)

// MaxRunSystemPromptBytes bounds the combined invocation-only prompt supplied
// through CLI text/file flags. It is exported so cmd/gokin can reject an
// oversized file before constructing provider clients.
const MaxRunSystemPromptBytes = 64 << 10

// ConfigureRunSystemPrompt installs invocation-scoped system instructions.
// replacement == nil preserves Gokin's generated prompt; a non-nil pointer
// replaces it (including with an intentionally empty string). appended is
// applied after either the generated or replacement prompt.
//
// This configuration is deliberately runtime-only. applySystemInstruction
// stores the canonical generated prompt in chat.Session and sends the composed
// prompt only to the provider client.
func (a *App) ConfigureRunSystemPrompt(replacement *string, appended string) error {
	if a == nil {
		return fmt.Errorf("cannot configure system prompt without an app")
	}
	replace := ""
	if replacement != nil {
		replace = *replacement
	}
	if err := validateRunSystemPromptText("system prompt replacement", replace); err != nil {
		return err
	}
	if err := validateRunSystemPromptText("system prompt appendix", appended); err != nil {
		return err
	}
	if len(replace)+len(appended) > MaxRunSystemPromptBytes {
		return fmt.Errorf(
			"combined run system prompt exceeds %d KiB limit",
			MaxRunSystemPromptBytes>>10,
		)
	}

	a.runSystemPromptMu.Lock()
	a.runSystemPromptReplacement = replace
	a.runSystemPromptReplace = replacement != nil
	a.runSystemPromptAppend = appended
	a.runSystemPromptMu.Unlock()
	return nil
}

func validateRunSystemPromptText(label, value string) error {
	if len(value) > MaxRunSystemPromptBytes {
		return fmt.Errorf("%s exceeds %d KiB limit", label, MaxRunSystemPromptBytes>>10)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", label)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s contains a NUL byte", label)
	}
	return nil
}

func (a *App) hasRunSystemPromptCustomization() bool {
	if a == nil {
		return false
	}
	a.runSystemPromptMu.RLock()
	custom := a.runSystemPromptReplace || a.runSystemPromptAppend != ""
	a.runSystemPromptMu.RUnlock()
	return custom
}

func (a *App) composeRunSystemInstruction(base string) string {
	if a == nil {
		return base
	}
	a.runSystemPromptMu.RLock()
	replace := a.runSystemPromptReplace
	replacement := a.runSystemPromptReplacement
	appended := a.runSystemPromptAppend
	structured := a.runStructuredOutputPrompt
	a.runSystemPromptMu.RUnlock()

	result := base
	if replace {
		result = replacement
	}
	if appended != "" {
		if result != "" {
			result = strings.TrimRight(result, "\r\n") + "\n\n"
		}
		result += strings.TrimLeft(appended, "\r\n")
	}
	if structured != "" {
		if result != "" {
			result = strings.TrimRight(result, "\r\n") + "\n\n"
		}
		result += strings.TrimLeft(structured, "\r\n")
	}
	if a.harnessStore != nil {
		if harnessPrompt := a.harnessStore.RenderPrompt(); harnessPrompt != "" {
			if result != "" {
				result = strings.TrimRight(result, "\r\n") + "\n\n"
			}
			result += harnessPrompt
		}
	}
	return result
}

// buildDefaultSystemInstruction returns the canonical instruction that is safe
// to persist. Invocation-scoped CLI content is never included here.
func (a *App) buildDefaultSystemInstruction() string {
	if a == nil {
		return ""
	}
	base := ""
	if a.promptBuilder != nil {
		base = a.promptBuilder.Build()
	}
	return base + a.buildModelEnhancement()
}

// applySystemInstruction sends the runtime composition to target and, when
// requested, stores only the canonical base in the session.
func (a *App) applySystemInstruction(target client.Client, base string, persistBase bool) {
	if target != nil {
		target.SetSystemInstruction(a.composeRunSystemInstruction(base))
	}
	if persistBase && a != nil && a.session != nil {
		a.session.SetSystemInstruction(base)
	}
}

// applyStartupSystemInstruction selects between a saved canonical prompt and a
// newly generated one. Custom invocations always rebuild so an appendix cannot
// attach to stale project/model guidance from a prior Gokin version.
func (a *App) applyStartupSystemInstruction(sessionRestored bool) {
	if a == nil {
		return
	}
	if !sessionRestored || (a.config != nil && a.config.Bare) ||
		a.planningModeEnabled || a.hasRunSystemPromptCustomization() {
		base := a.buildDefaultSystemInstruction()
		a.applySystemInstruction(a.client, base, true)
		return
	}
	if a.session != nil {
		if saved := a.session.GetSystemInstruction(); saved != "" {
			a.applySystemInstruction(a.client, saved, false)
			return
		}
	}
	base := a.buildDefaultSystemInstruction()
	a.applySystemInstruction(a.client, base, true)
}

// buildSubAgentProjectContext keeps invocation-scoped instructions effective
// for delegated work without replacing a sub-agent's own safety/tool contract.
func (a *App) buildSubAgentProjectContext(task string) string {
	context := ""
	if a != nil && a.promptBuilder != nil {
		context = a.promptBuilder.BuildSubAgentPromptForTask(task)
	}
	if a == nil {
		return context
	}
	a.runSystemPromptMu.RLock()
	replace := a.runSystemPromptReplace
	replacement := a.runSystemPromptReplacement
	appended := a.runSystemPromptAppend
	a.runSystemPromptMu.RUnlock()

	custom := ""
	if replace {
		custom = replacement
	}
	if appended != "" {
		if custom != "" {
			custom = strings.TrimRight(custom, "\r\n") + "\n\n"
		}
		custom += strings.TrimLeft(appended, "\r\n")
	}
	if custom == "" {
		return context
	}
	const heading = "## Invocation-scoped system instructions"
	if context == "" {
		return heading + "\n\n" + custom
	}
	return strings.TrimRight(context, "\r\n") + "\n\n" + heading + "\n\n" + custom
}
