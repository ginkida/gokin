package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
)

// processTracker tracks spawned editor processes for cleanup on exit.
var (
	spawnedPIDs   []int
	spawnedPIDsMu sync.Mutex
)

// trackPID adds a process ID to the tracking list.
func trackPID(pid int) {
	spawnedPIDsMu.Lock()
	defer spawnedPIDsMu.Unlock()
	spawnedPIDs = append(spawnedPIDs, pid)
}

// CleanupSpawnedProcesses terminates all tracked editor processes.
// Call this during graceful shutdown.
func CleanupSpawnedProcesses() {
	spawnedPIDsMu.Lock()
	defer spawnedPIDsMu.Unlock()
	for _, pid := range spawnedPIDs {
		if proc, err := os.FindProcess(pid); err == nil {
			// Send SIGTERM for graceful shutdown, not SIGKILL
			_ = proc.Signal(syscall.SIGTERM)
		}
	}
	spawnedPIDs = nil
}

// OpenCommand opens a file in the system's default editor.
type OpenCommand struct{}

func (c *OpenCommand) Name() string        { return "open" }
func (c *OpenCommand) Description() string { return "Open a file in your editor" }
func (c *OpenCommand) Usage() string       { return "/open <file>" }
func (c *OpenCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "edit",
		Priority: 20,
		HasArgs:  true,
		ArgHint:  "<file>",
	}
}

func (c *OpenCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	if len(args) == 0 {
		return "Usage: /open <file>\n\nOpens the specified file in your default editor ($EDITOR or vi).\n\nExamples:\n  /open main.go\n  /open internal/app/app.go", nil
	}

	filePath := args[0]

	// Get the working directory
	workDir := app.GetWorkDir()

	// Resolve the file path
	absPath := filePath
	if !filepath.IsAbs(filePath) {
		absPath = filepath.Join(workDir, filePath)
	}

	// Check if file exists
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return fmt.Sprintf("Error: File not found: %s", absPath), nil
	}

	// Determine the editor to use
	editor := os.Getenv("EDITOR")
	if editor == "" {
		// Default editors by OS
		switch runtime.GOOS {
		case "windows":
			editor = "notepad"
		case "darwin":
			editor = "open"
		default:
			editor = "vi"
		}
	}

	// For macOS, use "open -t" to open in default text editor
	if runtime.GOOS == "darwin" && editor == "open" {
		editor = "open -t"
	}

	// Execute the editor command.
	// IMPORTANT: NEVER invoke the editor through `sh -c`. Earlier code did
	// `sh -c "<editor> <path>"` which was a shell-injection vector on two
	// axes:
	//   1. The path comes from user input (`/open <file>`) and could contain
	//      metacharacters like `;`, `|`, backticks, `$()`. `/open foo;whoami`
	//      would execute `whoami`.
	//   2. The $EDITOR env var is user-controlled at shell level but could
	//      be set maliciously via an inherited environment.
	// exec.Command argv-style (no shell) treats each token as a literal
	// argument — no interpretation. We still split EDITOR on whitespace
	// because users legitimately set things like `EDITOR="code --wait"` or
	// `open -t`, and that's a positional-arg convention that doesn't require
	// shell metachar interpretation.
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "start", "", absPath)
	} else {
		editorCmd, editorArgs, ok := parseEditorCommand(editor)
		if !ok {
			return "Error: $EDITOR is empty. Set EDITOR=<your-editor>.", nil
		}
		argv := append(append([]string(nil), editorArgs...), absPath)
		cmd = exec.Command(editorCmd, argv...)
	}

	// Detach from parent process so the editor continues running
	if runtime.GOOS != "windows" {
		cmd.Stdin = nil
		cmd.Stdout = nil
		cmd.Stderr = nil
	}

	// Start the command (don't wait)
	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("Error opening file: %v\n\nEditor: %s\nFile: %s", err, editor, absPath), nil
	}

	// Track the spawned process for cleanup
	if cmd.Process != nil {
		trackPID(cmd.Process.Pid)

		// Release the process so it can be reaped by init when we exit.
		// Non-fatal: editor still opened, just might leave a zombie.
		_ = cmd.Process.Release()
	}

	return fmt.Sprintf("Opening %s in %s...", filePath, editor), nil
}

// parseEditorCommand splits a user-supplied $EDITOR value into (executable,
// extra-args) suitable for exec.Command — no shell is involved. Users
// legitimately set values like "code --wait" or "open -t"; those split on
// whitespace into positional tokens that exec.Command handles directly.
//
// Critically, tokens are treated as literal argv entries: `;`, `|`,
// backticks, and `$()` have no special meaning. The old code ran
// `sh -c "<editor> <path>"` which interpreted those as shell
// metacharacters, enabling command injection via either a malicious $EDITOR
// or a file path containing shell syntax.
//
// Returns ok=false only when the editor value is empty or whitespace-only —
// callers should surface that to the user rather than exec'ing an empty
// command.
func parseEditorCommand(editor string) (string, []string, bool) {
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return "", nil, false
	}
	return parts[0], parts[1:], true
}
