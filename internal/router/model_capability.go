package router

import (
	"strings"
)

// CapabilityTier represents the general capability level of a model.
type CapabilityTier int

const (
	CapabilityWeak   CapabilityTier = iota // Ollama small, unknown models
	CapabilityMedium                       // GLM and DeepSeek
	CapabilityStrong                       // GLM-5+
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
	case "glm":
		cap.Tier = CapabilityMedium
		// GLM-5+ models are strong-tier
		m := strings.ToLower(modelName)
		if strings.HasPrefix(m, "glm-5") {
			cap.Tier = CapabilityStrong
		}
	case "deepseek":
		// DeepSeek V4 Pro is Strong-tier (top SWE-bench, ~Opus-class
		// reasoning). V4 Flash and the legacy deepseek-chat/reasoner
		// are Medium. Unknown deepseek variants default to Medium too —
		// less aggressive decompose penalty than Weak keeps behaviour
		// reasonable if DeepSeek ships a new model id we haven't
		// classified yet.
		cap.Tier = CapabilityMedium
		m := strings.ToLower(modelName)
		if isStrongDeepSeekModel(m) {
			cap.Tier = CapabilityStrong
		}
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

// isStrongDeepSeekModel returns true for DeepSeek models that warrant
// Strong-tier treatment (no decompose penalty, baseline thinking
// multiplier). Currently only V4 Pro qualifies — Flash and legacy
// chat/reasoner stay Medium.
func isStrongDeepSeekModel(modelName string) bool {
	m := strings.ToLower(strings.TrimSpace(modelName))
	return strings.HasPrefix(m, "deepseek-v4-pro")
}
