package auth

// OpenAI OAuth credentials (public client, no client secret)
const (
	OpenAIOAuthClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	OpenAIOAuthCallbackPort = 1455
	OpenAIOAuthCallbackPath = "/auth/callback"
)

// OpenAI OAuth endpoints
const (
	OpenAIAuthURL  = "https://auth.openai.com/authorize"
	OpenAITokenURL = "https://auth.openai.com/oauth/token"
)

// OpenAI API endpoint (Codex Responses API)
const (
	OpenAICodexAPI = "https://chatgpt.com/backend-api/codex/responses"
)

// OpenAI OAuth scopes
const (
	OpenAIScopeOpenID         = "openid"
	OpenAIScopeProfile        = "profile"
	OpenAIScopeEmail          = "email"
	OpenAIScopeOfflineAccess  = "offline_access"
)

// OpenAI API headers (required for Codex API routing)
var OpenAICodexHeaders = map[string]string{
	"OpenAI-Beta": "responses=experimental",
	"originator":  "codex_cli_rs",
}

// OpenAI OAuth audience
const OpenAIOAuthAudience = "https://api.openai.com/v1"
