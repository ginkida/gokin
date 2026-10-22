package config

import (
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SmallFastModel resolves the process-local override on the active transport.
// Custom gateway IDs are allowed; recognizable foreign-provider IDs must not
// accidentally send a request to a different provider's endpoint.
func SmallFastModel(provider, current string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if strings.EqualFold(strings.TrimSpace(current), "haiku") {
		if definition := GetProvider(provider); definition != nil {
			current = definition.DefaultModel
		}
	}
	for _, key := range []string{"GOKIN_SMALL_FAST_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_SMALL_FAST_MODEL"} {
		model := strings.TrimSpace(os.Getenv(key))
		if model == "" {
			continue
		}
		if len(model) > 1024 || !utf8.ValidString(model) || strings.IndexFunc(model, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return "", fmt.Errorf("%s must contain a full model ID without whitespace or control characters", key)
		}
		switch strings.ToLower(model) {
		case "haiku", "fast", "flash", "inherit", "default", "sonnet", "opus":
			return "", fmt.Errorf("%s requires a full model ID, not the alias %q", key, model)
		}
		if provider == "" || provider == "auto" {
			return "", fmt.Errorf("%s requires an identified active provider", key)
		}
		known := DetectKnownProviderFromModel(model)
		if strings.HasPrefix(strings.ToLower(model), "claude-") {
			known = "anthropic"
		}
		if provider != "ollama" && !strings.EqualFold(model, current) && known != "" && known != provider {
			return "", fmt.Errorf("%s model %q belongs to %q, not active provider %q", key, model, known, provider)
		}
		return model, nil
	}
	if provider == "glm" && GLMAlwaysOnReasoning(current) {
		return GLMFlashModel, nil
	}
	if current == "" || strings.EqualFold(current, "haiku") {
		return "", fmt.Errorf("no small fast model is configured for provider %q", provider)
	}
	return current, nil
}

func ResolveModelAlias(provider, current, selection string) (string, error) {
	if strings.EqualFold(strings.TrimSpace(selection), "haiku") {
		return SmallFastModel(provider, current)
	}
	return selection, nil
}

// ValidateModelSelection checks an explicit provider-local model choice without
// reading the default small-model environment preference.
func ValidateModelSelection(provider, current, model string) error {
	if model == "" || len(model) > 1024 || !utf8.ValidString(model) || strings.IndexFunc(model, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("model must be a full ID without whitespace or control characters")
	}
	if provider == "" || provider == "auto" {
		return fmt.Errorf("model selection requires an identified active provider")
	}
	known := DetectKnownProviderFromModel(model)
	if strings.HasPrefix(strings.ToLower(model), "claude-") {
		known = "anthropic"
	}
	if provider != "ollama" && !strings.EqualFold(model, current) && known != "" && known != provider {
		return fmt.Errorf("model %q belongs to %q, not active provider %q", model, known, provider)
	}
	return nil
}
