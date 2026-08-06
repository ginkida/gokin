package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gokin/internal/config"
)

const (
	// Keep an accidental typo from making every provider request fail almost
	// instantly or from disabling the zombie backstop for hours. Advanced users
	// can still edit YAML directly; the interactive command chooses safe bounds.
	minModelRoundTimeout = time.Minute
	maxModelRoundTimeout = 2 * time.Hour
)

// TimeoutCommand shows or changes the hard cap for one model provider round.
// The setting is live: ApplyConfig propagates it to the foreground executor and
// to existing/future sub-agents for their next round.
type TimeoutCommand struct{}

// ModelRoundTimeoutApplier is the narrow runtime path implemented by App. It
// avoids rebuilding the provider client for a local watchdog-only change.
// Lightweight integrations may omit it and fall back to the full ApplyConfig.
type ModelRoundTimeoutApplier interface {
	ApplyModelRoundTimeout(cfg *config.Config) (persisted bool, err error)
}

func (c *TimeoutCommand) Name() string { return "timeout" }
func (c *TimeoutCommand) Description() string {
	return "View or change the model round timeout"
}
func (c *TimeoutCommand) Usage() string {
	return `/timeout              - Show the effective model round timeout
/timeout 20m          - Set and apply a new timeout
/timeout default      - Restore the recommended default`
}
func (c *TimeoutCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "clock",
		Priority: 26,
		HasArgs:  true,
		ArgHint:  "<duration|default>",
	}
}

func effectiveModelRoundTimeout(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.Tools.ModelRoundTimeout <= 0 {
		return config.DefaultModelRoundTimeout
	}
	return cfg.Tools.ModelRoundTimeout
}

func (c *TimeoutCommand) Execute(_ context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Config not available", nil
	}
	if len(args) == 0 {
		return timeoutStatus(effectiveModelRoundTimeout(cfg)), nil
	}
	if len(args) != 1 {
		return fmt.Sprintf("Expected one duration.\n\n%s", c.Usage()), nil
	}

	raw := strings.ToLower(strings.TrimSpace(args[0]))
	timeout := config.DefaultModelRoundTimeout
	if raw != "default" && raw != "reset" && raw != "0" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Sprintf("Invalid duration %q — include a unit, for example /timeout 20m.\n\n%s", args[0], c.Usage()), nil
		}
		timeout = parsed
	}

	if timeout < minModelRoundTimeout {
		return fmt.Sprintf("Timeout too short: %s (minimum %s).", timeout, minModelRoundTimeout), nil
	}
	if timeout > maxModelRoundTimeout {
		return fmt.Sprintf("Timeout too long: %s (maximum %s).", timeout, maxModelRoundTimeout), nil
	}
	if effectiveModelRoundTimeout(cfg) == timeout {
		return fmt.Sprintf("Model round timeout is already %s — nothing changed.", timeout), nil
	}

	cfg.Tools.ModelRoundTimeout = timeout
	persisted := true
	var err error
	if applier, ok := app.(ModelRoundTimeoutApplier); ok {
		persisted, err = applier.ApplyModelRoundTimeout(cfg)
	} else {
		err = app.ApplyConfig(cfg)
	}
	if err != nil {
		return fmt.Sprintf("Failed to apply model round timeout: %v", err), nil
	}
	if !persisted {
		return fmt.Sprintf("⚠ model round timeout: %s — applied for this session but NOT saved; it will revert next launch", timeout), nil
	}
	return fmt.Sprintf("✓ model round timeout: %s — saved and applied to foreground and sub-agents for the next round; shorter explicit outer deadlines still win", timeout), nil
}

func timeoutStatus(timeout time.Duration) string {
	status := fmt.Sprintf(`Model round timeout: %s
  Applies to one provider response in foreground and sub-agents.
	  Active streaming has a separate idle watchdog; this is the final safety cap.
	  A shorter explicit quick-agent, plan, coordinate, loop, or headless deadline still wins.`, timeout)
	if timeout < config.DefaultModelRoundTimeout {
		status += fmt.Sprintf("\n  ⚠ Shorter than the recommended %s; heavy reasoning may be interrupted.", config.DefaultModelRoundTimeout)
	}
	status += "\n\nChange with /timeout 20m · restore with /timeout default"
	return status
}
