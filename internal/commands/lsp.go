package commands

import (
	"context"
	"fmt"
)

type LSPCommand struct{}

func (*LSPCommand) Name() string        { return "lsp" }
func (*LSPCommand) Description() string { return "Inspect plugin language servers and startup errors" }
func (*LSPCommand) Usage() string       { return "/lsp" }
func (*LSPCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{Category: CategoryTools, Icon: "code", Priority: 41}
}
func (c *LSPCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	if len(args) != 0 {
		return "", fmt.Errorf("usage: %s", c.Usage())
	}
	if runtime, ok := app.(interface{ GetLSPStatus() string }); ok {
		return runtime.GetLSPStatus(), nil
	}
	return "", fmt.Errorf("LSP inspection is unavailable in this runtime")
}
