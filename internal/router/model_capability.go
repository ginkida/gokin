package router

import "strings"

// CapabilityTier represents the general capability level of a model.
type CapabilityTier int

const (
	CapabilityWeak   CapabilityTier = iota // Ollama small, unknown models
	CapabilityMedium                       // GLM, DeepSeek, Kimi, MiniMax
	CapabilityStrong                       // Anthropic, OpenAI, Gemini Pro
)

func (t CapabilityTier) String() string {
	switch t {
	case CapabilityWeak:
		return "weak"
	case CapabilityMedium:
		return "medium"
	case CapabilityStrong:
		return "strong"
	}
	return "unknown"
}

// ModelCapability describes the capabilities of the active model
// and how routing should adapt.
type ModelCapability struct {
	Tier               CapabilityTier
	Provider           string
	ModelName          string
	DecomposeAdjust    int     // Additive adjustment to decompose threshold (negative = decompose earlier)
	ThinkingMultiplier float64 // Multiplier for thinking budget (>1 = more thinking for weaker models)
	SelfReviewBoost    bool    // Lower self-review threshold for weaker models
}

// InferModelCapability derives capability from provider and model name.
func InferModelCapability(provider, modelName string) *ModelCapability {
	cap := &ModelCapability{
		Provider:           provider,
		ModelName:          modelName,
		ThinkingMultiplier: 1.0,
	}

	p := strings.ToLower(provider)
	switch p {
	case "anthropic", "openai":
		cap.Tier = CapabilityStrong
	case "gemini":
		cap.Tier = CapabilityStrong
		// Downgrade for flash/lite models
		m := strings.ToLower(modelName)
		if strings.Contains(m, "flash") || strings.Contains(m, "lite") {
			cap.Tier = CapabilityMedium
		}
	case "deepseek", "kimi", "glm", "minimax":
		cap.Tier = CapabilityMedium
	case "ollama":
		cap.Tier = CapabilityWeak
	default:
		cap.Tier = CapabilityWeak
	}

	switch cap.Tier {
	case CapabilityWeak:
		cap.DecomposeAdjust = -2
		cap.ThinkingMultiplier = 1.5
		cap.SelfReviewBoost = true
	case CapabilityMedium:
		cap.DecomposeAdjust = -1
		cap.ThinkingMultiplier = 1.2
		cap.SelfReviewBoost = false
	case CapabilityStrong:
		cap.DecomposeAdjust = 0
		cap.ThinkingMultiplier = 1.0
		cap.SelfReviewBoost = false
	}

	return cap
}
