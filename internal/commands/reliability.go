package commands

import "context"

// HealthCommand displays runtime reliability and provider health information.
type HealthCommand struct{}

func (c *HealthCommand) Name() string        { return "health" }
func (c *HealthCommand) Description() string { return "Show runtime health and provider reliability" }
func (c *HealthCommand) Usage() string       { return "/health" }
func (c *HealthCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryPlanning,
		Icon:     "heartbeat",
		Priority: 20,
	}
}

func (c *HealthCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	return app.GetRuntimeHealthReport(), nil
}

// PolicyCommand displays current policy-engine state.
type PolicyCommand struct{}

func (c *PolicyCommand) Name() string        { return "policy" }
func (c *PolicyCommand) Description() string { return "Show policy engine and circuit breaker state" }
func (c *PolicyCommand) Usage() string       { return "/policy" }
func (c *PolicyCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryPlanning,
		Icon:     "shield",
		Priority: 30,
	}
}

func (c *PolicyCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	return app.GetPolicyReport(), nil
}

// LedgerCommand displays current plan run ledger diagnostics.
type LedgerCommand struct{}

func (c *LedgerCommand) Name() string        { return "ledger" }
func (c *LedgerCommand) Description() string { return "Show run ledger for current plan" }
func (c *LedgerCommand) Usage() string       { return "/ledger" }
func (c *LedgerCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryPlanning,
		Icon:     "list",
		Priority: 40,
	}
}

func (c *LedgerCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	return app.GetLedgerReport(), nil
}
