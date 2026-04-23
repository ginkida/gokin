package router

import (
	"strconv"
	"strings"
)

// CapabilityTier represents the general capability level of a model.
type CapabilityTier int

const (
	CapabilityWeak   CapabilityTier = iota // Ollama small, unknown models
	CapabilityMedium                       // GLM, DeepSeek, Kimi, MiniMax
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
	case "kimi", "minimax":
		cap.Tier = CapabilityMedium
		// Kimi K2.6 (kimi-for-coding on the Coding Plan endpoint) and
		// newer K2.x variants are Strong-tier in practice: 262K context,
		// comparable SWE-bench to Opus-class models, fine-grained tool
		// use. Keeping them in Medium silently triggered decompose=-1
		// + 1.2× thinking budget multiplier — both of which over-fire
		// for the current Kimi generation. Older kimi-k2.5 stays Medium.
		m := strings.ToLower(modelName)
		if p == "kimi" && isStrongKimiCodingModel(m) {
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

func isStrongKimiCodingModel(modelName string) bool {
	m := strings.ToLower(strings.TrimSpace(modelName))
	if strings.HasPrefix(m, "kimi-for-coding") {
		return true
	}

	const prefix = "kimi-k2."
	if !strings.HasPrefix(m, prefix) {
		return false
	}
	rest := m[len(prefix):]
	end := 0
	for end < len(rest) {
		if rest[end] < '0' || rest[end] > '9' {
			break
		}
		end++
	}
	if end == 0 {
		return false
	}
	minor, err := strconv.Atoi(rest[:end])
	if err != nil {
		return false
	}
	return minor >= 6
}
