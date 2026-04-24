package context

// ToolUsageGuide provides detailed instructions for how to use each tool effectively.
type ToolUsageGuide struct {
	Description    string // What the tool does
	WhenToUse      string // When this tool is appropriate
	HowToRespond   string // How to respond after using this tool
	CommonMistakes string // What NOT to do
	Examples       string // Example usage patterns
}

// ToolUsageGuides contains guidance for each tool to help the model use them correctly.
var ToolUsageGuides = map[string]ToolUsageGuide{
	"read": {
		Description: "Reads file contents with line numbers. Supports text files, PDFs, images, and Jupyter notebooks.",
		WhenToUse: `Use when you need to:
- Understand what a file contains
- Find specific code in a known file
- Analyze code structure
- Check configuration files`,
		HowToRespond: `After reading a file, ALWAYS explain:
1. What the file contains (purpose, main functions/classes)
2. Key code sections with line numbers
3. Patterns or issues you noticed
4. How it relates to the user's question`,
		CommonMistakes: `DON'T:
- Read file and say nothing
- Just quote the entire file
- Give vague summaries like "it's a config file"`,
		Examples: `GOOD: "I read main.go (245 lines). It's the entry point that:
- Lines 12-45: Sets up CLI with Cobra
- Lines 50-80: Initializes database
- Lines 85-120: Starts HTTP server
Key observation: Error handling at line 92 could miss connection timeouts."`,
	},

	"grep": {
		Description: "Searches for regex patterns in files. Returns matching lines with file paths and line numbers.",
		WhenToUse: `Use when you need to:
- Find where a function/variable is used
- Search for patterns across codebase
- Find all occurrences of a string
- Locate error messages or TODOs`,
		HowToRespond: `After searching, ALWAYS explain:
1. How many matches found and in how many files
2. Group results by category/purpose
3. Highlight the most relevant matches
4. If no results, explain why and suggest alternatives`,
		CommonMistakes: `DON'T:
- Just list raw grep output
- Say "no matches" without explanation
- Search without analyzing results`,
		Examples: `GOOD: "Found 'handleError' in 12 locations across 5 files:

**Error Handlers (3 files):**
- handler/errors.go:25 - Main error handler
- middleware/recovery.go:12 - Panic recovery

**Usage (2 files):**
- api/users.go:45, 67, 89 - User endpoint errors
- api/orders.go:34 - Order validation errors

Pattern: All errors are wrapped with stack traces before returning."`,
	},

	"glob": {
		Description: "Finds files matching a glob pattern. Supports ** for recursive matching. Limited to 1000 results, sorted by modification time (newest first).",
		WhenToUse: `Use when you need to:
- Find files by extension (*.go, *.ts)
- Explore project structure
- Find files in specific directories
- Identify configuration files`,
		HowToRespond: `After finding files, ALWAYS:
1. Summarize what types of files were found
2. Highlight important/relevant files
3. Suggest which files to read next
4. If no results, suggest alternative patterns`,
		CommonMistakes: `DON'T:
- Just list file names without context
- Say "found X files" without explaining relevance
- Ignore the file structure implications`,
		Examples: `GOOD: "Found 23 Go files in the project:

**Entry Points (cmd/):**
- cmd/api/main.go - API server
- cmd/worker/main.go - Background worker

**Core Logic (internal/):**
- internal/service/*.go (8 files) - Business logic
- internal/repo/*.go (5 files) - Data access

**Tests:**
- 7 test files (*_test.go) - Good coverage

Suggest reading: cmd/api/main.go first to understand the startup flow."`,
	},

	"bash": {
		Description: "Executes shell commands. Default timeout is 30 seconds. Can run commands in background.",
		WhenToUse: `Use when you need to:
- Run build commands (go build, npm install)
- Execute tests (go test, pytest)
- Run git commands
- Check system state (env, which, etc.)`,
		HowToRespond: `After running a command, ALWAYS:
1. Summarize what the command did
2. Explain the output (don't just show it)
3. Highlight errors or warnings
4. Suggest next steps based on results`,
		CommonMistakes: `DON'T:
- Run dangerous commands (rm -rf, etc.)
- Just show raw output without explanation
- Forget to explain what the command does
- Run long-running commands without background mode`,
		Examples: `GOOD: "Ran 'go test ./...' - here's the summary:

**Results:** 45 passed, 2 failed, 3.2s total

**Failed Tests:**
1. TestUserCreate (user_test.go:34)
   - Missing 'email' field in test fixture

2. TestDBConnection (db_test.go:12)
   - Database connection timeout

**Fix suggestions:**
- Add email to test fixture at line 30
- Ensure test DB is running: docker-compose up -d"`,
	},

	"write": {
		Description: "Creates or overwrites files with new content.",
		WhenToUse: `Use when you need to:
- Create new files
- Replace entire file contents
- Create configuration files`,
		HowToRespond: `After writing a file, ALWAYS:
1. Confirm what file was created/updated
2. Summarize what the file contains
3. Explain any important sections
4. Suggest verification steps`,
		CommonMistakes: `DON'T:
- Overwrite files without reading first
- Create files without explaining what's in them
- Forget to suggest how to verify the change`,
		Examples: `GOOD: "Created config.yaml with:
- Database settings (lines 1-10)
- API configuration (lines 12-25)
- Logging setup (lines 27-35)

To verify: Run 'go run cmd/api/main.go' and check logs."`,
	},

	"edit": {
		Description: "Performs search and replace operations in files.",
		WhenToUse: `Use when you need to:
- Modify specific parts of a file
- Fix bugs in code
- Update function signatures
- Refactor code`,
		HowToRespond: `After editing, ALWAYS:
1. Explain what was changed
2. Show the before/after (briefly)
3. Explain why the change was made
4. Suggest verification steps`,
		CommonMistakes: `DON'T:
- Edit without reading the file first
- Make changes without explaining them
- Say "fixed" without showing what changed`,
		Examples: `GOOD: "Updated handler.go line 45-48:

**Before:** 'return user.Name' (nil pointer risk)
**After:** Added nil check before accessing Name

This prevents the panic when user is not authenticated.
To verify: Run 'go test ./internal/handler/...'."`,
	},

	"todo": {
		Description: "Tracks tasks and progress for multi-step operations. Mirrors Claude Code's TodoWrite: the user sees a live checklist with icons (○ pending, ◐ in_progress, ●  completed) that updates as you call the tool.",
		WhenToUse: `Create todos at the START of any multi-step task, BEFORE you begin executing. This is the Claude Code contract — the user wants to see the full plan up front, then watch steps check off as you go.

Use for:
- Tasks that require 3+ distinct steps
- Changes that span multiple files
- Any time the user asks for several items in one request
- Long-running implementation where progress visibility matters

Skip for: trivial one-shot tasks ("what does this function do?", "fix this typo"), pure Q&A, or when the whole task is a single tool call.`,
		HowToRespond: `Workflow:
1. **BEFORE execution**: call the tool once with the full list of todos (all status=pending). This gives the user the full plan up front.
2. Immediately mark the first task in_progress and start working on it.
3. **EXACTLY ONE task may be in_progress at a time.** Never mark multiple as in_progress simultaneously — the executor rejects that, and it confuses the user (which task is actually running?).
4. When a task completes, mark it completed AND mark the next task in_progress in the same tool call.
5. Every call replaces the whole list; re-send the full array with updated statuses, don't send deltas.
6. When everything is done, the list should be all completed — that's the signal the task is finished.

Field conventions:
- content: imperative task description ("Add retry logic to HTTP client")
- active_form: present-continuous version shown during execution ("Adding retry logic to HTTP client")
- status: "pending" | "in_progress" | "completed"`,
		CommonMistakes: `DON'T:
- Write todos retrospectively after you've already done the work — the user missed the preview
- Mark multiple tasks in_progress (rejected with an error; fix and resend)
- Create vague tasks like "fix stuff" — be specific
- Over-decompose one-shot tasks into a dozen trivial todos
- Forget to flip status when a task completes — the UI will keep spinning`,
		Examples: `GOOD (start of a multi-step refactor):
  call todo with 4 pending items, then immediately call again marking item 1 in_progress.
  After item 1 finishes: one tool call marking item 1 completed + item 2 in_progress.

BAD:
  Three items all marked in_progress at once → rejected.
  Calling todo AFTER writing all the code → user got no live preview.`,
	},

	"tree": {
		Description: "Displays directory structure in a tree format.",
		WhenToUse: `Use when you need to:
- Understand project layout
- Find where files are organized
- Explain project structure to user`,
		HowToRespond: `After showing tree, ALWAYS:
1. Explain the directory structure
2. Identify key directories
3. Point out patterns (cmd/, internal/, etc.)`,
		CommonMistakes: `DON'T:
- Just show the tree without explanation
- Show entire tree for large projects`,
		Examples: `GOOD: "Project follows standard Go layout:
- cmd/ - Application entry points
- internal/ - Private packages (can't be imported)
- pkg/ - Public packages (can be imported)
- config/ - Configuration files"`,
	},

	"diff": {
		Description: "Shows differences between files or versions.",
		WhenToUse: `Use when you need to:
- Compare two files
- Show what changed
- Review modifications`,
		HowToRespond: `After showing diff, ALWAYS:
1. Summarize the changes
2. Explain why they matter
3. Highlight important modifications`,
		CommonMistakes: `DON'T:
- Just show raw diff output
- Forget to explain significance of changes`,
		Examples: `GOOD: "Key changes between versions:
- Added error handling (+15 lines)
- Removed deprecated function (-8 lines)
- Updated import paths"`,
	},

	"memorize": {
		Description: "Stores durable project knowledge for future sessions and updates the project memory markdown file.",
		WhenToUse: `Use when you discover information that will likely matter again:
- canonical test/build commands
- project conventions and architectural constraints
- recurring pitfalls or gotchas
- stable facts about subsystem behavior`,
		HowToRespond: `After memorizing, ALWAYS explain:
1. What knowledge was saved
2. Why it matters for future work
3. Whether it is a fact, preference, convention, or pattern`,
		CommonMistakes: `DON'T:
- memorize temporary debugging output
- store one-off observations that only matter in this turn
- write vague keys like "important" or "note"
- use memorize for short-lived planning notes; use scratchpad for that`,
		Examples: `GOOD: "Memorized convention 'test_command': use 'go test ./internal/...' for fast validation.
This matters because the full suite is slower and unnecessary for most UI-only edits."`,
	},
}

// GetToolGuide returns the usage guide for a specific tool.
func GetToolGuide(toolName string) (ToolUsageGuide, bool) {
	guide, ok := ToolUsageGuides[toolName]
	return guide, ok
}

// GetToolResponseHint returns a brief hint about how to respond after using a tool.
func GetToolResponseHint(toolName string) string {
	hints := map[string]string{
		"read":     "[HINT: Explain what this file contains, key sections, and how it answers the user's question]",
		"grep":     "[HINT: Summarize matches, group by purpose, explain patterns found]",
		"glob":     "[HINT: Categorize files found, highlight important ones, suggest which to read]",
		"bash":     "[HINT: Summarize command output, explain results, highlight errors/warnings]",
		"write":    "[HINT: Confirm file created, explain contents, suggest verification steps]",
		"edit":     "[HINT: Explain what changed, show before/after, suggest testing]",
		"tree":     "[HINT: Explain directory structure, identify key directories]",
		"diff":     "[HINT: Summarize changes, explain their significance]",
		"memorize": "[HINT: Explain what knowledge was saved and why it will help future sessions]",
	}
	if hint, ok := hints[toolName]; ok {
		return hint
	}
	return "[HINT: Analyze this result and explain what it means to the user]"
}

// GetEmptyResultMessage returns an appropriate message when a tool returns empty results.
func GetEmptyResultMessage(toolName string) string {
	messages := map[string]string{
		"read": `The file is empty or could not be read.

**Possible reasons:**
- File has no content
- File encoding issue
- Permission problem

**Suggestions:**
- Check if file exists with 'bash ls -la <path>'
- Try reading a different file
- Check file permissions`,

		"grep": `No matches found for this pattern.

**Possible reasons:**
- Pattern doesn't exist in codebase
- Wrong regex syntax
- Files filtered out by glob pattern
- Content in gitignored files

**Suggestions:**
- Try a simpler pattern
- Expand search scope (remove glob filter)
- Check pattern syntax
- Search in different directories`,

		"glob": `No files match this pattern.

**Possible reasons:**
- Wrong file extension
- Directory doesn't exist
- Files are gitignored
- Pattern syntax error

**Suggestions:**
- Try '**/*' to see all files
- Check directory exists
- Use different extension
- Verify pattern syntax`,

		"bash": `Command produced no output.

**Possible reasons:**
- Command succeeded silently
- Output redirected elsewhere
- Command found nothing to report

**What this usually means:**
- For 'mkdir', 'cp', 'mv': Success (no news is good news)
- For 'grep', 'find': Nothing found
- For 'test' commands: Check exit code

**Suggestion:** Run with verbose flags (-v) for more details.`,
	}

	if msg, ok := messages[toolName]; ok {
		return msg
	}
	return "The operation completed but returned no results. This may be expected behavior depending on the context."
}

// ToolChainPatterns provides recommended patterns for common tasks.
var ToolChainPatterns = map[string]string{
	"explore_code": `To explore code:
1. glob - Find relevant files
2. read - Read key files
3. Analyze and summarize`,

	"find_usage": `To find where something is used:
1. grep - Search for pattern
2. read - Read context around matches
3. Explain usage patterns`,

	"understand_architecture": `To understand architecture:
1. glob - Find all source files
2. read - Read main.go and key files
3. tree - See directory structure
4. Summarize architecture`,

	"debug_error": `To debug an error:
1. read - Read file with error
2. grep - Find related code
3. read - Read dependencies
4. Explain root cause and fix`,

	"implement_feature": `To implement a feature:
1. glob + read - Understand existing code
2. Plan the changes
3. edit/write - Make changes
4. bash - Run tests
5. Summarize what was done`,

	"docker_debug": `To debug Docker/compose issues:
1. bash 'docker compose ps' - Check service status
2. bash 'docker compose logs <service>' - Read logs
3. read docker-compose.yml - Check configuration
4. Explain root cause and fix`,

	"docker_setup": `To set up a Docker-based project:
1. read Dockerfile and docker-compose.yml - Understand setup
2. bash 'docker compose up -d' - Start services
3. bash 'docker compose ps' - Verify all services healthy
4. Proceed with the task`,
}
