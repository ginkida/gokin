package context

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxSystemPromptChars       = 28000
	maxPlanExecutionPromptChar = 20000
	maxSubAgentPromptChars     = 12000
)

// planModeBanner is the Claude Code-style "you are in plan mode" header.
// Injected at the top of the system prompt when the user has planning mode
// active (default on, toggled via Shift+Tab or /plan). Pairs with the
// tool-schema filter that strips write/exec tools — the banner explains
// the "why" so the model doesn't thrash against missing tool errors.
const planModeBanner = `## PLAN MODE — READ-ONLY EXPLORATION

You are currently in PLAN MODE. Your job is to EXPLORE the codebase and PROPOSE a plan for the user to approve. You CANNOT modify anything yet:
- No file writes, edits, or deletions
- No shell commands (bash, run_tests, git_add/commit/pr)
- No sub-agent spawning or task delegation
- Your available tools are filtered to read-only: read, glob, grep, list_dir, tree, diff, git_status/diff/log/blame, web_search, web_fetch, memory, ask_user.

Workflow:
1. Investigate the relevant files and context enough to understand the task.
2. When you have a concrete plan, call the ` + "`enter_plan_mode`" + ` tool with a clear title, short description, and ordered steps (title + what each step will do, plus verify commands where useful).
3. The user will approve, reject, or request modifications. On approval, plan mode auto-exits and you regain write/exec tools — then carry out the plan.

Keep exploration narrow and targeted. Do NOT read dozens of unrelated files. Prefer grep + targeted read over broad directory scans. If the task is trivial, propose a one-step plan and move on. If critical info is missing, ask via ` + "`ask_user`" + ` rather than guessing.`

// baseSystemPrompt is the foundation for all prompts.
const baseSystemPrompt = `You are Gokin, an AI coding assistant. You help users work with code through tools for reading, writing, searching, and executing commands.

## Code Project Operating Protocol

Act as a senior coding agent embedded in the user's repository. When the user asks for a code change, do not stop at a proposal: investigate, implement, verify, and report the result unless blocked by missing information or permissions.

Use repository evidence as the source of truth:
- Start by identifying the smallest relevant slice of the project: entry points, interfaces, tests, configuration, and existing conventions for the requested area.
- Before editing, read the files that define the current behavior and at least one nearby caller/test when they exist.
- Prefer existing patterns, helpers, dependency style, error handling, naming, and test conventions over inventing a new abstraction.
- Keep changes scoped to the request. Do not refactor unrelated code, rename public APIs, or change generated/vendor files unless the task explicitly requires it.
- If tool output disproves your assumption, revise the approach immediately; never keep coding from stale assumptions.

Implementation standard:
- Make the smallest coherent change that solves the user's task end to end.
- Preserve user work in the git tree. Check diff/status before commits and never discard unrelated changes.
- Update tests, fixtures, docs, or configuration when the behavior contract changes.
- After code edits, run the narrowest reliable verification first (targeted test/build/typecheck), then broaden only when risk or failures justify it.
- If verification cannot run, say exactly why and what command should be run next.

## Tool Selection (ALWAYS prefer dedicated tools over bash)

- Find files → glob (NOT bash find/ls)
- Search content → grep (NOT bash grep/rg)
- Read file → read (NOT bash cat/head/tail)
- Targeted edit → edit (NOT write entire file)
- New file → write
- Run commands/builds/tests → bash (only when no dedicated tool exists)

When multiple independent operations are needed, call tools in parallel.

## After Using Tools

1. Explain what you found — specific files, lines, patterns
2. Analyze meaning — why it matters for the user's task
3. Suggest next steps — concrete actions

Provide analysis, not raw output. Use file:line references for code locations.

## Security & Git

- Confirm before destructive commands (rm -rf, drop database, force push)
- Read files before editing; prefer edit over write for existing files
- Check git_status/git_diff before committing; stage specific files
- Write commit messages explaining WHY; protect secrets (.env, API keys)

## Response Style

- Be concise but thorough — explain what matters, skip what doesn't
- For multi-step tasks, use todo to track progress
- For completed code changes, name changed files/components and verification performed
- Handle errors gracefully and suggest fixes`

// legacyPlanInstructions is used when auto-detect planning is disabled.
const legacyPlanInstructions = `
Plan Mode:
- When a user provides feedback on a plan (after pressing ESC or requesting changes), you MUST:
  1. First call get_plan_status to check if there's a previously rejected plan
  2. If there is, review the rejected plan carefully
  3. Address the user's specific feedback
  4. Create a NEW plan using enter_plan_mode that incorporates their feedback
  5. Do NOT ignore the previous plan - build upon it and make the requested changes
`

// autoPlanningProtocol is the comprehensive planning protocol used when auto-detect is enabled.
const autoPlanningProtocol = `
═══════════════════════════════════════════════════════════════════════
                    AUTOMATIC PLANNING PROTOCOL
═══════════════════════════════════════════════════════════════════════

You MUST automatically enter plan mode when the user's request meets ALL of these criteria:
- The task involves creating or modifying code (not just answering questions)
- The task spans 3+ files OR requires architectural decisions
- Getting it wrong would require significant undo

You MUST NOT enter plan mode for:
- Simple questions about code — answer directly
- Single-file reads, searches, or explanations
- Running a single command
- Small edits (< 3 files) where the change is obvious
- When the user just wants a quick answer

IMPORTANT: When in doubt, prefer answering directly over entering plan mode.
If you can answer without tools or with 1-2 tool calls, do so immediately.

PLANNING WORKFLOW (when plan mode IS needed):

PHASE 1: TARGETED EXPLORATION (only what's needed)
1. Use glob/grep/read ONLY for files directly relevant to the task
2. Do NOT explore the entire project structure for every task
3. If you already know enough to plan, skip exploration and go to Phase 2

PHASE 2: PLAN CREATION
Call enter_plan_mode with:
- Clear, specific title
- Description explaining approach and WHY
- Concrete steps mapping to specific file changes
- Steps ordered by dependency
- Include per-step verify_commands that the orchestrator can run deterministically
- Include per-step expected_artifact_paths when concrete files/paths are known

PHASE 3: WAIT FOR APPROVAL
Tool blocks until user approves, rejects, or requests modifications.

PHASE 4: EXECUTION
After plan approval, the orchestrator will execute each step automatically.
- You will receive one step at a time — execute ONLY the requested step
- Do NOT call update_plan_progress or exit_plan_mode — the orchestrator handles this
- Provide a brief summary of what was done after each step

PLAN FEEDBACK HANDLING:
When a user provides feedback on a plan (after pressing ESC or requesting changes), you MUST:
1. Call get_plan_status to check the rejected plan
2. Review the rejected plan carefully
3. Address the user's specific feedback point by point
4. Create a NEW plan using enter_plan_mode incorporating feedback
5. Explain what changed from the previous plan
Do NOT ignore the previous plan - build upon it and make the requested changes.

STEP QUALITY:
- Specific: "Add validateEmail function to internal/auth/validator.go" not "add validation"
- Atomic: One logical change per step
- Verifiable: Include HOW to verify the step worked
- Contract-complete: Include verify_commands and expected_artifact_paths for each step
- Ordered: Dependencies before dependents

CONTRACT-FIRST PLANNING:
For tasks with clear I/O (functions, APIs, endpoints): fill contract fields in enter_plan_mode.
For exploratory/refactoring tasks: skip contract fields.

═══════════════════════════════════════════════════════════════════════
`

// projectGuidelines contains project-type-specific guidelines.
var projectGuidelines = map[ProjectType]string{
	ProjectTypeGo: `
## Go Project Guidelines
- Use 'go mod tidy' after adding or removing dependencies
- Run 'go test ./...' to run all tests
- Use 'go vet ./...' for static analysis
- Follow Go naming conventions (camelCase for private, PascalCase for exported)
- Run 'go fmt' to format code
- Prefer 'go build ./...' to verify compilation
- Use meaningful error messages with context`,

	ProjectTypeNode: `
## Node.js Project Guidelines
- Check package.json scripts before running commands
- Use the detected package manager (%s)
- Run '%s install' to install dependencies
- Run '%s test' for testing
- Check for .nvmrc or .node-version for Node version
- Prefer ES modules if using "type": "module"`,

	ProjectTypeRust: `
## Rust Project Guidelines
- Use 'cargo build' to compile
- Use 'cargo test' to run tests
- Use 'cargo fmt' to format code
- Use 'cargo clippy' for linting
- Check Cargo.toml for dependencies and features
- Prefer Result<T, E> for error handling`,

	ProjectTypePython: `
## Python Project Guidelines
- Use the detected package manager (%s)
- Check for virtual environment (.venv, venv)
- Use 'pytest' or detected test framework for testing
- Check pyproject.toml or setup.py for project config
- Follow PEP 8 style guidelines
- Use type hints where appropriate`,

	ProjectTypeJava: `
## Java Project Guidelines
- Use the detected build tool (Maven/Gradle)
- Check pom.xml or build.gradle for configuration
- Run tests with the build tool
- Follow Java naming conventions`,

	ProjectTypeRuby: `
## Ruby Project Guidelines
- Use 'bundle install' to install dependencies
- Check Gemfile for dependencies
- Use 'rake' or 'rspec' for testing
- Follow Ruby style guidelines`,

	ProjectTypePHP: `
## PHP Project Guidelines
- Use 'composer install' for dependencies
- Check composer.json for configuration
- Use PHPUnit for testing
- Follow PSR coding standards`,
}

// dockerGuidelines is injected when Docker/docker-compose is detected.
// This is separate from projectGuidelines because Docker is an overlay on any project type.
const dockerGuidelines = `
## Docker Guidelines
- The project has Docker configuration — use it for running services and commands
- Check container status with 'docker ps' before running dependent commands
- Use 'docker compose logs <service>' to debug service issues
- Pin base image versions in Dockerfile (avoid :latest)
- Prefer COPY over ADD for local files in Dockerfile`

const dockerComposeGuidelines = `
## Docker Compose Guidelines
- Start services: 'docker compose -f %s up -d'
- Stop services: 'docker compose -f %s down'
- View logs: 'docker compose -f %s logs -f <service>'
- Restart a service: 'docker compose -f %s restart <service>'
- Rebuild after Dockerfile changes: 'docker compose -f %s up -d --build'
- Run a command in a service: 'docker compose -f %s exec <service> <command>'
- Available services: %s
- Before running tests or builds that need services (DB, Redis, etc.), ensure they are up with 'docker compose -f %s ps'
- If a command fails because a service is not running, start it first with docker compose`

// MemoryProvider provides memory entries for prompt injection.
type MemoryProvider interface {
	GetForContext(projectOnly bool) string
}

// PlanStepInfo holds step information for plan execution prompts.
type PlanStepInfo struct {
	ID          int
	Title       string
	Description string
}

// PlanManagerProvider provides active contract context from plan manager.
type PlanManagerProvider interface {
	GetActiveContractContext() string
}

// ProjectLearningProvider exposes durable project learnings for prompt injection.
type ProjectLearningProvider interface {
	FormatForPrompt() string
	HasContent() bool
}

// PromptBuilder builds dynamic system prompts.
type PromptBuilder struct {
	workDir         string
	projectInfo     *ProjectInfo
	projectMemory   *ProjectMemory
	projectLearning ProjectLearningProvider
	memoryStore     MemoryProvider
	sessionMemory   *SessionMemoryManager
	workingMemory   WorkingMemoryProvider
	planAutoDetect  bool
	planManager     PlanManagerProvider
	detectedContext string // Auto-detected project context (frameworks, docs, etc.)
	toolHints       string // Tool usage pattern hints
	lastMessage     string // Last user message for conditional prompt injection
	provider        string // Active provider family ("kimi", "glm", ...) for addenda
	pinnedContent   string // User-pinned focus block, injected near the end of the prompt
	planMode        bool   // Claude Code-style plan mode: read-only phase until plan approved

	// Prompt caching: avoids rebuilding when inputs haven't changed
	cachedPrompt string
	promptDirty  bool
}

// NewPromptBuilder creates a new prompt builder.
func NewPromptBuilder(workDir string, projectInfo *ProjectInfo) *PromptBuilder {
	return &PromptBuilder{
		workDir:     workDir,
		projectInfo: projectInfo,
		promptDirty: true,
	}
}

// Invalidate marks the cached prompt as stale so Build() regenerates it.
func (b *PromptBuilder) Invalidate() {
	b.promptDirty = true
}

// SetProjectMemory sets the project memory for custom instructions.
func (b *PromptBuilder) SetProjectMemory(memory *ProjectMemory) {
	b.projectMemory = memory
	b.promptDirty = true
}

// SetProjectLearning sets durable project learning for prompt injection.
func (b *PromptBuilder) SetProjectLearning(learning ProjectLearningProvider) {
	b.projectLearning = learning
	b.promptDirty = true
}

// SetMemoryStore sets the memory store for persistent memory injection.
func (b *PromptBuilder) SetMemoryStore(store MemoryProvider) {
	b.memoryStore = store
	b.promptDirty = true
}

// SetSessionMemory sets the session memory manager for automatic context injection.
func (b *PromptBuilder) SetSessionMemory(sm *SessionMemoryManager) {
	b.sessionMemory = sm
	b.promptDirty = true
}

// SetWorkingMemory sets the compact working memory manager for prompt injection.
func (b *PromptBuilder) SetWorkingMemory(wm WorkingMemoryProvider) {
	b.workingMemory = wm
	b.promptDirty = true
}

// SetPlanAutoDetect enables or disables auto-planning in the system prompt.
func (b *PromptBuilder) SetPlanAutoDetect(enabled bool) {
	if b.planAutoDetect != enabled {
		b.planAutoDetect = enabled
		b.promptDirty = true
	}
}

// SetPlanManager sets the plan manager for contract context injection.
func (b *PromptBuilder) SetPlanManager(pm PlanManagerProvider) {
	b.planManager = pm
	b.promptDirty = true
}

// SetDetectedContext sets the auto-detected project context (frameworks, docs summaries).
func (b *PromptBuilder) SetDetectedContext(ctx string) {
	if b.detectedContext != ctx {
		b.detectedContext = ctx
		b.promptDirty = true
	}
}

// SetToolHints sets the tool usage pattern hints for periodic injection.
func (b *PromptBuilder) SetToolHints(hints string) {
	if b.toolHints != hints {
		b.toolHints = hints
		b.promptDirty = true
	}
}

// SetLastMessage sets the last user message for conditional prompt injection.
// Used to skip planning protocol for simple question-only messages.
func (b *PromptBuilder) SetLastMessage(msg string) {
	if b.lastMessage != msg {
		b.lastMessage = msg
		b.promptDirty = true
	}
}

// SetProvider records the active provider family ("kimi", "glm", "minimax",
// "deepseek", "ollama"). Used to inject a provider-specific addendum with
// behavioural guidance in Build(). Empty string means no addendum.
func (b *PromptBuilder) SetProvider(provider string) {
	provider = strings.TrimSpace(strings.ToLower(provider))
	if b.provider != provider {
		b.provider = provider
		b.promptDirty = true
	}
}

// SetPinnedContent stores content that the `pin_context` tool pinned
// for the rest of the session. Rendered in Build() as a dedicated
// "Pinned Focus" block near the end — high attention weight for the
// model. Empty string clears the pin. Setter is idempotent so the pin
// tool can re-apply on reload without thrashing the prompt cache.
func (b *PromptBuilder) SetPinnedContent(content string) {
	if b.pinnedContent != content {
		b.pinnedContent = content
		b.promptDirty = true
	}
}

// SetPlanMode toggles the Claude Code-style plan-mode system-prompt block.
// When true, Build() prepends a high-priority instruction telling the model
// it can only explore — no write/edit/bash until it presents a plan via
// `enter_plan_mode` and the user approves. Complements the tool-schema
// filter applied at the client level so the model both sees only read
// tools AND understands why.
func (b *PromptBuilder) SetPlanMode(enabled bool) {
	if b.planMode != enabled {
		b.planMode = enabled
		b.promptDirty = true
	}
}

// Build constructs the full system prompt. Returns cached version if inputs
// haven't changed since the last call.
func (b *PromptBuilder) Build() string {
	if !b.promptDirty && b.cachedPrompt != "" {
		return b.cachedPrompt
	}

	var builder strings.Builder

	// Plan-mode banner (Claude Code style) comes FIRST so the model sees
	// the restriction before any tool list or general guidance. Without
	// this, models have been observed to try write/edit/bash even with
	// the restricted tool schema — they retry on the "tool not found"
	// error instead of switching to exploration. The explicit banner
	// reframes the task as "explore, then propose", which matches the
	// enter_plan_mode handoff.
	if b.planMode {
		builder.WriteString(planModeBanner)
		builder.WriteString("\n\n")
	}

	// Base prompt
	builder.WriteString(baseSystemPrompt)
	builder.WriteString("\n")

	// Add project-specific guidelines
	if b.projectInfo != nil && b.projectInfo.Type != ProjectTypeUnknown {
		builder.WriteString(b.buildProjectSection())
	}

	// Add project memory instructions (from project instruction files)
	if b.projectMemory != nil && b.projectMemory.HasInstructions() {
		builder.WriteString("\n\n## Project Instructions\n")
		builder.WriteString("The following instructions are specific to this project:\n\n")
		builder.WriteString(b.projectMemory.GetInstructions())
	}

	// Add durable project learning (facts, conventions, reliable commands).
	if b.projectLearning != nil && b.projectLearning.HasContent() {
		builder.WriteString("\n\n")
		builder.WriteString(strings.TrimSpace(b.projectLearning.FormatForPrompt()))
	}

	// Add persistent memories from memory store
	if b.memoryStore != nil {
		memoryContent := b.memoryStore.GetForContext(true) // Project-specific memories
		if memoryContent != "" {
			builder.WriteString("\n\n")
			builder.WriteString(memoryContent)
		}
	}

	// Add session memory (auto-extracted conversation summary)
	if b.sessionMemory != nil {
		if smContent := b.sessionMemory.GetContent(); smContent != "" {
			builder.WriteString("\n\n")
			builder.WriteString(smContent)
		}
	}
	if b.workingMemory != nil {
		if wmContent := b.workingMemory.GetContent(); wmContent != "" {
			builder.WriteString("\n\n")
			builder.WriteString(wmContent)
		}
	}

	// Add working directory
	builder.WriteString(fmt.Sprintf("\n\nThe user's working directory is: %s", b.workDir))

	// Add project context if available
	if b.projectInfo != nil && b.projectInfo.Name != "" {
		builder.WriteString(fmt.Sprintf("\nProject name: %s", b.projectInfo.Name))
	}

	// Smart context injection: skip heavy sections for simple questions.
	// Questions only need base prompt + project instructions + memory.
	// Action-oriented tasks get the full prompt with planning, tool hints, detected context.
	questionOnly := b.isQuestionOnly()

	// Inject auto-detected project context (skip for questions — saves ~2K chars)
	if b.detectedContext != "" && !questionOnly {
		builder.WriteString("\n\n## Detected Project Context\n")
		builder.WriteString(b.detectedContext)
	}

	// Inject tool usage pattern hints (skip for questions — saves ~1K chars)
	if b.toolHints != "" && !questionOnly {
		builder.WriteString("\n\n## Tool Usage Hints\n")
		builder.WriteString(b.toolHints)
	}

	// Add plan mode instructions (skip for questions — saves ~3.5K chars).
	if b.planAutoDetect && !questionOnly {
		builder.WriteString(autoPlanningProtocol)
	} else if !b.planAutoDetect && !questionOnly {
		builder.WriteString(legacyPlanInstructions)
	}

	// Inject active contract context from plan manager
	if b.planManager != nil {
		if ctx := b.planManager.GetActiveContractContext(); ctx != "" {
			builder.WriteString("\n\n=== ACTIVE CONTRACT ===\n")
			builder.WriteString(ctx)
			builder.WriteString("\n=== END CONTRACT ===\n")
			builder.WriteString("\nOperate strictly within this contract's boundaries.\n")
		}
	}

	// Tool chain patterns removed from system prompt to avoid
	// encouraging excessive exploration on simple tasks.

	// User-pinned focus content — rendered before the provider addendum
	// (which is always last). The pin represents the user's current
	// "hot memory": priorities, constraints, working files. Kept above
	// the addendum because provider rules are static scaffolding,
	// pinned content is dynamic per-session state.
	if strings.TrimSpace(b.pinnedContent) != "" {
		builder.WriteString("\n\n## Pinned Focus\n")
		builder.WriteString("The user pinned this information — honour it throughout the session until explicitly cleared:\n\n")
		builder.WriteString(strings.TrimSpace(b.pinnedContent))
		builder.WriteString("\n")
	}

	// Provider-specific behavioural addendum. Placed last so it has the
	// closest position to the user message and therefore the highest
	// model attention. Empty when no provider is set or the provider has
	// no addendum defined.
	if addendum := providerAddendum(b.provider); addendum != "" {
		builder.WriteString("\n\n")
		builder.WriteString(addendum)
	}

	result := applyPromptBudget(builder.String(), maxSystemPromptChars)
	b.cachedPrompt = result
	b.promptDirty = false
	return result
}

// providerAddendum returns provider-specific behavioural guidance for the
// system prompt. Empty for providers we haven't characterised. Keep each
// block under ~1K chars — any longer and the prompt cache churns.
//
// Kimi-for-coding is a strong coding model, but it benefits from explicit
// guardrails around bounded exploration, evidence tracking, and verification.
// The addendum encodes the constraints already enforced by code
// (read-before-edit invariant, tool budget, delta-check) so the model sees
// them before it plans the turn, not only as post-hoc error responses.
func providerAddendum(provider string) string {
	switch provider {
	case "kimi":
		return kimiOperatingRules
	case "deepseek":
		return deepseekOperatingRules
	}
	return ""
}

const kimiOperatingRules = `## Operating rules (Kimi-specific)

Plan-then-act: before the first tool call of a turn, write a single-line plan. Format: "Plan: <3-7 word objective>". Skip for trivial one-tool turns (e.g. a single read).

Project-map discipline:
- For code changes, build a small evidence ledger before editing: target files, relevant symbols, caller/test touchpoints, and the expected behavior change.
- Do not infer APIs from names alone. Confirm signatures, data shapes, config keys, and error contracts from source before writing code.
- In large repositories, search for the exact symbol/path first, then read the few files that own the behavior. Avoid scanning the whole tree unless the search result is ambiguous.
- If the repository has explicit instructions (GOKIN.md, CLAUDE.md, .gokin/rules, package scripts), treat them as higher-priority local law.

Read discipline:
- You MUST Read a file before Edit. The edit tool blocks edits on unread files (read-before-edit invariant). Don't edit from grep snippets.
- Do not Re-read a file already loaded this session unless you changed it. If unsure, consult Working Memory.
- Prefer: grep → targeted Read (with offset/limit). Avoid: glob → bulk Read of whole directory.
- Batch independent Reads in parallel in one turn. Never serialise Reads that don't depend on each other.

Edit discipline:
- In old_string, quote 3-5 lines of exact surrounding text to make the match unambiguous. Short 1-line old_string regularly produces wrong-location matches.
- After each Edit, the next tool response may contain delta-check errors (go build / typecheck output). If it does, fix those errors BEFORE any further Edit. Do not stack new edits on a broken tree.
- Mutations (Edit/Write/Delete) are sequential; never batch them in parallel.
- After the final mutation, inspect the diff before finalising. If the diff shows unrelated churn, correct it or explicitly flag why it is necessary.

Verification discipline:
- Run the narrowest meaningful verification for changed code: targeted tests, typecheck/build, lint, or the project-specific command surfaced in the prompt.
- If verification fails, fix the first actionable failure and rerun the same check before broadening scope.
- Do not claim success from intent. Only say "verified" when a command/tool actually ran and passed.

Answer discipline:
- After 3+ tool calls in a turn, pause and write ~3 lines of "Established / Unknown / Next" before deciding to continue or finalise.
- Keep the final answer concise: ≤4 short paragraphs. Cite changed files by path and mention verification. Don't paste full diffs — the runtime appends an evidence footer automatically.
- Tool budget is capped per turn. When you see "Tool budget guard" in a tool response, STOP calling tools and write the final answer from what you already know.`

// deepseekOperatingRules mirrors the Kimi addendum for DeepSeek V4.
// DeepSeek V4 reasoning is excellent on analytical tasks but shares two
// Kimi-class behaviours worth guarding against:
//   - Eagerness to re-read files it already loaded (wastes tool budget)
//   - Skipping the verification step after a code edit (delta-check then
//     resolves the gap in the NEXT turn — slows convergence)
//
// The rules are intentionally parallel to kimiOperatingRules so a user
// switching providers sees consistent behaviour. Only the intro line
// and thinking-budget note differ.
const deepseekOperatingRules = `## Operating rules (DeepSeek-specific)

Plan-then-act: before the first tool call of a turn, write a single-line plan. Format: "Plan: <3-7 word objective>". Skip for trivial one-tool turns (e.g. a single read).

Read discipline:
- You MUST Read a file before Edit. The edit tool blocks edits on unread files (read-before-edit invariant). Don't edit from grep snippets.
- Do not Re-read a file already loaded this session unless it changed.
- Batch independent Reads in parallel in one turn.

Edit discipline:
- In old_string, quote 3-5 lines of exact surrounding text for unique matching. Whitespace-tolerant fuzzy match handles minor indentation drift, but overlapping fuzzy candidates return a disambiguation error — use line_start/line_end mode when the error hints at it.
- After each Edit, the next tool response may carry delta-check errors (go build / typecheck / lint). Fix those BEFORE any further Edit. Do not stack new edits on a broken tree.

Verification discipline:
- Before the final answer: if the turn touched code, run the narrowest reliable verification (targeted go test for the touched package, pytest for the touched module, etc.). Broaden only on failure.
- Cite evidence in the final answer: which files changed, which verification commands succeeded. Don't claim "tests pass" unless a test command actually ran this turn.

Reasoning discipline:
- Extended thinking is enabled — use it to plan tool sequences, not to emit lengthy prose. Final answer should be short.
- After 3+ tool calls: pause and write ~3 lines of "Established / Unknown / Next" before deciding to continue or finalise. If Established already covers the user's request, STOP and write the final answer.

Answer discipline:
- Keep the final answer concise: ≤4 short paragraphs. Cite changed files by path and mention verification. Don't paste full diffs — the runtime appends an evidence footer automatically.
- Tool budget is capped per turn. When you see "Tool budget guard" in a tool response, STOP calling tools and write the final answer from what you already know.`

// buildProjectSection builds the project-specific section.
func (b *PromptBuilder) buildProjectSection() string {
	guidelines, ok := projectGuidelines[b.projectInfo.Type]
	if !ok {
		return ""
	}

	// Format guidelines with project-specific info
	switch b.projectInfo.Type {
	case ProjectTypeNode:
		pm := b.projectInfo.PackageManager
		if pm == "" {
			pm = "npm"
		}
		guidelines = fmt.Sprintf(guidelines, pm, pm, pm)
	case ProjectTypePython:
		pm := b.projectInfo.PackageManager
		if pm == "" {
			pm = "pip"
		}
		guidelines = fmt.Sprintf(guidelines, pm)
	}

	var builder strings.Builder
	builder.WriteString(guidelines)

	// Add detected details
	if len(b.projectInfo.Dependencies) > 0 {
		builder.WriteString(fmt.Sprintf("\n\nKey dependencies: %s", strings.Join(b.projectInfo.Dependencies, ", ")))
	}

	if b.projectInfo.TestFramework != "" {
		builder.WriteString(fmt.Sprintf("\nTest command: %s", b.projectInfo.TestFramework))
	}

	if b.projectInfo.BuildTool != "" {
		builder.WriteString(fmt.Sprintf("\nBuild tool: %s", b.projectInfo.BuildTool))
	}

	// Append Docker context if detected
	if b.projectInfo.HasDocker {
		builder.WriteString("\n")
		builder.WriteString(dockerGuidelines)
		if b.projectInfo.DockerBaseImage != "" {
			builder.WriteString(fmt.Sprintf("\nBase image: %s", b.projectInfo.DockerBaseImage))
		}
	}

	if b.projectInfo.HasDockerCompose && b.projectInfo.ComposeFile != "" {
		cf := b.projectInfo.ComposeFile
		services := strings.Join(b.projectInfo.DockerServices, ", ")
		if services == "" {
			services = "(check compose file)"
		}
		builder.WriteString("\n")
		builder.WriteString(fmt.Sprintf(dockerComposeGuidelines, cf, cf, cf, cf, cf, cf, services, cf))
	}

	return builder.String()
}

// BuildWithContext builds a prompt with additional context.
func (b *PromptBuilder) BuildWithContext(additionalContext string) string {
	base := b.Build()
	if additionalContext == "" {
		return base
	}

	return applyPromptBudget(base+"\n\nAdditional context:\n"+additionalContext, maxSystemPromptChars)
}

// GetProjectSummary returns a brief summary of the detected project.
func (b *PromptBuilder) GetProjectSummary() string {
	if b.projectInfo == nil || b.projectInfo.Type == ProjectTypeUnknown {
		return ""
	}

	parts := []string{b.projectInfo.Type.String()}

	if b.projectInfo.Name != "" {
		parts = append(parts, fmt.Sprintf("(%s)", b.projectInfo.Name))
	}

	if b.projectInfo.PackageManager != "" {
		parts = append(parts, fmt.Sprintf("[%s]", b.projectInfo.PackageManager))
	}

	return strings.Join(parts, " ")
}

// BuildPlanExecutionPrompt constructs a minimal focused prompt for executing an approved plan.
// This prompt is used after context is cleared, providing only what's needed for plan execution.
func (b *PromptBuilder) BuildPlanExecutionPrompt(title, description string, steps []PlanStepInfo) string {
	var builder strings.Builder

	builder.WriteString("You are Gokin, executing an approved plan. Execute precisely.\n\n")

	// Add project-specific guidelines (reuse existing method)
	if b.projectInfo != nil && b.projectInfo.Type != ProjectTypeUnknown {
		builder.WriteString(b.buildProjectSection())
		builder.WriteString("\n\n")
	}

	// Add project instructions (from project instruction files)
	if b.projectMemory != nil && b.projectMemory.HasInstructions() {
		builder.WriteString("## Project Instructions\n")
		builder.WriteString(b.projectMemory.GetInstructions())
		builder.WriteString("\n\n")
	}

	if b.projectLearning != nil && b.projectLearning.HasContent() {
		builder.WriteString(strings.TrimSpace(b.projectLearning.FormatForPrompt()))
		builder.WriteString("\n\n")
	}

	// Add persistent memories from memory store (critical for context retention)
	if b.memoryStore != nil {
		memoryContent := b.memoryStore.GetForContext(true)
		if memoryContent != "" {
			builder.WriteString("## Project Knowledge\n")
			builder.WriteString(memoryContent)
			builder.WriteString("\n\n")
		}
	}

	// Add session memory for plan execution context
	if b.sessionMemory != nil {
		if smContent := b.sessionMemory.GetContent(); smContent != "" {
			builder.WriteString(smContent)
			builder.WriteString("\n")
		}
	}
	if b.workingMemory != nil {
		if wmContent := b.workingMemory.GetContent(); wmContent != "" {
			builder.WriteString(wmContent)
			builder.WriteString("\n")
		}
	}

	// Inject active contract context from plan manager
	if b.planManager != nil {
		if ctx := b.planManager.GetActiveContractContext(); ctx != "" {
			builder.WriteString("## Active Contract\n")
			builder.WriteString(ctx)
			builder.WriteString("\n\n")
		}
	}

	// The approved plan
	builder.WriteString("═══════════════════════════════════════════════════════════════════════\n")
	builder.WriteString("                         APPROVED PLAN\n")
	builder.WriteString("═══════════════════════════════════════════════════════════════════════\n\n")
	builder.WriteString(fmt.Sprintf("## %s\n", title))
	if description != "" {
		builder.WriteString(fmt.Sprintf("%s\n", description))
	}
	builder.WriteString("\n### Steps:\n")
	for _, step := range steps {
		builder.WriteString(fmt.Sprintf("%d. **%s**\n", step.ID, step.Title))
		if step.Description != "" {
			builder.WriteString(fmt.Sprintf("   %s\n", step.Description))
		}
	}

	// Execution rules
	builder.WriteString("\n═══════════════════════════════════════════════════════════════════════\n")
	builder.WriteString("                       EXECUTION RULES\n")
	builder.WriteString("═══════════════════════════════════════════════════════════════════════\n\n")
	builder.WriteString("1. Execute ONLY the current step described in the user message\n")
	builder.WriteString("2. Always READ files before editing them\n")
	builder.WriteString("3. Do NOT deviate from the plan — execute exactly what was approved\n")
	builder.WriteString("4. Do NOT call update_plan_progress or exit_plan_mode — the orchestrator handles this automatically\n")
	builder.WriteString("5. verify_commands are mandatory and must pass before a step is completed\n")
	builder.WriteString("6. Provide a brief summary of what was done at the end\n")
	builder.WriteString("7. Report any issues or deviations from the plan\n")

	// Working directory
	builder.WriteString(fmt.Sprintf("\nWorking directory: %s\n", b.workDir))

	return applyPromptBudget(builder.String(), maxPlanExecutionPromptChar)
}

// BuildPlanExecutionPromptWithContext is like BuildPlanExecutionPrompt but includes
// a context snapshot from the planning conversation to preserve important decisions.
func (b *PromptBuilder) BuildPlanExecutionPromptWithContext(title, description string, steps []PlanStepInfo, contextSnapshot string) string {
	basePrompt := b.BuildPlanExecutionPrompt(title, description, steps)

	if contextSnapshot == "" {
		return basePrompt
	}

	// Insert context snapshot before the approved plan section
	var builder strings.Builder
	builder.WriteString("You are Gokin, executing an approved plan. Execute precisely.\n\n")

	// Context from planning discussion (important decisions, findings)
	builder.WriteString(contextSnapshot)
	builder.WriteString("\n")

	// The rest is from base prompt (skip the first line which we already wrote)
	lines := strings.SplitN(basePrompt, "\n", 2)
	if len(lines) > 1 {
		builder.WriteString(lines[1])
	}

	return applyPromptBudget(builder.String(), maxPlanExecutionPromptChar)
}

// BuildSubAgentPrompt builds a compact project context for injection into sub-agents.
// Unlike Build(), this omits examples, response format rules, and planning protocol.
// It provides project guidelines, project instructions, memory, and working directory.
func (b *PromptBuilder) BuildSubAgentPrompt() string {
	var builder strings.Builder

	// Project-specific guidelines (Go, Node, Python, etc.)
	if b.projectInfo != nil && b.projectInfo.Type != ProjectTypeUnknown {
		builder.WriteString(b.buildProjectSection())
		builder.WriteString("\n")
	}

	// Project instructions from project instruction files
	if b.projectMemory != nil && b.projectMemory.HasInstructions() {
		builder.WriteString("\n## Project Instructions\n")
		builder.WriteString(b.projectMemory.GetInstructions())
		builder.WriteString("\n")
	}

	if b.projectLearning != nil && b.projectLearning.HasContent() {
		builder.WriteString("\n")
		builder.WriteString(strings.TrimSpace(b.projectLearning.FormatForPrompt()))
		builder.WriteString("\n")
	}

	// Add persistent memories (project knowledge, decisions, etc.)
	if b.memoryStore != nil {
		memoryContent := b.memoryStore.GetForContext(true)
		if memoryContent != "" {
			builder.WriteString("\n## Project Knowledge\n")
			builder.WriteString(memoryContent)
			builder.WriteString("\n")
		}
	}

	// Add session memory for sub-agent context
	if b.sessionMemory != nil {
		if smContent := b.sessionMemory.GetContent(); smContent != "" {
			builder.WriteString("\n")
			builder.WriteString(smContent)
		}
	}
	if b.workingMemory != nil {
		if wmContent := b.workingMemory.GetContent(); wmContent != "" {
			builder.WriteString("\n")
			builder.WriteString(wmContent)
		}
	}

	// Inject active contract context
	if b.planManager != nil {
		if ctx := b.planManager.GetActiveContractContext(); ctx != "" {
			builder.WriteString("\n## Active Contract\n")
			builder.WriteString(ctx)
			builder.WriteString("\n")
		}
	}

	// Working directory
	builder.WriteString(fmt.Sprintf("\nWorking directory: %s\n", b.workDir))

	// Project name
	if b.projectInfo != nil && b.projectInfo.Name != "" {
		builder.WriteString(fmt.Sprintf("Project: %s\n", b.projectInfo.Name))
	}

	return applyPromptBudget(builder.String(), maxSubAgentPromptChars)
}

// isQuestionOnly returns true if the last user message appears to be a simple question
// with no action-oriented content. Used to skip planning protocol injection.
func (b *PromptBuilder) isQuestionOnly() bool {
	msg := strings.TrimSpace(b.lastMessage)
	if msg == "" {
		return false // Safe default: inject planning protocol
	}
	lower := strings.ToLower(msg)

	// Action patterns indicate the user wants changes — inject planning protocol
	actionPatterns := []string{
		"добавь", "создай", "измени", "удали", "исправь", "сделай", "реализуй", "напиши", "перепиши",
		"add", "create", "implement", "refactor", "fix", "migrate", "build", "write", "change",
		"update", "modify", "remove", "delete", "rename", "move", "install", "configure", "set up",
	}
	for _, p := range actionPatterns {
		if strings.Contains(lower, p) {
			return false
		}
	}

	// Question patterns indicate the user is asking, not doing
	questionPatterns := []string{
		"что", "где", "как", "почему", "зачем", "какой", "сколько", "когда", "покажи", "объясни",
		"what", "where", "how", "why", "which", "when", "show", "explain", "describe", "list",
		"does", "is ", "are ", "can ", "could ", "would ", "should ", "do ",
	}
	for _, p := range questionPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}

	// Ends with ? is a question
	if strings.HasSuffix(msg, "?") {
		return true
	}

	return false // Safe default: inject planning protocol
}

func applyPromptBudget(prompt string, maxChars int) string {
	prompt = strings.TrimSpace(prompt)
	if maxChars <= 0 || len(prompt) <= maxChars {
		return prompt
	}

	contractBlock := extractContractBlock(prompt)

	trimmed := prompt
	trimmed = removeHeadingSection(trimmed, "## Common Task Patterns")
	trimmed = truncateHeadingSection(trimmed, "## Tool Usage Hints", 1000)
	trimmed = truncateHeadingSection(trimmed, "## Detected Project Context", 1800)
	trimmed = truncateHeadingSection(trimmed, "## Project Instructions", 2200)
	trimmed = truncateHeadingSection(trimmed, "## Project Learning", 1800)
	trimmed = truncateHeadingSection(trimmed, "## Memory", 2800)
	trimmed = truncateHeadingSection(trimmed, "# Session Memory", 1800)
	trimmed = truncateHeadingSection(trimmed, "# Working Memory", 1400)
	trimmed = truncatePlanningProtocolSection(trimmed, 2600)
	trimmed = strings.TrimSpace(trimmed)

	if contractBlock != "" && !strings.Contains(trimmed, "=== ACTIVE CONTRACT ===") {
		trimmed += "\n\n" + contractBlock
	}

	if len(trimmed) <= maxChars {
		return trimmed
	}

	if contractBlock != "" {
		prefixBudget := maxChars - len(contractBlock) - len("\n\n")
		if prefixBudget > 200 {
			if len(trimmed) > prefixBudget {
				trimmed = strings.TrimSpace(truncateUTF8Safe(trimmed, prefixBudget)) + "\n\n[Prompt compacted due to budget]"
			}
			trimmed += "\n\n" + contractBlock
			if len(trimmed) <= maxChars {
				return trimmed
			}
		}
	}

	if maxChars > 40 {
		return strings.TrimSpace(truncateUTF8Safe(trimmed, maxChars-40)) + "\n\n[Prompt compacted due to budget]"
	}
	return strings.TrimSpace(truncateUTF8Safe(trimmed, maxChars))
}

// truncateUTF8Safe truncates a string to maxBytes without splitting multi-byte characters.
func truncateUTF8Safe(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

func extractContractBlock(prompt string) string {
	start := strings.Index(prompt, "=== ACTIVE CONTRACT ===")
	if start < 0 {
		return ""
	}
	endRel := strings.Index(prompt[start:], "=== END CONTRACT ===")
	if endRel < 0 {
		return ""
	}
	end := start + endRel + len("=== END CONTRACT ===")
	if tailRel := strings.Index(prompt[end:], "\nOperate strictly within this contract's boundaries."); tailRel >= 0 {
		end += tailRel + len("\nOperate strictly within this contract's boundaries.")
	}
	if end > len(prompt) {
		end = len(prompt)
	}
	return strings.TrimSpace(prompt[start:end])
}

func removeHeadingSection(prompt, heading string) string {
	start := strings.Index(prompt, heading)
	if start < 0 {
		return prompt
	}

	sectionEnd := findNextHeading(prompt, start+len(heading))
	if sectionEnd < 0 {
		return strings.TrimSpace(prompt[:start])
	}
	return strings.TrimSpace(prompt[:start] + prompt[sectionEnd:])
}

func truncateHeadingSection(prompt, heading string, maxSectionChars int) string {
	if maxSectionChars <= 0 {
		return removeHeadingSection(prompt, heading)
	}

	start := strings.Index(prompt, heading)
	if start < 0 {
		return prompt
	}
	sectionEnd := findNextHeading(prompt, start+len(heading))
	if sectionEnd < 0 {
		sectionEnd = len(prompt)
	}

	section := prompt[start:sectionEnd]
	if len(section) <= maxSectionChars {
		return prompt
	}

	truncated := strings.TrimSpace(section[:maxSectionChars]) + "\n[section compacted]"
	return strings.TrimSpace(prompt[:start] + truncated + prompt[sectionEnd:])
}

func truncatePlanningProtocolSection(prompt string, maxSectionChars int) string {
	start := strings.Index(prompt, "AUTOMATIC PLANNING PROTOCOL")
	if start < 0 {
		return prompt
	}

	sectionStart := strings.LastIndex(prompt[:start], "\n")
	if sectionStart < 0 {
		sectionStart = start
	}
	sectionEnd := strings.Index(prompt[start:], "## Common Task Patterns")
	if sectionEnd < 0 {
		sectionEnd = len(prompt)
	} else {
		sectionEnd = start + sectionEnd
	}

	section := prompt[sectionStart:sectionEnd]
	if len(section) <= maxSectionChars {
		return prompt
	}
	truncated := strings.TrimSpace(section[:maxSectionChars]) + "\n[planning protocol compacted]"
	return strings.TrimSpace(prompt[:sectionStart] + "\n" + truncated + prompt[sectionEnd:])
}

func findNextHeading(prompt string, from int) int {
	if from < 0 || from >= len(prompt) {
		return -1
	}

	candidates := []string{
		"\n# ",
		"\n## ",
		"\n=== ACTIVE CONTRACT ===",
		"\n═══════════════════════════════════════════════════════════════════════",
	}
	next := -1
	for _, marker := range candidates {
		idx := strings.Index(prompt[from:], marker)
		if idx < 0 {
			continue
		}
		pos := from + idx
		if next == -1 || pos < next {
			next = pos
		}
	}
	return next
}
