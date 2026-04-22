package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gokin/internal/config"
)

// ThinkingCommand toggles extended thinking display and lets users tune the
// token budget at runtime — eliminates the "edit config.yaml then restart"
// round-trip every time you want to see, or stop seeing, the model's
// reasoning stream.
//
// Background: Kimi Coding Plan (K2.6) and GLM 4.7+ both emit `thinking_delta`
// SSE events that the TUI renders as dim italic content. Whether they fire is
// controlled by `cfg.Model.EnableThinking` + `cfg.Model.ThinkingBudget`; the
// factory auto-enables thinking for supported models, but users may want to
// disable it (save tokens on simple prompts) or crank the budget up for a
// gnarly refactor.
type ThinkingCommand struct{}

func (c *ThinkingCommand) Name() string        { return "thinking" }
func (c *ThinkingCommand) Description() string { return "Toggle extended thinking display" }
func (c *ThinkingCommand) Usage() string {
	return `/thinking           - Show status
/thinking on        - Enable extended thinking
/thinking off       - Disable extended thinking
/thinking <tokens>  - Set budget (e.g. /thinking 16384)`
}
func (c *ThinkingCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "brain",
		Priority: 25,
		HasArgs:  true,
		ArgHint:  "on|off|<tokens>",
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

	// Toggles first — short and common.
	switch arg {
	case "on", "true", "enable":
		// "1" intentionally NOT a synonym — it would shadow the numeric-budget
		// path ("/thinking 1" should hit the below-minimum rejection, not
		// silently turn thinking on with a stale/default budget).
		cfg.Model.EnableThinking = true
		// Normalize an out-of-range budget silently: users who typo'd a
		// value into config.yaml (e.g. `thinking_budget: 500`) should still
		// get a working "/thinking on" instead of a cryptic 400 from the
		// provider on their next message.
		cfg.Model.ThinkingBudget = clampThinkingBudget(cfg.Model.ThinkingBudget)
		if err := app.ApplyConfig(cfg); err != nil {
			return fmt.Sprintf("Failed to save: %v", err), nil
		}
		return fmt.Sprintf("✓ thinking: on (budget %d tokens)", cfg.Model.ThinkingBudget), nil

	case "off", "false", "disable":
		// "0" intentionally NOT a synonym — see comment on "1" above.
		cfg.Model.EnableThinking = false
		// Ensure the saved budget is non-zero. Factory's auto-enable branch
		// treats `budget == 0` as "never configured" and flips thinking on.
		// Without this line, /thinking off on a fresh config would silently
		// re-enable on next startup because we'd persist {enable:false,
		// budget:0}. Seeding a default is the simplest way to mark the
		// setting as "user-touched".
		if cfg.Model.ThinkingBudget == 0 {
			cfg.Model.ThinkingBudget = thinkingDefaultBudget
		}
		if err := app.ApplyConfig(cfg); err != nil {
			return fmt.Sprintf("Failed to save: %v", err), nil
		}
		return "✓ thinking: off", nil
	}

	// Numeric budget — setting a budget implies turning thinking on. That's
	// the common case ("I want deeper reasoning for this refactor").
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

	cfg.Model.ThinkingBudget = int32(n)
	cfg.Model.EnableThinking = true
	if err := app.ApplyConfig(cfg); err != nil {
		return fmt.Sprintf("Failed to save: %v", err), nil
	}
	return fmt.Sprintf("✓ thinking: on (budget %d tokens)", n), nil
}

// status renders the current thinking configuration along with a hint about
// whether the active model actually honours it. Mentioning the model inline
// prevents the "I enabled it, why don't I see anything?" support loop.
func (c *ThinkingCommand) status(cfg *config.Config) string {
	model := cfg.Model.Name
	if model == "" {
		model = "(not set)"
	}

	if !cfg.Model.EnableThinking {
		return fmt.Sprintf(`thinking: off
  model:  %s

Use /thinking on to enable, or /thinking <tokens> to set a budget.`, model)
	}

	budget := cfg.Model.ThinkingBudget
	budgetLine := fmt.Sprintf("%d tokens", budget)
	if budget == 0 {
		budgetLine = "(0 — will use provider default)"
	}

	supported := modelSupportsThinking(model)
	note := "✓ model supports extended thinking"
	if !supported {
		note = "⚠ current model may not emit thinking content — switch via /model"
	}

	return fmt.Sprintf(`thinking: on
  budget: %s
  model:  %s
  %s`, budgetLine, model, note)
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
func modelSupportsThinking(modelID string) bool {
	m := strings.ToLower(modelID)
	switch {
	case strings.HasPrefix(m, "kimi-for-coding"),
		strings.HasPrefix(m, "kimi-k2"):
		return true
	case strings.HasPrefix(m, "glm-5"),
		strings.HasPrefix(m, "glm-4.7"):
		return true
	}
	return false
}
