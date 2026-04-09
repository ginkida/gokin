package commands

import "context"

// MemoryCommand shows stored memories.
type MemoryCommand struct{}

func (c *MemoryCommand) Name() string        { return "memory" }
func (c *MemoryCommand) Description() string { return "Show stored memories" }
func (c *MemoryCommand) Usage() string       { return "/memory" }
func (c *MemoryCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategorySession,
		Icon:     "memory",
		Priority: 55,
	}
}

func (c *MemoryCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	return app.GetMemoryReport(), nil
}
