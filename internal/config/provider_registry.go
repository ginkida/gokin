package config

import "strings"

// ProviderDef is the single source of truth for a provider's metadata.
// All provider-specific logic (loadFromEnv, Validate, HasProvider, SetProviderKey,
// GetActiveKey, commands, wizard, UI) iterates Providers instead of switch/case.
type ProviderDef struct {
	Name          string                           // "anthropic"
	DisplayName   string                           // "Anthropic (Claude)"
	DefaultModel  string                           // "claude-sonnet-4-5-20250929"
	EnvVars       []string                         // {"GOKIN_ANTHROPIC_KEY", "ANTHROPIC_API_KEY"}
	UsesLegacyKey bool                             // true = fallback to APIKey
	KeyOptional   bool                             // true for ollama
	HasOAuth      bool                             // true for gemini
	GetKey        func(api *APIConfig) string      // read the key field
	SetKey        func(api *APIConfig, key string) // write the key field
	ModelPrefixes []string                         // {"claude"} — auto-detect provider by model name
	SetupKeyURL   string                           // "https://console.anthropic.com/settings/keys"
	KeyValidation KeyValidationDef                 // Optional online API key validation settings
}

// KeyValidationDef describes how to validate an API key online for a provider.
type KeyValidationDef struct {
	URL             string            // Endpoint used for lightweight key validation
	AuthMode        string            // "query", "bearer", or "header"
	QueryParam      string            // For AuthMode=query, e.g. "key"
	HeaderName      string            // For AuthMode=header, e.g. "x-api-key"
	ExtraHeaders    map[string]string // Additional static headers
	SuccessStatuses []int             // Accepted HTTP status codes (default: 200)
}

// Providers is the ordered list of all supported providers.
// Add a new provider here + one field in APIConfig + one factory case in client/factory.go.
// Optional but recommended: add key validation endpoint and setup copy in internal/setup/wizard.go.
var Providers = []ProviderDef{
	{
		Name:          "gemini",
		DisplayName:   "Gemini",
		DefaultModel:  "gemini-3-flash-preview",
		EnvVars:       []string{"GOKIN_GEMINI_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"},
		UsesLegacyKey: true,
		HasOAuth:      true,
		GetKey:        func(api *APIConfig) string { return api.GeminiKey },
		SetKey:        func(api *APIConfig, key string) { api.GeminiKey = key },
		ModelPrefixes: []string{"gemini", "models/"},
		SetupKeyURL:   "https://aistudio.google.com/apikey",
		KeyValidation: KeyValidationDef{
			URL:             "https://generativelanguage.googleapis.com/v1beta/models",
			AuthMode:        "query",
			QueryParam:      "key",
			SuccessStatuses: []int{200},
		},
	},
	{
		Name:          "anthropic",
		DisplayName:   "Anthropic (Claude)",
		DefaultModel:  "claude-sonnet-4-5-20250929",
		EnvVars:       []string{"GOKIN_ANTHROPIC_KEY", "ANTHROPIC_API_KEY"},
		UsesLegacyKey: true,
		GetKey:        func(api *APIConfig) string { return api.AnthropicKey },
		SetKey:        func(api *APIConfig, key string) { api.AnthropicKey = key },
		ModelPrefixes: []string{"claude"},
		SetupKeyURL:   "https://console.anthropic.com/settings/keys",
		KeyValidation: KeyValidationDef{
			URL:        "https://api.anthropic.com/v1/models",
			AuthMode:   "header",
			HeaderName: "x-api-key",
			ExtraHeaders: map[string]string{
				"anthropic-version": "2023-06-01",
			},
			SuccessStatuses: []int{200},
		},
	},
	{
		Name:          "glm",
		DisplayName:   "GLM Coding Plan (Z.ai)",
		DefaultModel:  "glm-5",
		EnvVars:       []string{"GOKIN_GLM_KEY", "GLM_API_KEY"},
		UsesLegacyKey: true,
		GetKey:        func(api *APIConfig) string { return api.GLMKey },
		SetKey:        func(api *APIConfig, key string) { api.GLMKey = key },
		ModelPrefixes: []string{"glm"},
		SetupKeyURL:   "https://open.bigmodel.cn",
		KeyValidation: KeyValidationDef{
			URL:             "https://open.bigmodel.cn/api/paas/v4/models",
			AuthMode:        "bearer",
			SuccessStatuses: []int{200},
		},
	},
	{
		Name:          "deepseek",
		DisplayName:   "DeepSeek",
		DefaultModel:  "deepseek-chat",
		EnvVars:       []string{"GOKIN_DEEPSEEK_KEY", "DEEPSEEK_API_KEY"},
		UsesLegacyKey: true,
		GetKey:        func(api *APIConfig) string { return api.DeepSeekKey },
		SetKey:        func(api *APIConfig, key string) { api.DeepSeekKey = key },
		ModelPrefixes: []string{"deepseek"},
		SetupKeyURL:   "https://platform.deepseek.com/api_keys",
		KeyValidation: KeyValidationDef{
			URL:             "https://api.deepseek.com/models",
			AuthMode:        "bearer",
			SuccessStatuses: []int{200},
		},
	},
	{
		Name:          "minimax",
		DisplayName:   "MiniMax",
		DefaultModel:  "MiniMax-M2.5",
		EnvVars:       []string{"GOKIN_MINIMAX_KEY", "MINIMAX_API_KEY"},
		UsesLegacyKey: true,
		GetKey:        func(api *APIConfig) string { return api.MiniMaxKey },
		SetKey:        func(api *APIConfig, key string) { api.MiniMaxKey = key },
		ModelPrefixes: []string{"minimax"},
		SetupKeyURL:   "https://platform.minimaxi.com/user-center/basic-information/interface-key",
		KeyValidation: KeyValidationDef{
			URL:             "https://api.minimax.io/v1/models",
			AuthMode:        "bearer",
			SuccessStatuses: []int{200},
		},
	},
	{
		Name:          "kimi",
		DisplayName:   "Kimi Code (Moonshot)",
		DefaultModel:  "kimi-k2.5",
		EnvVars:       []string{"GOKIN_KIMI_KEY", "KIMI_API_KEY", "MOONSHOT_API_KEY"},
		UsesLegacyKey: true,
		GetKey:        func(api *APIConfig) string { return api.KimiKey },
		SetKey:        func(api *APIConfig, key string) { api.KimiKey = key },
		ModelPrefixes: []string{"kimi", "moonshot"},
		SetupKeyURL:   "https://platform.moonshot.ai/console/api-keys",
		KeyValidation: KeyValidationDef{
			URL:             "https://api.moonshot.ai/v1/models",
			AuthMode:        "bearer",
			SuccessStatuses: []int{200},
		},
	},
	{
		Name:          "openai",
		DisplayName:   "OpenAI (ChatGPT)",
		DefaultModel:  "gpt-5.3-codex",
		KeyOptional:   true, // Only OAuth, no API key
		HasOAuth:      true,
		GetKey:        func(api *APIConfig) string { return "" },
		SetKey:        func(api *APIConfig, key string) {},
		ModelPrefixes: []string{"gpt", "o1", "o3", "o4"},
	},
	{
		Name:         "ollama",
		DisplayName:  "Ollama (local)",
		DefaultModel: "llama3.2",
		EnvVars:      []string{"GOKIN_OLLAMA_KEY", "OLLAMA_API_KEY"},
		KeyOptional:  true,
		GetKey:       func(api *APIConfig) string { return api.OllamaKey },
		SetKey:       func(api *APIConfig, key string) { api.OllamaKey = key },
		ModelPrefixes: []string{
			"llama", "qwen", "codellama", "mistral", "phi", "gemma",
			"vicuna", "yi", "starcoder", "wizardcoder", "orca", "neural", "solar",
			"openchat", "zephyr", "dolphin", "nous", "tinyllama", "stablelm",
		},
	},
}

// providerByName is an O(1) lookup map built at init time.
var providerByName map[string]*ProviderDef

func init() {
	providerByName = make(map[string]*ProviderDef, len(Providers))
	for i := range Providers {
		providerByName[Providers[i].Name] = &Providers[i]
	}
}

// GetProvider returns the provider definition by name, or nil if not found.
func GetProvider(name string) *ProviderDef {
	return providerByName[name]
}

// ProviderNames returns all provider names in registry order.
func ProviderNames() []string {
	names := make([]string, len(Providers))
	for i, p := range Providers {
		names[i] = p.Name
	}
	return names
}

// KeyProviderNames returns provider names that require an API key (excludes ollama).
// Used by /login command.
func KeyProviderNames() []string {
	var names []string
	for _, p := range Providers {
		if !p.KeyOptional {
			names = append(names, p.Name)
		}
	}
	return names
}

// AllProviderNames returns all provider names plus "all" — for /logout.
func AllProviderNames() []string {
	names := ProviderNames()
	return append(names, "all")
}

// DetectProviderFromModel determines the provider from a model name
// by matching against each provider's ModelPrefixes.
func DetectProviderFromModel(modelName string) string {
	if modelName == "" {
		return "gemini"
	}
	lower := strings.ToLower(modelName)
	for _, p := range Providers {
		for _, prefix := range p.ModelPrefixes {
			if strings.HasPrefix(lower, prefix) {
				return p.Name
			}
		}
	}
	return "gemini" // default
}

// AnyProviderHasKey returns true if any provider has a configured key or OAuth.
func AnyProviderHasKey(api *APIConfig) bool {
	for _, p := range Providers {
		if p.GetKey(api) != "" {
			return true
		}
	}
	return api.APIKey != "" || api.HasOAuthToken("gemini") || api.HasOAuthToken("openai")
}
