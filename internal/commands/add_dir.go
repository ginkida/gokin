package commands

import (
	"context"
	"fmt"
	"strings"
)

// AddDirCommand grants the agent access to a directory OUTSIDE the workspace —
// the Claude-Code-style /add-dir. With no args it lists the directories the
// agent may currently access; with a path it grants access for this session.
// "--persist" (or "-p") also saves the grant to config so it survives restart.
type AddDirCommand struct{}

func (c *AddDirCommand) Name() string { return "add-dir" }
func (c *AddDirCommand) Description() string {
	return "Grant the agent access to a directory outside the workspace"
}
func (c *AddDirCommand) Usage() string { return "/add-dir [--persist] <path>" }
func (c *AddDirCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "folder",
		Priority: 55,
		HasArgs:  true,
		ArgHint:  "[--persist] <path>",
	}
}

func (c *AddDirCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	// No args -> list currently accessible directories.
	if len(args) == 0 || strings.TrimSpace(strings.Join(args, " ")) == "" {
		return renderAllowedDirs(app), nil
	}

	persist := false
	rest := make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "--persist", "-p", "--save":
			persist = true
		default:
			rest = append(rest, a)
		}
	}
	path := strings.TrimSpace(strings.Join(rest, " "))
	if path == "" {
		return "", fmt.Errorf("usage: %s", c.Usage())
	}

	resolved, err := app.GrantAllowedDir(path, persist)
	if err != nil {
		// A persistence-only failure still granted for the session — surface it
		// as a soft message, not a hard error, so the grant isn't reported lost.
		if resolved != "" {
			return fmt.Sprintf("Granted access to %s for this session.\nNote: %s", resolved, err), nil
		}
		return "", err
	}

	scope := "this session"
	tail := "Use /remove-dir to revoke, or re-run with --persist to keep it across restarts."
	if persist {
		scope = "this session and saved to config"
		tail = "Use /remove-dir to revoke the session grant (the config entry persists)."
	}
	return fmt.Sprintf("Granted access to %s (%s).\nThe agent can now read and work in this directory; writes still prompt as usual. %s", resolved, scope, tail), nil
}

// RemoveDirCommand revokes a SESSION directory grant. Persisted config dirs are
// not removed (edit config to change those).
type RemoveDirCommand struct{}

func (c *RemoveDirCommand) Name() string { return "remove-dir" }
func (c *RemoveDirCommand) Description() string {
	return "Revoke a session directory grant added with /add-dir"
}
func (c *RemoveDirCommand) Usage() string { return "/remove-dir <path>" }
func (c *RemoveDirCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "folder",
		Priority: 54,
		HasArgs:  true,
		ArgHint:  "<path>",
	}
}

func (c *RemoveDirCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	path := strings.TrimSpace(strings.Join(args, " "))
	if path == "" {
		return "", fmt.Errorf("usage: %s", c.Usage())
	}
	removed, err := app.RevokeGrantedDir(path)
	if err != nil {
		return "", err
	}
	if !removed {
		return fmt.Sprintf("No session grant found for %s.\n%s", path, renderAllowedDirs(app)), nil
	}
	return fmt.Sprintf("Revoked session access to %s.", path), nil
}

func renderAllowedDirs(app AppInterface) string {
	dirs := app.ListAllowedDirs()
	var sb strings.Builder
	sb.WriteString("Workspace: ")
	sb.WriteString(app.GetWorkDir())
	sb.WriteString("\n")
	if len(dirs) == 0 {
		sb.WriteString("\nNo additional directories granted.\n")
		sb.WriteString("Grant one with: /add-dir <path>  (add --persist to keep it across restarts)")
		return sb.String()
	}
	sb.WriteString("\nAdditional directories the agent can access:\n")
	for _, d := range dirs {
		fmt.Fprintf(&sb, "  • %s\n", d)
	}
	sb.WriteString("\nGrant more with /add-dir <path>, revoke a session grant with /remove-dir <path>.")
	return sb.String()
}
