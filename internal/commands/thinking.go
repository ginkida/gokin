package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gokin/internal/config"
)

// ThinkingCommand configures whether extended reasoning is adaptive, forced,
// or disabled and lets users tune its token budget at runtime — this changes
// model behavior/cost, not merely whether reasoning text is displayed.
//
// Background: Kimi Coding Plan (K3/K2.7) and GLM 4.7+ both emit `thinking_delta`
// SSE events that the TUI renders as dim italic content. Whether they fire is
// controlled by `cfg.Model.EnableThinking` + `cfg.Model.ThinkingBudget`; the
// factory auto-enables thinking for supported models, but users may want to
// disable it (save tokens on simple prompts) or crank the budget up for a
// gnarly refactor.
type ThinkingCommand struct{}

func (c *ThinkingCommand) Name() string { return "thinking" }
func (c *ThinkingCommand) Description() string {
	return "Configure adaptive/forced reasoning and its token budget"
}
func (c *ThinkingCommand) Usage() string {
	return `/thinking           - Show status
/thinking auto      - Reason when the task is hard, skip when easy (default)
/thinking on        - Force reasoning every turn
/thinking off       - Never reason
/thinking <tokens>  - Force on with an explicit budget (e.g. /thinking 16384)`
}
func (c *ThinkingCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "brain",
		Priority: 25,
		HasArgs:  true,
		ArgHint:  "auto|on|off|<tokens>",
	}
}

// applyThinkingMode sets the master ThinkingMode plus the legacy EnableThinking/
// ThinkingBudget fields the factory + non-routed (headless) paths still read, so
// the three stay consistent. budget==0 keeps the current/default budget.
//
//	auto — router/runner decide per task; clear the forced static flag.
//	on   — force reasoning; ensure a usable budget.
//	off  — never; seed a non-zero budget so the factory's "budget==0 ⇒ auto-on"
//	       branch can't silently re-enable it.
func applyThinkingMode(cfg *config.Config, mode string, budget int32) {
	mode = config.ResolveThinkingMode(mode)
	cfg.Model.ThinkingMode = mode
	switch mode {
	case config.ThinkingModeOn:
		cfg.Model.EnableThinking = true
		if budget > 0 {
			cfg.Model.ThinkingBudget = budget
		}
		cfg.Model.ThinkingBudget = clampThinkingBudget(cfg.Model.ThinkingBudget)
	case config.ThinkingModeOff:
		cfg.Model.EnableThinking = false
		if cfg.Model.ThinkingBudget == 0 {
			cfg.Model.ThinkingBudget = thinkingDefaultBudget
		}
	default: // auto — the router/runner drive it per request
		cfg.Model.EnableThinking = false
	}
}

// thinkingDefaultBudget matches the factory defaults for GLM/Kimi. Kept here
// as a local constant so /thinking on doesn't need to know which provider
// the user is on — any provider that implements Extended Thinking honours a
// non-zero budget.
const thinkingDefaultBudget int32 = 8192

// thinkingMaxBudget bounds user input. 65536 is well past any practical
// reasoning depth and matches the Anthropic-compat API's documented ceiling;
// going higher is almost always a typo (e.g. pasting a token count from
// /stats into the wrong command).
const thinkingMaxBudget int32 = 65536

// thinkingMinBudget is the API-enforced floor. Requests below this fail at
// the provider side with a cryptic 400 — catch it here.
const thinkingMinBudget int32 = 1024

func (c *ThinkingCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Config not available", nil
	}

	// No args — show current state. Includes the budget because "on" alone
	// is only half the picture; users ask "is it actually firing?" and the
	// budget value answers that.
	if len(args) == 0 {
		return c.status(cfg), nil
	}

	arg := strings.ToLower(strings.TrimSpace(args[0]))

	// Mode words first — short and common.
	switch arg {
	case "auto":
		applyThinkingMode(cfg, config.ThinkingModeAuto, 0)
		if err := app.ApplyConfig(cfg); err != nil {
			return fmt.Sprintf("Failed to save: %v", err), nil
		}
		return "✓ thinking: auto — reasons on hard tasks, skips easy ones (applied during work, not a manual setting)", nil

	case "on", "true", "enable":
		// "1" intentionally NOT a synonym — it would shadow the numeric-budget
		// path ("/thinking 1" should hit the below-minimum rejection, not
		// silently turn thinking on with a stale/default budget).
		applyThinkingMode(cfg, config.ThinkingModeOn, 0)
		if err := app.ApplyConfig(cfg); err != nil {
			return fmt.Sprintf("Failed to save: %v", err), nil
		}
		return fmt.Sprintf("✓ thinking: on — forced every turn (budget %d tokens)", cfg.Model.ThinkingBudget), nil

	case "off", "false", "disable":
		// "0" intentionally NOT a synonym — see comment on "1" above.
		applyThinkingMode(cfg, config.ThinkingModeOff, 0)
		if err := app.ApplyConfig(cfg); err != nil {
			return fmt.Sprintf("Failed to save: %v", err), nil
		}
		return "✓ thinking: off — never reasons", nil
	}

	// Numeric budget — setting a budget implies forcing thinking on with that
	// budget. The common case ("I want deeper reasoning for this refactor").
	n, err := strconv.Atoi(arg)
	if err != nil {
		return fmt.Sprintf("Unknown option: %q\n\n%s", args[0], c.Usage()), nil
	}
	if n < int(thinkingMinBudget) {
		return fmt.Sprintf("Budget too low: %d (minimum %d tokens — Anthropic-compat API floor)", n, thinkingMinBudget), nil
	}
	if n > int(thinkingMaxBudget) {
		return fmt.Sprintf("Budget too high: %d (maximum %d tokens — likely a typo)", n, thinkingMaxBudget), nil
	}

	applyThinkingMode(cfg, config.ThinkingModeOn, int32(n))
	if err := app.ApplyConfig(cfg); err != nil {
		return fmt.Sprintf("Failed to save: %v", err), nil
	}
	return fmt.Sprintf("✓ thinking: on — forced every turn (budget %d tokens)", cfg.Model.ThinkingBudget), nil
}

// status renders the current thinking configuration along with a hint about
// whether the active model actually honours it. Mentioning the model inline
// prevents the "I enabled it, why don't I see anything?" support loop.
func (c *ThinkingCommand) status(cfg *config.Config) string {
	model := cfg.Model.Name
	if model == "" {
		model = "(not set)"
	}

	mode := config.ResolveThinkingMode(cfg.Model.ThinkingMode)
	supported := modelSupportsThinking(model)
	note := "✓ model supports extended thinking"
	if !supported {
		note = "⚠ current model may not emit thinking content — switch via /model"
	}

	switch mode {
	case config.ThinkingModeOff:
		return fmt.Sprintf(`thinking: off — never reasons
  model:  %s

Use /thinking auto (reason on hard tasks only) or /thinking on (force every turn).`, model)

	case config.ThinkingModeOn:
		budget := cfg.Model.ThinkingBudget
		budgetLine := fmt.Sprintf("%d tokens", budget)
		if budget == 0 {
			budgetLine = "(0 — will use provider default)"
		}
		return fmt.Sprintf(`thinking: on — forced every turn
  budget: %s
  model:  %s
  %s

/thinking auto returns to adaptive (reason only when the task is hard).`, budgetLine, model, note)

	default: // auto
		return fmt.Sprintf(`thinking: auto — applied during work, not a manual setting
  The router reasons on hard/code tasks (~4-8K tokens) and skips easy ones;
  sub-agents (/loop, delegation) reason by type. No need to toggle it.
  model:  %s
  %s

Override: /thinking on (force every turn) · /thinking off (never).`, model, note)
	}
}

// clampThinkingBudget coerces any value to the valid [min, max] range. Used
// by "/thinking on" to repair out-of-range budgets silently rather than
// failing on the next request. Returns the default for any value outside
// the range — clamping to boundary would hide typos like 5 (rare intent
// vs. almost always a slip).
//
// Mirrors client/factory.go normalizeThinkingBudget. Duplicated because
// the commands package can't import client (circular dep). If you change
// the bounds or fallback policy, update both.
func clampThinkingBudget(budget int32) int32 {
	if budget < thinkingMinBudget || budget > thinkingMaxBudget {
		return thinkingDefaultBudget
	}
	return budget
}

// modelSupportsThinking duplicates the factory-level checks so the command
// can give an accurate status without importing the client package (which
// would create a circular dep: commands → client → commands).
//
// Mirrors: factory.go supportsKimiThinking / supportsGLMThinking /
// supportsDeepSeekThinking. When adding a new thinking-capable model there,
// update this table too.
func modelSupportsThinking(modelID string) bool {
	m := strings.ToLower(modelID)
	switch {
	case m == "k3", strings.HasPrefix(m, "k3-"), strings.HasPrefix(m, "kimi-k3"),
		strings.HasPrefix(m, "kimi-for-coding"),
		strings.HasPrefix(m, "kimi-k2"):
		return true
	case strings.HasPrefix(m, "glm-5"),
		strings.HasPrefix(m, "glm-4.7"):
		return true
	case strings.HasPrefix(m, "deepseek-"):
		// All DeepSeek family models EXCEPT deepseek-chat support thinking.
		// Matches supportsDeepSeekThinking in client/factory.go.
		return m != "deepseek-chat"
	}
	return false
}
