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

// JournalCommand displays recent execution journal events.
type JournalCommand struct{}

func (c *JournalCommand) Name() string        { return "journal" }
func (c *JournalCommand) Description() string { return "Show recent execution journal events" }
func (c *JournalCommand) Usage() string       { return "/journal" }
func (c *JournalCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryPlanning,
		Icon:     "history",
		Priority: 50,
	}
}

func (c *JournalCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	return app.GetJournalReport(), nil
}

// RecoveryCommand displays persisted recovery snapshot.
type RecoveryCommand struct{}

func (c *RecoveryCommand) Name() string        { return "recovery" }
func (c *RecoveryCommand) Description() string { return "Show latest recovery snapshot" }
func (c *RecoveryCommand) Usage() string       { return "/recovery" }
func (c *RecoveryCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryPlanning,
		Icon:     "restore",
		Priority: 60,
	}
}

func (c *RecoveryCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	return app.GetRecoveryReport(), nil
}

// ObservabilityCommand provides a unified reliability dashboard.
type ObservabilityCommand struct{}

func (c *ObservabilityCommand) Name() string        { return "observability" }
func (c *ObservabilityCommand) Description() string { return "Show unified observability dashboard" }
func (c *ObservabilityCommand) Usage() string       { return "/observability" }
func (c *ObservabilityCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryPlanning,
		Icon:     "dashboard",
		Priority: 70,
	}
}

func (c *ObservabilityCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	return app.GetObservabilityReport(), nil
}

// MemoryGovernanceCommand shows session memory governance stats.
type MemoryGovernanceCommand struct{}

func (c *MemoryGovernanceCommand) Name() string { return "memory-governance" }
func (c *MemoryGovernanceCommand) Description() string {
	return "Show session memory governance status"
}
func (c *MemoryGovernanceCommand) Usage() string { return "/memory-governance" }
func (c *MemoryGovernanceCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryPlanning,
		Icon:     "memory",
		Priority: 80,
	}
}

func (c *MemoryGovernanceCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	return app.GetSessionGovernanceReport(), nil
}
