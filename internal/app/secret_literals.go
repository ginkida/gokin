package app

import (
	"gokin/internal/config"
	"gokin/internal/security"
)

// registerConfigSecrets registers every credential gokin holds as an exact
// literal for the process-wide redactor, so a key that reaches tool output by
// any route (a test printing its env, a config file the model reads, bash
// output) is scrubbed even though it has no recognizable token shape. GLM and
// DeepSeek keys are opaque strings the pattern redactor cannot recognise.
// Safe to call repeatedly (ApplyConfig after /login adds the new key).
func registerConfigSecrets(cfg *config.Config) {
	if cfg == nil {
		return
	}
	for _, key := range []string{
		cfg.API.APIKey,
		cfg.API.GLMKey,
		cfg.API.DeepSeekKey,
		cfg.API.OllamaKey,
		cfg.API.GetActiveKey(),
		cfg.Web.SearchAPIKey,
	} {
		security.RegisterSecretLiteral(key)
	}
}
