package app

import (
	"context"

	"gokin/internal/config"
	"gokin/internal/security"
)

func bashSandboxConfig(cfg *config.Config) security.SandboxConfig {
	policy := security.DefaultSandboxConfig()
	if cfg != nil {
		policy.Enabled = cfg.Tools.Bash.Sandbox || cfg.Tools.Bash.RequireSandbox
		policy.AllowNetwork = cfg.Tools.Bash.AllowNetwork
		policy.RequireSandbox = cfg.Tools.Bash.RequireSandbox
	}
	if path := config.GetConfigPath(); path != "" {
		policy.SensitivePaths = append(policy.SensitivePaths, path)
	}
	return policy
}

// GetBashSandboxStatus reports the run's actual probe, not executable presence
// or the REPL's independent availability.
func (a *App) GetBashSandboxStatus() security.SandboxStatus {
	if a == nil {
		return (*security.SandboxPolicy)(nil).Status(context.Background())
	}
	return a.bashSandbox.Status(context.Background())
}
