package client

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/config"
)

type requestReasoningEffortKey struct{}

// ContextWithReasoningEffort overrides one request, including its retries. It
// never mutates the shared client or the user's saved configuration.
func ContextWithReasoningEffort(ctx context.Context, effort string) context.Context {
	return context.WithValue(ctx, requestReasoningEffortKey{}, effort)
}

// EffectiveReasoningEffort uses the active client's request resolution, so
// hook payloads agree with the level sent to the provider.
func EffectiveReasoningEffort(ctx context.Context, c Client) string {
	if reader, ok := c.(interface{ EffectiveReasoningEffort(context.Context) string }); ok {
		return reader.EffectiveReasoningEffort(ctx)
	}
	return ""
}

func (c *AnthropicClient) EffectiveReasoningEffort(ctx context.Context) string {
	c.mu.RLock()
	cfg := c.config
	c.mu.RUnlock()
	if !c.isProvider("glm") || !config.GLMAlwaysOnReasoning(cfg.Model) {
		return ""
	}
	effort, err := c.requestReasoningEffort(ctx, cfg.Model, cfg.ReasoningEffort)
	if err != nil {
		return ""
	}
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" || effort == "auto" {
		budget := cfg.ThinkingBudget
		if !cfg.EnableThinking {
			budget = 0
		}
		return config.GLMEffortForBudget(budget)
	}
	return effort
}

func (c *AnthropicClient) requestReasoningEffort(ctx context.Context, model, configured string) (string, error) {
	effort, ok := ctx.Value(requestReasoningEffortKey{}).(string)
	if !ok || effort == "" {
		return configured, nil
	}
	if !c.isProvider("glm") || !config.GLMAlwaysOnReasoning(model) {
		return "", fmt.Errorf("request effort override is unsupported for %s model %q", c.GetProvider(), model)
	}
	if effort != "low" && effort != "high" && effort != "max" {
		return "", fmt.Errorf("unsupported GLM request effort %q", effort)
	}
	return effort, nil
}

// ResolveSkillModelSettings validates before skill shell injection can run.
// A direct provider client must never send another provider's model to its
// endpoint. Claude model aliases select equivalent GLM tiers explicitly.
func ResolveSkillModelSettings(c Client, selection, effort string) (model, nativeEffort string, err error) {
	if c == nil {
		return "", "", fmt.Errorf("skill model settings require an initialized client")
	}
	current := c.GetModel()
	identified, ok := c.(ProviderIdentity)
	if !ok {
		return "", "", fmt.Errorf("skill model settings require provider identity")
	}
	provider := identified.GetProvider()
	model = strings.TrimSpace(selection)
	smallFastAlias := strings.EqualFold(model, "haiku")
	if smallFastAlias {
		model, err = ResolveSmallFastModel(c)
		if err != nil {
			return "", "", err
		}
	}
	if model == "" || strings.EqualFold(model, "inherit") {
		model = current
	} else if provider == "glm" {
		switch strings.ToLower(model) {
		case "flash", "fast":
			model = config.GLMFlashModel
		case "pro", "strong", "sonnet", "opus":
			model = config.GLMFlagshipModel
		}
	}
	if !smallFastAlias && !strings.EqualFold(model, current) {
		found := false
		for _, known := range GetModelsForProvider(provider) {
			if strings.EqualFold(model, known.ID) {
				model, found = known.ID, true
				break
			}
		}
		if !found {
			return "", "", fmt.Errorf("skill model %q is not available on active provider %q", selection, provider)
		}
	}
	if effort == "" {
		return model, "", nil
	}
	if provider != "glm" || !config.GLMAlwaysOnReasoning(model) {
		return "", "", fmt.Errorf("skill effort is currently supported only for GLM-5.3 models; selected %q on %q", model, provider)
	}
	switch effort {
	case "low", "high", "max":
		nativeEffort = effort
	case "medium":
		nativeEffort = "high"
	case "xhigh":
		nativeEffort = "max"
	default:
		return "", "", fmt.Errorf("invalid skill effort %q", effort)
	}
	return model, nativeEffort, nil
}
