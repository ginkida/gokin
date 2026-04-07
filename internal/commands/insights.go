package commands

import (
	"context"
)

// InsightsCommand requests the learning insights dashboard.
type InsightsCommand struct{}

func (c *InsightsCommand) Name() string {
	return "insights"
}

func (c *InsightsCommand) Description() string {
	return "Show learning insights: strategy metrics, patterns, delegation stats"
}

func (c *InsightsCommand) Usage() string {
	return "/insights"
}

func (c *InsightsCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategorySession,
		Icon:     "bulb",
		Priority: 45,
	}
}

func (c *InsightsCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	// Let the App handle the rendering of insights
	if viewRenderer, ok := app.(interface{ HandleInsightsCommand() string }); ok {
		return viewRenderer.HandleInsightsCommand(), nil
	}
	return "Insights functionality not fully supported by this app instance", nil
}
