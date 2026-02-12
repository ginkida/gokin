![Gokin](https://minio.ginkida.dev/minion/github/gokin.jpg)

<p align="center">
  <a href="https://github.com/ginkida/gokin/releases"><img src="https://img.shields.io/github/v/release/ginkida/gokin" alt="Release"></a>
  <a href="https://github.com/ginkida/gokin/blob/main/LICENSE"><img src="https://img.shields.io/github/license/ginkida/gokin" alt="License"></a>
  <img src="https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go" alt="Go Version">
</p>

<p align="center">
  <img src="https://minio.ginkida.dev/minion/github/Gokin-cli.gif" alt="Gokin Demo" width="800">
</p>

<h3 align="center">AI-powered coding assistant for your terminal.<br>Multi-provider, multi-agent, fully open source.</h3>

<p align="center">
  <a href="#installation">Install</a> &middot;
  <a href="#quick-start">Quick Start</a> &middot;
  <a href="#providers">Providers</a> &middot;
  <a href="#features">Features</a> &middot;
  <a href="#configuration">Configuration</a> &middot;
  <a href="#contributing">Contributing</a>
</p>

---

## Why Gokin?

**Use any AI provider.** Gemini, Claude, DeepSeek, GLM, or local models via Ollama — switch freely, even mid-conversation.

**Pay what you want.** From $0/month with Ollama or Gemini free tier to premium Claude for complex reasoning. No mandatory subscription.

| Stack | Cost | Good for |
|-------|------|----------|
| Gokin + Ollama | Free | Privacy, offline work |
| Gokin + Gemini Flash | Free tier | Fast iterations, prototyping |
| Gokin + DeepSeek | ~$1/month | Daily coding, great value |
| Gokin + GLM-4 | ~$3/month | Bulk operations |
| Gokin + Claude | Pay-per-use | Complex reasoning |

**Stay in the terminal.** No context switching. Read, write, search, refactor, commit, deploy — all from one place.

**52 built-in tools.** File ops, git, search, code analysis, web, planning, memory — the AI uses them autonomously.

**Multi-agent system.** Specialized agents work in parallel: one explores the codebase while another runs tests while another writes code.

---

## Installation

### Quick install (recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/ginkida/gokin/main/install.sh | sh
```

### From source

```bash
git clone https://github.com/ginkida/gokin.git
cd gokin
go build -o gokin ./cmd/gokin
```

### Requirements

- **Go 1.25+** (for building from source)
- **One AI provider** — any of:
  - [Gemini API key](https://aistudio.google.com/apikey) (free tier available)
  - Anthropic, DeepSeek, or GLM API key
  - [Ollama](https://ollama.ai) installed locally (no API key needed)

---

## Quick Start

**1. Set up a provider:**

```bash
# Option A: Environment variable
export GEMINI_API_KEY="your-key"

# Option B: Interactive setup
gokin --setup

# Option C: Login command inside Gokin
> /login gemini <your-key>
```

**2. Launch in your project:**

```bash
cd /path/to/your/project
gokin
```

**3. Start talking:**

```
> Explain the architecture of this project
> Find all TODO comments and suggest fixes
> Create a REST endpoint for user registration with validation and tests
> Refactor the auth module to use JWT
```

---

## Providers

| Provider | Models | Auth | Notes |
|----------|--------|------|-------|
| **Gemini** | gemini-2.5-flash, gemini-2.5-pro | API key or OAuth | Free tier, streaming, native tools |
| **Anthropic** | claude-3.5-sonnet, claude-3.5-haiku | API key | Extended thinking support |
| **DeepSeek** | deepseek-chat, deepseek-reasoner | API key | Great value for coding |
| **GLM/Z.AI** | glm-4.7, glm-4-flash | API key | Budget-friendly |
| **Ollama** | Any local model | None | Fully offline, auto-detects models |

Switch anytime:

```bash
> /model gemini-2.5-flash     # fast iteration
> /model claude-3.5-sonnet    # complex reasoning
> /model llama3.2             # local, private
```

Automatic fallback — if one provider is down, Gokin tries the next configured one.

---

## Features

### Code understanding

Read, search, and navigate any codebase. Semantic search finds code by meaning, not just keywords.

```
> How does authentication work in this project?
> Find all functions that handle payments
> Show the dependency graph for the user module
```

### Code generation and refactoring

Create features, refactor existing code, fix bugs — with automatic reference updates.

```
> Add input validation to all API endpoints
> Extract the error handling into a reusable middleware
> Rename getUserData to fetchUserProfile across the entire codebase
```

### Debugging

Analyze errors, find root causes, suggest fixes with specific file and line references.

```
> The app panics on startup, here's the error: [paste]
> Find all places where errors aren't being handled
> Why does memory usage keep growing?
```

### Testing

Generate tests, analyze coverage, run and parse results.

```
> Write unit tests for the payment module
> Run tests and explain what's failing
> Increase coverage for the auth package
```

### Git workflow

Stage, commit, diff, blame, branch, and create PRs without leaving the conversation.

```
> Show changes since last commit
> /commit -m "feat: add user validation"
> /pr --title "Add user validation feature"
```

### Multi-agent coordination

Specialized agents run in parallel with shared memory:

| Agent | Role | Tools |
|-------|------|-------|
| **Explore** | Read-only codebase exploration | glob, grep, read, tree |
| **Bash** | Command execution | bash, read, glob |
| **General** | Full access for complex tasks | All 52 tools |
| **Plan** | Task decomposition and planning | Read-only + planning |
| **Guide** | Documentation and help | glob, grep, read, web |

Up to 5 concurrent agents with task dependencies, progress tracking, and checkpoint/restore.

### Planning system

Complex tasks are automatically decomposed into steps with approval:

```
> Migrate the database layer from SQLite to PostgreSQL
```

Gokin creates a plan, shows it for approval, then executes step by step. Supports beam search, MCTS, and A* algorithms. Undo/redo entire plans.

### Memory

Gokin remembers your project patterns and preferences across sessions:

```
> Remember that this project uses PostgreSQL 15 with pgx driver
> Recall our database conventions
```

Memories are stored locally and auto-injected into context when relevant.

### Safety and permissions

Three permission levels control what the AI can do:

| Level | Examples | Behavior |
|-------|----------|----------|
| **Low** (read-only) | read, glob, grep, tree | Auto-approved |
| **Medium** (write) | write, edit, git_add | Ask once, then auto-approve |
| **High** (system) | bash, delete, git_commit | Ask every time |

Plus: sandbox mode for bash, undo/redo for all file changes, diff preview before applying, audit logging.

---

## Commands

| Command | Description |
|---------|-------------|
| `/help` | Show help |
| `/model <name>` | Switch AI model |
| `/login <provider> <key>` | Set API key |
| `/logout` | Remove saved API key |
| `/oauth-login` | Login via Google OAuth |
| `/save [name]` | Save session |
| `/sessions` | List saved sessions |
| `/load <id>` | Restore session |
| `/clear` | Clear conversation |
| `/compact` | Compress context |
| `/plan` | Enter planning mode |
| `/commit [-m msg]` | Create git commit |
| `/pr [--title t]` | Create pull request |
| `/browse` | Interactive file browser |
| `/theme` | Switch UI theme |
| `/init` | Create GOKIN.md for project |
| `/doctor` | Diagnose environment |
| `/status` | Show provider status |

### Keyboard shortcuts

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Ctrl+C` | Interrupt / Exit |
| `Ctrl+P` | Command palette |
| `Ctrl+G` | Toggle select mode |
| `Option+C` | Copy last response (macOS) |
| `Up` / `Down` | Input history |
| `Tab` | Autocomplete |

---

## Tools

Gokin gives the AI access to **52 tools** across 10 categories:

<details>
<summary><strong>File operations</strong> — read, write, edit, copy, move, delete, mkdir, diff, batch</summary>

Atomic file operations with undo support. Reads 50+ file types including PDFs, images, and Jupyter notebooks.
</details>

<details>
<summary><strong>Search and navigation</strong> — glob, grep, list_dir, tree, semantic_search, code_graph, history_search</summary>

Pattern matching, regex search, semantic search with embeddings, and dependency graph traversal.
</details>

<details>
<summary><strong>Execution</strong> — bash, ssh, env, run_tests, kill_shell</summary>

Shell commands with timeout and background mode. Sandbox support for safety.
</details>

<details>
<summary><strong>Git</strong> — git_status, git_add, git_commit, git_log, git_blame, git_diff, git_branch, git_pr</summary>

Full git workflow including GitHub PR creation.
</details>

<details>
<summary><strong>Web</strong> — web_fetch, web_search</summary>

Fetch and parse web content, search for current information.
</details>

<details>
<summary><strong>Planning</strong> — enter/exit_plan_mode, update/get_plan_status, undo/redo_plan, task, task_output, task_stop, todo, coordinate</summary>

Task decomposition, parallel execution, progress tracking.
</details>

<details>
<summary><strong>Memory</strong> — memory, shared_memory, update_scratchpad, memorize, pin_context, ask_user</summary>

Persistent memory, inter-agent communication, context management.
</details>

<details>
<summary><strong>Code analysis</strong> — refactor, check_impact, verify_code, declarations</summary>

AST-based refactoring, impact analysis, code verification.
</details>

<details>
<summary><strong>Agent coordination</strong> — ask_agent, request_tool, notifications</summary>

Delegate tasks to specialized agents, request capabilities.
</details>

<details>
<summary><strong>Utility</strong> — batch, safety, tools_list</summary>

Batch operations, safety checks, tool discovery.
</details>

---

## Configuration

Config file: `~/.config/gokin/config.yaml`

### Minimal config

```yaml
api:
  gemini_key: ""               # or set GEMINI_API_KEY env var
  active_provider: "gemini"

model:
  name: "gemini-2.5-flash-preview"
```

### Full reference

<details>
<summary><strong>Click to expand full config reference</strong></summary>

```yaml
api:
  gemini_key: ""
  anthropic_key: ""
  deepseek_key: ""
  glm_key: ""
  active_provider: "gemini"    # gemini, anthropic, deepseek, glm, ollama
  ollama_base_url: ""          # default: http://localhost:11434

model:
  preset: "fast"               # fast, balanced, creative, coding, advanced, local
  name: "gemini-2.5-flash-preview"
  temperature: 1.0
  max_output_tokens: 8192
  enable_thinking: false       # Anthropic extended thinking
  thinking_budget: 0
  fallback_providers: []

tools:
  timeout: 2m
  bash:
    sandbox: true
    blocked_commands: []
  allowed_dirs: []
  formatters:
    ".py": "black"
    ".js": "prettier --write"

permission:
  enabled: true
  default_policy: "ask"        # allow, ask, deny
  rules:
    read: "allow"
    write: "ask"

plan:
  enabled: true
  require_approval: true
  auto_detect: true
  algorithm: "beam"            # beam, mcts, astar
  default_step_timeout: 5m
  delegate_steps: false

ui:
  stream_output: true
  markdown_rendering: true
  show_tool_calls: true
  show_token_usage: true
  theme: "dark"                # dark, macos
  mouse_mode: "enabled"
  native_notifications: false

context:
  warning_threshold: 0.8
  summarization_ratio: 0.5
  tool_result_max_chars: 100000
  auto_compact_threshold: 0.75

session:
  enabled: true
  save_interval: 2m
  auto_load: false

memory:
  enabled: true
  max_entries: 1000
  auto_inject: true

semantic:
  enabled: true
  model: "text-embedding-004"
  index_on_start: true
  top_k: 10

hooks:
  enabled: false
  hooks:
    - name: "pre-commit hook"
      type: "pre_tool"
      tool_name: "git_commit"
      command: "echo 'About to commit'"
      enabled: true

mcp:
  enabled: false
  servers:
    - name: "github"
      transport: "stdio"
      command: "npx"
      args: ["-y", "@modelcontextprotocol/server-github"]
      env:
        GITHUB_TOKEN: "${GITHUB_TOKEN}"
      auto_connect: true
      timeout: 30s

update:
  enabled: true
  auto_check: true
  check_interval: 24h
  channel: "stable"            # stable, beta, nightly

logging:
  level: "info"

rate_limit:
  enabled: false
  requests_per_minute: 60

cache:
  enabled: true
  ttl: 1h

diff_preview:
  enabled: true
```

</details>

### Environment variables

| Variable | Description |
|----------|-------------|
| `GEMINI_API_KEY` | Gemini API key |
| `ANTHROPIC_API_KEY` | Anthropic Claude API key |
| `DEEPSEEK_API_KEY` | DeepSeek API key |
| `GLM_API_KEY` | GLM/Z.AI API key |
| `OLLAMA_HOST` | Ollama server URL |
| `GOKIN_MODEL` | Override model name |
| `GOKIN_BACKEND` | Override provider |
| `GOKIN_CONFIG` | Custom config path |
| `GOKIN_THEME` | Override UI theme |

### File locations

| Path | Contents |
|------|----------|
| `~/.config/gokin/config.yaml` | Configuration |
| `~/.local/share/gokin/sessions/` | Saved sessions |
| `~/.local/share/gokin/memory/` | Memory data |
| `~/.config/gokin/semantic_cache/` | Semantic search index |
| `~/.config/gokin/gokin.log` | Log file (when enabled) |

---

## MCP (Model Context Protocol)

Connect external tool servers that appear as native Gokin tools:

```yaml
mcp:
  enabled: true
  servers:
    - name: github
      transport: stdio
      command: npx
      args: ["-y", "@modelcontextprotocol/server-github"]
      env:
        GITHUB_PERSONAL_ACCESS_TOKEN: "${GITHUB_TOKEN}"
      auto_connect: true
```

Popular servers: [GitHub](https://github.com/modelcontextprotocol/servers), Filesystem, Brave Search, Puppeteer, Slack.

Auto-healing reconnects failed servers automatically.

---

## GOKIN.md

Create project-specific instructions that the AI reads on startup:

```bash
> /init
```

Use it for project context, code conventions, build commands, architecture decisions, and team workflows. Similar to `.cursorrules` or `CLAUDE.md`.

---

## Offline usage

Gokin works fully offline with Ollama:

```bash
# 1. Install Ollama
curl -fsSL https://ollama.ai/install.sh | sh

# 2. Pull a model
ollama pull llama3.2

# 3. Run Gokin
gokin --model llama3.2
```

No API calls, no data leaves your machine. Gokin auto-detects Ollama models and provides tool-call fallback for models that don't natively support function calling.

---

## Security

- **Sandbox mode** — bash commands run in a restricted environment with blocked dangerous commands
- **3-level permissions** — read-only auto-approved, writes ask once, system ops ask every time
- **Secret redaction** — API keys and passwords are automatically redacted in tool output
- **Environment isolation** — API keys excluded from subprocesses
- **Audit logging** — optional audit trail with configurable retention

---

## FAQ

<details>
<summary><strong>Is Gokin free?</strong></summary>

Yes. Gokin is open source (MIT). You only pay for AI provider costs — and there are free options (Ollama, Gemini free tier).
</details>

<details>
<summary><strong>Is my code safe?</strong></summary>

Gokin runs locally. Code only leaves your machine via API calls to your chosen provider, and only the context you provide. With Ollama, everything stays 100% local.
</details>

<details>
<summary><strong>What languages are supported?</strong></summary>

All of them. Gokin works with any text-based file. Best optimized for Go, JavaScript/TypeScript, and Python.
</details>

<details>
<summary><strong>How does this compare to Claude Code / Cursor?</strong></summary>

Gokin is terminal-native and multi-provider. You choose (and switch) your AI. You can use free/cheap models for daily work and premium models for complex tasks. No mandatory subscription.
</details>

<details>
<summary><strong>How much context can it handle?</strong></summary>

Up to 1M tokens depending on provider. Gokin uses smart compression and semantic search to stay within limits.
</details>

---

## Contributing

We welcome contributions! See [CONTRIBUTING.md](CONTRIBUTING.md) for details.

```bash
git clone https://github.com/ginkida/gokin.git
cd gokin
go build ./...
go test -race ./...
```

---

## License

MIT License — see [LICENSE](LICENSE) for details.

---

<p align="center"><b>Made with ❤️ by the Gokin community</b></p>
