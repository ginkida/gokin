![Gokin](https://minio.ginkida.dev/minion/github/gokin.jpg)

<p align="center">
  <a href="https://github.com/ginkida/gokin/releases"><img src="https://img.shields.io/github/v/release/ginkida/gokin" alt="Release"></a>
  <a href="https://github.com/ginkida/gokin/blob/main/LICENSE"><img src="https://img.shields.io/github/license/ginkida/gokin" alt="License"></a>
  <img src="https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go" alt="Go Version">
</p>

<p align="center">
  <img src="https://minio.ginkida.dev/minion/github/Gokin-cli.gif" alt="Gokin Demo" width="800">
</p>

**Gokin** is an advanced AI-powered coding assistant for your terminal — a comprehensive multi-provider alternative to Claude Code.

- **Multi-provider** — Gemini (with OAuth support), DeepSeek, GLM/Z.AI, Anthropic Claude, Ollama (local models)
- **Multi-agent** — specialized agents (explore, bash, general, plan, claude-code-guide) with tree-based planning and coordination
- **54 tools** — complete toolkit for development, from file operations to semantic search
- **Advanced features** — semantic code search, MCP integration, session persistence, undo/redo, automatic updates
- **$1–3/month** or **free** with Ollama / Gemini free tier

---

## Why Gokin?

### Cost Comparison

| Tool | Cost | Best For |
|------|------|----------|
| Gokin + Ollama | Free (local) | Privacy-focused, offline development |
| Gokin + Gemini Flash 2.5 | Free tier available | Fast iterations, prototyping |
| Gokin + DeepSeek | ~$1/month | Coding tasks, great value |
| Gokin + GLM-4 | ~$3/month | Initial development, bulk operations |
| Gokin + Anthropic Claude | Pay-per-use | Advanced reasoning and complex tasks |
| Gokin + Ollama (local) | Free | Privacy, offline development |
| Claude Code | ~$100/month | Final polish, complex reasoning |
| Cursor | ~$20/month | IDE-integrated AI |
| Aider | API costs only | Git-focused editing |
### Recommended Workflow

```
Gokin (GLM-4 / Gemini Flash 2.5)   →     Claude Code (Claude Opus 4.5)
        ↓                                         ↓
   Write code from scratch              Polish and refine the code
   Bulk file operations                 Complex architectural decisions
   Repetitive tasks                     Code review and optimization
```

**Tip:** Use Gokin with local Ollama models for development, then switch to Anthropic Claude for final review.
   Repetitive tasks                     Code review and optimization
```

### Why Not Just Use Other Tools?

| Feature | Gokin | Claude Code | Cursor | Aider |
|---------|-------|-------------|--------|-------|
| Multi-provider | 5 providers + Ollama | Claude only | Multiple | Multiple |
| Terminal-native | Yes | Yes | IDE | Terminal |
| Local models | Ollama | No | No | Yes |
| Multi-agent | 5 types | No | No | No |
| Tree planning | Beam/MCTS/A* | No | No | No |
| Full code control | Open source | Closed | Closed | Open source |
| Cost (monthly) | $0–3 | ~$100 | ~$20 | API costs |

## Features

### Core
- **File Operations** — Read, create, edit, copy, move, delete, atomic write (including PDF, images, Jupyter notebooks)
- **Shell Execution** — Run commands with timeout, background execution, task management (Unix/Windows support)
- **Search** — Glob patterns, regex grep with context, semantic search with embeddings
- **Directory Operations** — Tree view, list directory contents, recursive operations
- **Diff Tools** — File diffs, git diffs, batch operations

### Tool Categories (54+ tools)
**File & Directory:** `read`, `write`, `edit`, `copy`, `move`, `delete`, `mkdir`, `atomicwrite`, `glob`, `list_dir`, `tree`, `diff`

**Shell & Execution:** `bash`, `task`, `task_output`, `task_stop`, `kill_shell`, `coordinate`

**Search & Navigation:** `grep`, `declarations`, `history_search`, `semantic_search`, `refactor`, `code_graph`

**Git Operations:** `git_status`, `git_add`, `git_commit`, `git_diff`, `git_log`, `git_branch`, `git_pr`, `git_blame`

**Testing & Verification:** `run_tests`, `verify_code`, `check_impact`

**Memory & Context:** `memory`, `memorize`, `shared_memory`, `pin_context`, `scratchpad`

**Collaboration:** `ask_user`, `ask_agent`, `web_search`, `web_fetch`

**Project Management:** `todo`, `env`, `plan_mode`, `undo_plan`, `redo_plan`

**Advanced:** `ssh`, `batch`, `coordinate`, `tools_list`, `request_tool`

### AI Providers
- **Google Gemini** — Gemini 3 Pro/Flash with OAuth support, free tier available
- **Anthropic Claude** — Claude 3.5/4 series for advanced reasoning
- **DeepSeek** — Excellent coding model, very affordable (~$1/month)
- **GLM/Z.AI** — Cost-effective model (~$3/month), Anthropic-compatible API
- **Ollama** — Local LLMs (Llama, Qwen, DeepSeek, CodeLlama), free & private
- **Client Pool** — Automatic fallback and load balancing across providers

### Intelligence
- **Multi-Agent System** — Specialized agents (Explore, Bash, Plan, General, Claude Code Guide) with adaptive delegation
- **Tree Planner** — Advanced planning with Beam Search, MCTS, A* algorithms
- **Agent Coordinator** — Parallel execution with dependency management
- **Context Predictor** — Predicts needed files based on access patterns
- **Semantic Search** — Find code by meaning, not just keywords (embedding-based)
- **Memory System** — Learn project patterns and remember information across sessions

### Productivity
- **Git Integration** — Status, add, commit, pull request, blame, diff, log, branch management
- **Task Management** — Todo list, background tasks, parallel execution, coordination
- **Memory System** — Session memory, shared memory, project learning, scratchpad
- **Sessions** — Save and restore conversation state, undo/redo for operations
- **Undo/Redo** — Revert file changes (including copy, move, delete operations)
- **Notifications** — Desktop notifications for long-running tasks

### Extensibility
- **MCP Support** — Connect to external MCP servers for additional tools and capabilities
- **Custom Agent Types** — Register your own specialized agents with specific toolsets
- **Permission System** — Control which operations require approval (ask/allow/deny policies)
- **Hooks** — Automate actions (pre/post tool execution, on error, on start/exit)
- **Themes** — Light and dark mode support
- **GOKIN.md** — Project-specific instructions and context
- **Safety Checks** — Automatic blocking of dangerous operations

## Installation

### Quick Install (recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/ginkida/gokin/main/install.sh | sh
```

Downloads the latest release binary for your OS and architecture, installs to `~/.local/bin`.

### From Source

```bash
git clone https://github.com/ginkida/gokin.git
cd gokin
go build -o gokin ./cmd/gokin

# Or install to ~/go/bin
go install ./cmd/gokin
```

### Requirements

- Go 1.25+
- One of:
  - Google account with Gemini subscription (OAuth login)
  - Google Gemini API key (free tier available)
  - DeepSeek API key
  - GLM-4 API key
  - Anthropic API key
  - Ollama installed locally (no API key needed)
## Quick Start

### 1. Authentication

**OAuth (recommended for Gemini subscribers):**
```bash
gokin
> /oauth-login
```

**API Key:**
```bash
# Gemini
export GEMINI_API_KEY="your-api-key"  # Get free key at https://aistudio.google.com/apikey

# Anthropic Claude
export ANTHROPIC_API_KEY="your-api-key"

# DeepSeek
export DEEPSEEK_API_KEY="your-api-key"

# GLM
export GLM_API_KEY="your-api-key"

gokin
```

**Or login inline:**
```bash
> /login gemini <your-api-key>
> /login anthropic <your-api-key>
> /login deepseek <your-api-key>
> /login glm <your-api-key>
```

### 2. Launch

```bash
cd /path/to/your/project
gokin
```

### 3. Getting Started

```
> Hello! Tell me about this project's structure

> Find all files with .go extension

> Create a function to validate email
```

## Project Statistics

| Metric | Value |
|--------|-------|
| **Language** | Go 1.25.6 |
| **Total Files** | 293 Go files |
| **Lines of Code** | ~98,700 lines |
| **AI Tools** | 50+ tools across 10 categories |
| **Providers** | 5 providers + Ollama (local) |
| **Agent Types** | 5 specialized agents |
| **Architecture** | Event-driven (Bubble Tea), Plugin-based |
| **Last Updated** | February 2026 |

### Code Organization

```
internal/
├── agent/         # Multi-agent coordination & planning
├── api/           # LLM provider clients (Gemini, Anthropic, DeepSeek, GLM)
├── chat/          # Session management & persistence
├── config/        # Configuration & validation
├── hooks/         # Event-driven automation
├── mcp/           # Model Context Protocol integration
├── memory/        # Cross-session memory system
├── permission/    # Fine-grained access control
├── semantic/      # Embeddings-based code search
├── ssh/           # Remote command execution
├── tools/         # 50+ extensible tools
├── undo/          # Undo/redo stack management
└── update/        # Automatic update system
```

## Supported AI Providers

| Provider | Models | Cost | Best For |
|----------|--------|------|----------|
| **Gemini** | gemini-2.5-flash-preview, gemini-2.5-pro-preview | Free tier + paid, OAuth support | Fast iterations, prototyping |
| **DeepSeek** | deepseek-chat, deepseek-reasoner | ~$1/month | Coding tasks, reasoning |
| **GLM/Z.AI** | glm-4.7, glm-4-flash | ~$3/month | Budget-friendly development |
| **Anthropic Claude** | claude-3.5-sonnet, claude-3.5-haiku | Pay-per-use | Advanced reasoning, complex tasks |
| **Ollama** | Any model from `ollama list` | Free (local) | Privacy, offline, custom models |

### Model Presets

| Preset | Provider | Model | Use Case |
|--------|----------|-------|----------|
| `fast` | Gemini | gemini-2.5-flash-preview | Quick responses |
| `creative` | Gemini | gemini-2.5-pro-preview | Complex tasks |
| `coding` | GLM | glm-4.7 | Budget coding |
| `advanced` | Anthropic | claude-3.5-sonnet | Advanced reasoning |
| `local` | Ollama | llama3.2 | Offline, privacy |

### Switching Providers
```yaml
# config.yaml
model:
  provider: "gemini"           # gemini, deepseek, glm, anthropic, or ollama
  name: "gemini-2.5-flash-preview"
  preset: "fast"               # or use preset instead
```

Or via environment: `export GOKIN_BACKEND="gemini"`

### Provider-Specific Features

**Gemini:**
- OAuth 2.0 authentication for Google AI subscribers (recommended)
- API key authentication for free tier access
- Automatic token refresh and session management

**DeepSeek:**
- Anthropic-compatible API interface
- DeepSeek-Reasoner model for chain-of-thought reasoning

**GLM/Z.AI:**
- Anthropic-compatible API
- Cost-effective for bulk operations
- Flash model for faster responses

**Anthropic Claude:**
- Native Claude API support
- Extended thinking capabilities
- Best for complex architectural decisions

**Ollama:**
- Local inference with no API costs
- Supports custom and fine-tuned models
- Privacy-focused, works offline

### Using Ollama
### Using Ollama

```bash
# 1. Install Ollama (https://ollama.ai)
curl -fsSL https://ollama.ai/install.sh | sh

# 2. Pull a model
ollama pull llama3.2

# 3. Run Gokin with Ollama
gokin --model llama3.2
```

> **Note:** Tool calling support varies by model. Llama 3.1+, Qwen 2.5+, and Mistral have good tool support.

## Commands

All commands start with `/`:

| Command | Description |
|---------|-------------|
| `/help [command]` | Show help |
| `/clear` | Clear conversation history |
| `/compact` | Force context compression |
| `/cost` | Show token usage and cost |
| `/sessions` | List saved sessions |
| `/save [name]` | Save current session |
| `/resume <id>` | Restore session |
| `/undo` | Undo last file change |
| `/commit [-m message]` | Create commit |
| `/pr [--title title]` | Create pull request |
| `/config` | Show current configuration |
| `/doctor` | Check environment |
| `/init` | Create GOKIN.md for project |
| `/model <name>` | Change AI model |
| `/theme` | Switch UI theme |
| `/permissions` | Manage tool permissions |
| `/sandbox` | Toggle sandbox mode |
| `/update` | Check for and install updates |
| `/browse` | Interactive file browser |
| `/copy` / `/paste` | Clipboard operations |
| `/oauth-login` | Login via Google account |
| `/login <provider> <key>` | Set API key (gemini, deepseek, glm, ollama) |
| `/logout` | Remove saved API key |
| `/semantic-stats` | Semantic index statistics |
| `/semantic-reindex` | Force reindex |
| `/semantic-cleanup` | Clean up old projects |
| `/register-agent-type` | Register custom agent type |

## AI Tools

AI has access to **50+ tools** organized across 10 categories:

### File Operations
| Tool | Description |
|------|-------------|
| `read` | Read file contents with line numbers and offset support |
| `write` | Write or append content to files (creates if needed) |
| `edit` | String replacement with regex, line-based, or exact match modes |
| `copy` | Copy files or directories recursively |
| `move` | Move or rename files and directories |
| `delete` | Delete files or directories with recursive option |
| `mkdir` | Create directories with parent creation |
| `diff` | Compare files or content with configurable context |
| `batch` | Apply operations to multiple files matching a pattern |
| `declarations` | Extract and analyze code declarations |

### Search & Navigation
| Tool | Description |
|------|-------------|
| `glob` | Find files by pattern with wildcard support |
| `grep` | Search content with regex and context options |
| `list_dir` | List directory contents with metadata |
| `tree` | Display directory tree structure |
| `semantic_search` | Semantic code search using embeddings |
| `code_graph` | Analyze code dependencies and relationships |
| `history_search` | Search through command history |

### Execution & Testing
| Tool | Description |
|------|-------------|
| `bash` | Execute shell commands with timeout and background mode |
| `ssh` | Remote command execution and file transfer |
| `kill_shell` | Terminate background shell tasks |
| `env` | Get or list environment variables |
| `run_tests` | Run project tests with auto-detection |

### Git Workflow
| Tool | Description |
|------|-------------|
| `git_status` | Show working tree status |
| `git_add` | Stage files for commit |
| `git_commit` | Create commits with auto-message option |
| `git_log` | Show commit history with filtering |
| `git_blame` | Show line-by-line authorship |
| `git_diff` | Show changes between commits/branches |
| `git_branch` | List, create, delete, or switch branches |
| `git_pr` | Create and manage GitHub pull requests |

### Web & Research
| Tool | Description |
|------|-------------|
| `web_fetch` | Fetch and parse web content as markdown |
| `web_search` | Search the web for current information |

### Planning & Coordination
| Tool | Description |
|------|-------------|
| `enter_plan_mode` | Create execution plans with approval |
| `update_plan_progress` | Update plan step status |
| `get_plan_status` | Get current plan status |
| `exit_plan_mode` | Exit plan mode with summary |
| `undo_plan` | Undo last executed plan |
| `redo_plan` | Redo previously undone plan |
| `todo` | Manage task lists for tracking |
| `coordinate` | Orchestrate multi-agent parallel tasks |
| `task` | Spawn specialized subagents |
| `task_output` | Get output from background tasks |
| `task_stop` | Stop running background tasks |

### Memory & Context
| Tool | Description |
|------|-------------|
| `memory` | Persistent memory storage (remember/recall/forget) |
| `shared_memory` | Inter-agent communication and state sharing |
| `update_scratchpad` | Update scratchpad with important context |
| `memorize` | Store facts for future recall |
| `pin_context` | Pin important context for easy access |
| `ask_user` | Ask questions for clarification |

### Code Analysis & Refactoring
| Tool | Description |
|------|-------------|
| `refactor` | AST-based code refactoring (rename, extract, inline) |
| `code_graph` | Build dependency graphs and find cycles |
| `verify_code` | Verify code correctness and patterns |
| `check_impact` | Analyze impact of proposed changes |

### Agent Coordination
| Tool | Description |
|------|-------------|
| `ask_agent` | Request help from specialized agents |
| `request_tool` | Request new tools from the system |
| `notifications` | Send notifications to users |

### Advanced Features
| Tool | Description |
|------|-------------|
| `safety` | Safety checks and validation |
| `tools_list` | List all available tools |

## Configuration

Config file: `~/.config/gokin/config.yaml`

### Quick Start Configuration

```yaml
api:
  gemini_key: ""               # Or via GEMINI_API_KEY env var
  anthropic_key: ""            # Or via ANTHROPIC_API_KEY
  deepseek_key: ""             # Or via DEEPSEEK_API_KEY
  glm_key: ""                  # Or via GLM_API_KEY
  active_provider: "gemini"     # gemini, anthropic, deepseek, glm, ollama
  ollama_base_url: ""          # Default: http://localhost:11434
  # OAuth for Gemini (recommended for subscribers)
  gemini_oauth:
    access_token: ""
    refresh_token: ""
    expires_at: 0
    email: ""
    project_id: ""

model:
  # Simple configuration using presets
  preset: "fast"               # fast, balanced, creative, coding, advanced, local
  provider: "auto"             # auto, gemini, anthropic, deepseek, glm, ollama

  # Or manual configuration (overrides preset)
  name: "gemini-2.5-flash-preview"
  temperature: 1.0
  max_output_tokens: 8192
  enable_thinking: false       # Anthropic extended thinking
  thinking_budget: 0           # Max tokens for thinking (0 = disabled)
  fallback_providers: []       # Fallback providers if primary fails
  max_pool_size: 5             # Connection pool size

tools:
  timeout: 2m
  bash:
    sandbox: true
    blocked_commands: []       # Additional blocked commands
  allowed_dirs: []             # Additional allowed directories
  formatters:                  # File formatters: ext → command
    ".py": "black"
    ".js": "prettier --write"

permission:
  enabled: true
  default_policy: "ask"        # allow, ask, deny
  rules:                       # Per-tool rules
    read: "allow"
    write: "ask"

# Advanced configuration
plan:
  enabled: true
  require_approval: true
  auto_detect: true            # Auto-trigger planning for complex tasks
  clear_context: false         # Clear context before plan execution
  delegate_steps: false        # Run each step in isolated sub-agent
  abort_on_step_failure: false
  planning_timeout: 10m
  default_step_timeout: 0      # 0 = 5 minutes
  use_llm_expansion: true      # Use LLM for dynamic plan expansion
  algorithm: "beam"            # beam, mcts, astar

ui:
  stream_output: true
  markdown_rendering: true
  show_tool_calls: true
  show_token_usage: true
  mouse_mode: "enabled"
  theme: "dark"                # dark, light, sepia, cyber, forest, ocean, monokai, dracula, high_contrast
  show_welcome: true
  hints_enabled: true
  compact_mode: false
  bell: true
  native_notifications: false

context:
  max_input_tokens: 0          # 0 = use model default
  warning_threshold: 0.8       # Warn at 80% of context
  summarization_ratio: 0.5     # Summarize to 50%
  tool_result_max_chars: 100000
  auto_compact_threshold: 0.75 # Compact at 75% usage
  enable_auto_summary: true

session:
  enabled: true
  save_interval: 2m
  auto_load: false             # Auto-load last session on startup

memory:
  enabled: true
  max_entries: 1000
  auto_inject: true            # Auto-inject memories into system prompt

semantic:
  enabled: true
  model: "text-embedding-004"
  index_on_start: true
  max_file_size: 1048576       # 1MB in bytes
  cache_ttl: 24h
  top_k: 10
  chunk_size: 1000
  chunk_overlap: 200
  auto_cleanup: true
  index_patterns:
    - "**/*.go"
    - "**/*.py"
    - "**/*.js"
    - "**/*.ts"
    - "**/*.java"
    - "**/*.rs"
    - "**/*.cpp"
    - "**/*.c"
    - "**/*.h"
  exclude_patterns:
    - "**/node_modules/**"
    - "**/.git/**"
    - "**/vendor/**"

hooks:
  enabled: false
  hooks:
    - name: "pre-commit hook"
      type: "pre_tool"
      tool_name: "git_commit"
      command: "echo 'About to commit'"
      enabled: true
      condition: "always"      # always, if_previous_success, if_previous_failure
      fail_on_error: false

mcp:
  enabled: false
  servers:
    - name: "github"
      transport: "stdio"       # stdio or http
      command: "npx"
      args: ["-y", "@modelcontextprotocol/server-github"]
      env:
        GITHUB_TOKEN: "${GITHUB_TOKEN}"
      auto_connect: true
      timeout: 30s
      max_retries: 3
      retry_delay: 1s
      tool_prefix: ""

update:
  enabled: true
  auto_check: true
  check_interval: 24h
  auto_download: false
  include_prerelease: false
  channel: "stable"            # stable, beta, nightly
  github_repo: "ginkida/gokin"
  max_backups: 3
  verify_checksum: true
  notify_only: false
  timeout: 30s

logging:
  level: "info"                # debug, info, warn, error

audit:
  enabled: false
  max_entries: 1000
  max_result_len: 10000
  retention_days: 30

rate_limit:
  enabled: false
  requests_per_minute: 60
  tokens_per_minute: 100000
  burst_size: 10

cache:
  enabled: true
  capacity: 1000
  ttl: 1h

watcher:
  enabled: true
  debounce_ms: 300
  max_watches: 100

diff_preview:
  enabled: true                # Enable diff preview for write/edit operations

contract:
  enabled: true                 # Contract-driven development
  require_approval: true
  auto_detect: true
  auto_verify: true
  verify_timeout: 5m
  store_path: "./contracts"
  inject_context: true
```

### Environment Variables

| Variable | Description |
|----------|-------------|
| `GEMINI_API_KEY` | Gemini API key |
| `ANTHROPIC_API_KEY` | Anthropic Claude API key |
| `DEEPSEEK_API_KEY` | DeepSeek API key |
| `GLM_API_KEY` | GLM/Z.AI API key |
| `OLLAMA_API_KEY` | Ollama API key (for remote servers) |
| `OLLAMA_HOST` | Ollama server URL (default: http://localhost:11434) |
| `GOKIN_MODEL` | Model name (overrides config) |
| `GOKIN_BACKEND` | Backend: gemini, anthropic, deepseek, glm, or ollama |
| `GOKIN_CONFIG` | Path to config file (default: ~/.config/gokin/config.yaml) |
| `GOKIN_DISABLE_UPDATE_CHECK` | Disable automatic update checking |
| `GOKIN_DATA_DIR` | Custom data directory path |
| `GOKIN_LOG_LEVEL` | Log level: debug, info, warn, error |
| `GOKIN_THEME` | UI theme: dark, light, sepia, cyber, forest, ocean, monokai, dracula, high_contrast |

### File Locations

| Path | Contents |
|------|----------|
| `~/.config/gokin/config.yaml` | Configuration |
| `~/.local/share/gokin/sessions/` | Saved sessions |
| `~/.local/share/gokin/memory/` | Memory data |
| `~/.config/gokin/semantic_cache/` | Semantic search index |

## MCP (Model Context Protocol)

Extend Gokin with external tools via [MCP servers](https://modelcontextprotocol.io/).

```yaml
# config.yaml
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

### Popular MCP Servers

| Server | Package | Description |
|--------|---------|-------------|
| GitHub | `@modelcontextprotocol/server-github` | GitHub API integration |
| Filesystem | `@modelcontextprotocol/server-filesystem` | File system access |
| Brave Search | `@modelcontextprotocol/server-brave-search` | Web search |
| Puppeteer | `@modelcontextprotocol/server-puppeteer` | Browser automation |
| Slack | `@modelcontextprotocol/server-slack` | Slack integration |

Find more servers at: https://github.com/modelcontextprotocol/servers

## Security

- **Automatic Secret Redaction** — API keys, tokens, passwords are masked in AI output and logs
- **Sandbox Mode** — Bash commands run in a restricted environment with blocked dangerous commands
- **Permission System** — Control which tools require approval (allow / ask / deny per tool)
- **Environment Isolation** — API keys excluded from subprocesses, config files use owner-only permissions

```
# What appears in files:              # What AI sees:
export GEMINI_API_KEY="AIzaSy..."  →  export GEMINI_API_KEY="[REDACTED]"
password: "super_secret_123"       →  password: "[REDACTED]"
```

## Advanced Features

### Semantic Code Search
Find code by meaning using embeddings and natural language queries. The semantic search system automatically indexes your project on launch and supports:

**Key Features:**
- **Enhanced Indexing** - Graph-based indexing with chunker support for optimal code understanding
- **Incremental Updates** - Only reindexes changed files for faster performance
- **Similarity Scoring** - Finds semantically similar code even with different syntax
- **Graph Traversal** - Navigate code relationships and dependencies
- **Smart Caching** - Cached embeddings for faster repeated searches

**Usage Examples:**
```
"where is authentication implemented?"
"find all database connection code"
"show me error handling patterns"
```

### MCP Integration
Full Model Context Protocol (MCP) support for extending Gokin with external tools and servers:

**Features:**
- **Multi-Server Management** - Connect to multiple MCP servers simultaneously
- **Health Monitoring** - Automatic health checks with configurable thresholds
- **Auto-Healing** - Automatic reconnection with exponential backoff
- **Tool Integration** - MCP tools appear as native Gokin tools
- **Timeout Management** - Per-server connection timeouts (default: 15s)
- **Parallel Execution** - Execute MCP tools in parallel with built-in tools

**Configuration:**
```yaml
mcp:
  servers:
    - name: "filesystem"
      command: "npx"
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/Users/ginkida/github"]
      timeout: 30s
      max_retries: 3
      enabled: true
```

### Multi-Agent Coordination
Advanced multi-agent system with parallel execution and dependency management:

**Agent Types:**
- **Explore Agent** - Read-only codebase exploration (glob, grep, read, tree)
- **Bash Agent** - Command execution focus (bash, ssh, env)
- **General Agent** - Full toolset access for complex tasks
- **Plan Agent** - Planning and analysis with read-only tools
- **Claude Code Guide** - Documentation and help agent

**Coordination Features:**
- **Parallel Execution** - Run up to 3 agents concurrently (configurable)
- **Task Dependencies** - Define dependencies between tasks for ordered execution
- **Shared Memory** - Agents share discovered facts, insights, and file states
- **Priority Queue** - Higher priority tasks execute first (1-10 scale)
- **Progress Tracking** - Real-time progress updates from running agents
- **Error Reflection** - Agents learn from errors and self-correct
- **Checkpoint/Restore** - Save and restore agent state for long-running tasks

**Usage:**
```yaml
agent:
  coordination:
    max_parallel: 3
    timeout: 10m
    shared_memory_enabled: true
```

### Session Persistence
Complete session management with branching and versioning:

**Features:**
- **Branch Management** - Create named branches for exploration
- **Checkpoints** - Save named restore points in conversation history
- **Change Tracking** - Track history changes with version control
- **Token Counting** - Track token usage per message and total
- **Auto-Save** - Automatic session persistence at configured intervals
- **History Limit** - Keeps up to 100 messages in memory (configurable)
- **Optimistic Concurrency** - Version-based conflict prevention

**Storage:** `~/.local/share/gokin/sessions/`

### Undo/Redo System
Full file change tracking with undo and redo capabilities:

**Features:**
- **Change Tracking** - Automatic tracking of all file modifications
- **Undo Stack** - Revert file changes with detailed information
- **Redo Support** - Re-apply undone changes (up to 50 operations)
- **Multi-File Support** - Track changes across multiple files
- **Safe Reversion** - Atomic undo operations with rollback on failure
- **Stack Management** - Clear redo stack on new changes

**Usage:**
- `/undo` - Undo the last change
- `/redo` - Redo the last undone change
- Automatic integration with file operations

### Automatic Updates
Built-in update system with rollback support:

**Features:**
- **Version Checking** - Automatic checks for new releases
- **Multiple Channels** - Support for stable, beta, and nightly channels
- **Secure Downloads** - Checksum verification for all downloads
- **Rollback Support** - Automatic backup before updates
- **Notification System** - Notify-only mode for controlled updates
- **Update Caching** - Cached update information for faster checks
- **Backup Management** - Automatic cleanup of old backups

**Configuration:**
```yaml
update:
  channel: stable  # stable, beta, nightly
  check_interval: 24h
  auto_download: true
  verify_checksum: true
  notify_only: false
```

### Tree-Based Planning
Advanced planning algorithms for complex task decomposition:

**Algorithms:**
- **Beam Search** - Explore top-k solutions at each step (default)
- **MCTS (Monte Carlo Tree Search)** - Probabilistic exploration for optimal paths
- **A* Search** - Heuristic-based search for guaranteed optimal solutions

**Features:**
- **Auto-Detection** - Automatically enables planning for complex tasks
- **Step Timeout** - Per-step timeout with configurable defaults (default: 5m)
- **LLM Expansion** - AI-generated step expansion for detailed plans
- **Progress Tracking** - Real-time progress updates during execution
- **Abort on Failure** - Optional abort on step failure (default: false)
- **Context Clearing** - Optional context clearing between steps

**Configuration:**
```yaml
plan:
  enabled: true
  require_approval: true
  auto_detect: true
  algorithm: beam  # beam, mcts, astar
  delegate_steps: false
  default_step_timeout: 5m
```

### Memory System
Cross-session memory with project learning:

**Features:**
- **Persistent Storage** - Memories stored in `~/.local/share/gokin/memory/`
- **Auto-Injection** - Relevant memories automatically injected into context
- **Memory Types** - Support for different memory scopes (session, project, global)
- **Max Entries** - Configurable limit on stored memories (default: 1000)
- **Search** - Quick memory search and retrieval

**Usage:**
- "remember that this project uses PostgreSQL 15"
- "recall our database schema decisions"
- `/memory list` - View all stored memories

### Permission System
Fine-grained access control for tools and operations:

**Features:**
- **Per-Tool Rules** - Allow/deny specific tools
- **Command Blocking** - Block dangerous bash commands (rm -rf, etc.)
- **Directory Restrictions** - Restrict file operations to specific directories
- **Policy-Based** - Define policies for different operation types
- **Runtime Checks** - Automatic permission verification before tool execution

**Configuration:**
```yaml
permissions:
  rules:
    - tool: "bash"
      action: allow
      conditions:
        blocked_commands:
          - "rm -rf /"
          - "mkfs"
    - tool: "write"
      action: allow
      conditions:
        allowed_dirs:
          - "/Users/ginkida/github"
```

### Hooks System
Event-driven automation with configurable hooks:

**Available Events:**
- `pre_tool` - Before tool execution
- `post_tool` - After successful tool execution
- `on_error` - When a tool fails
- `on_start` - When Gokin starts
- `on_exit` - When Gokin exits

**Features:**
- **Conditional Execution** - Execute hooks based on conditions
- **Error Handling** - Configure whether to fail on hook errors
- **Async Support** - Run hooks asynchronously without blocking
- **Tool Filtering** - Execute hooks for specific tools only

**Configuration:**
```yaml
hooks:
  - name: "notify on error"
    events: ["on_error"]
    command: "notify-send 'Gokin Error' '{{.Error}}'"
    async: true
    fail_on_error: false
  - name: "backup before write"
    events: ["pre_tool"]
    tools: ["write", "edit"]
    command: "cp {{.Path}} {{.Path}}.backup"
```

### GOKIN.md
Create project-specific instructions with `/init`. The AI reads this file on startup for:

- Project context and overview
- Code standards and conventions
- Build and test commands
- Architecture decisions
- Team-specific workflows

### Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Ctrl+C` | Interrupt / Exit |
| `Ctrl+P` | Command palette |
| `Ctrl+G` | Toggle select mode (freeze viewport for text selection) |
| `Option+C` | Copy last AI response (macOS) |
| `↑` / `↓` | Input history |
| `Tab` | Autocomplete |

## Architecture

### Multi-Agent System

Gokin uses a sophisticated multi-agent architecture with specialized agents:

**Agent Types:**
- **Explore Agent** - Read-only exploration (glob, grep, read, tree)
- **Bash Agent** - Command execution focus (bash, ssh, env)
- **General Agent** - Full toolset access for complex tasks
- **Plan Agent** - Planning and analysis with read-only tools

**Coordination Features:**
- **Task Coordination** - Execute multiple tasks in parallel with dependencies
- **Shared Memory** - Agents share discovered facts and insights
- **Checkpoint/Restore** - Save and restore agent state for long-running tasks
- **Delegation** - Automatic agent selection based on task requirements
- **Progress Tracking** - Real-time progress updates from running agents

### Tree-Based Planning

Gokin automatically breaks down complex tasks into plans:

**Algorithms:**
- **Beam Search** - Explores multiple solution paths in parallel
- **MCTS** - Monte Carlo Tree Search for optimal decision making
- **A*** - Best-first search with heuristics

**Plan Features:**
- Automatic plan generation before execution
- Step-by-step approval process
- Progress tracking and visualization
- Undo/redo for entire plans
- Plan search and scoring

### Semantic Search & Code Understanding

**Vector-Based Search:**
- Automatic code indexing with embeddings
- Find code by meaning, not just keywords
- Similarity scoring for results
- Background reindexing on file changes

**Code Analysis:**
- Dependency graph building
- Code relationship mapping
- Impact analysis before changes
- AST-based refactoring

### Session & State Management

**Persistence:**
- Automatic session saving
- Resume with `gokin --resume`
- Session history and replay
- State checkpoint/restore

**Context Management:**
- Automatic token counting
- Smart summarization
- Context compaction
- Predictive file loading

## Usage Examples

### Code Analysis
```
> Explain what the ProcessOrder function does in order.go
> Find all places where this function is used
> Are there potential performance issues?
```

### Refactoring
```
> Rename getUserData function to fetchUserProfile in all files
> Extract repeated error handling code into a separate function
```

### Writing Code
```
> Create a REST API endpoint to get user list
> Add input validation
> Write unit tests for this endpoint
```

### Git Workflow
```
> Show changes since last commit
> /commit -m "feat: add user validation"
> /pr --title "Add user validation feature"
```

### Debugging
```
> The app crashes on startup, here's the error: [error]
> Check logs and find the cause
> Fix the problem
```

## Troubleshooting

### Check Environment
```
/doctor
```

### Authentication Error
```
/auth-status
/logout
/login --oauth --client-id=YOUR_ID
```

### Context Overflow
```
/compact
```
or
```
/clear
```

### Permission Issues
Check `~/.config/gokin/config.yaml`:
```yaml
permission:
  enabled: true
  default_policy: "ask"
```

## License

MIT License
