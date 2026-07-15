package commands

import (
	"context"
	"fmt"
)

// QuickstartCommand provides a quick start guide.
type QuickstartCommand struct{}

const (
	// Header style matches /stats, /tree-stats, /doctor (post-v0.84.7):
	// lowercase muted label, no banner, no double-border. The boxed
	// ╔═╗ form was the second-to-last in the codebase after /doctor's
	// strip; quickstart is the last surface still using it.
	quickstartHeader = `
%sQuick Start with Gokin%s

Gokin is an AI assistant that understands your project context
and helps with coding using natural language.

%s─── 5 simple examples to get started ───%s
`

	quickstartExamples = `
%s1. Working with Files%s

    "Read README.md"
    "Create a config.yaml file with database settings"
    "Add logging to the ProcessRequest function"

%s2. Code Search%s

    "Find all .go files in the cmd directory"
    "Show all functions that start with Handle"
    "Find where the config variable is used"

%s3. Running Commands%s

    "Run tests in the internal/auth package"
    "Build the project and show the binary size"
    "Install dependencies"

%s4. Working with Git%s

    "Show git status"
    "Create a commit with message 'fix: resolve login issue'"
    "Show the last 5 commits"

%s5. Refactoring%s

    "Rename function oldName to newName in all files"
    "Extract validation logic into a separate function"
    "Find duplicate code"
`

	quickstartSafety = `
%s─── safety & session modes ───%s

  %sNormal%s  — asks before write, edit, or bash actions
  %sPlan%s    — explores read-only, then proposes a plan for approval
  %sYOLO%s    — permissions and sandbox are off; commands run without asking

  %sShift+Tab%s cycles Normal → Plan → YOLO. The active mode stays visible
  in the bottom status bar. During active work, %sEsc%s cancels the operation.
`

	quickstartTips = `
%s─── helpful tips ───%s

  • %sBe specific%s           — the more detail, the better the result
  • %sUse context%s           — "In main.go find the main function"
  • %sAsk for explanations%s  — "Explain how this code works"
  • %sReview changes%s        — use git diff to review changes

%s─── key commands ───%s

  %s/help%s        Help for all commands
  %s/quickstart%s  Open this guide again
  %s/doctor%s      Check setup and diagnostics
  %s/plan%s        Toggle plan mode for complex tasks
  %s/resume-plan%s Continue paused plan execution
  %s/model%s       Switch AI model
  %s/update%s      Check or install updates
  %s/clear%s       Clear chat history

%s─── ready to start? ───%s

Just start asking questions — Gokin understands natural language.

%sExample:%s "Analyze the project structure and suggest improvements"
`
)

func (c *QuickstartCommand) Name() string {
	return "quickstart"
}

func (c *QuickstartCommand) Description() string {
	return "Quick start guide with examples"
}

func (c *QuickstartCommand) Usage() string {
	return "/quickstart"
}

func (c *QuickstartCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryGettingStarted,
		Icon:     "rocket",
		Priority: 10,
	}
}

func (c *QuickstartCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	return c.getQuickstart(), nil
}

func (c *QuickstartCommand) getQuickstart() string {
	header := fmt.Sprintf(quickstartHeader, colorCyan, colorReset, colorYellow, colorReset)
	examples := fmt.Sprintf(quickstartExamples, colorCyan, colorReset, colorCyan, colorReset, colorCyan, colorReset, colorCyan, colorReset, colorCyan, colorReset)
	safety := fmt.Sprintf(quickstartSafety,
		colorYellow, colorReset,
		colorGreen, colorReset,
		colorCyan, colorReset,
		colorRed, colorReset,
		colorGreen, colorReset,
		colorGreen, colorReset)
	tips := fmt.Sprintf(quickstartTips,
		colorYellow, colorReset,
		colorGreen, colorReset,
		colorGreen, colorReset,
		colorGreen, colorReset,
		colorGreen, colorReset,
		colorYellow, colorReset,
		colorGreen, colorReset,
		colorGreen, colorReset,
		colorGreen, colorReset,
		colorGreen, colorReset,
		colorGreen, colorReset,
		colorGreen, colorReset,
		colorGreen, colorReset,
		colorGreen, colorReset,
		colorYellow, colorReset,
		colorCyan, colorReset)
	return header + examples + safety + tips
}
