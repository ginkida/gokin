package client

import "strings"

// Local Ollama models keep their inference metadata even when their hosted
// providers have been removed. These profiles do not register hosted clients.
func ollamaCompatibilityProfile(baseName string) (ModelProfile, bool) {
	switch {
	case strings.HasPrefix(baseName, "k3"):
		return ModelProfile{Family: "kimi", ContextWindow: 1048576, SupportsTools: true, IsCoding: true}, true
	case strings.HasPrefix(baseName, "kimi"):
		return ModelProfile{Family: "kimi", ContextWindow: 262144, SupportsTools: true, IsCoding: true}, true
	case strings.HasPrefix(baseName, "moonshot"):
		return ModelProfile{Family: "kimi", ContextWindow: 128000, SupportsTools: true}, true
	case strings.HasPrefix(baseName, "minimax"):
		return ModelProfile{Family: "minimax", ContextWindow: 204800, SupportsTools: true, IsCoding: true}, true
	default:
		return ModelProfile{}, false
	}
}
