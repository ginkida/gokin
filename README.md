![Gokin](https://minio.ginkida.dev/minion/github/gokin.jpg)

<p align="center">
  <a href="https://github.com/ginkida/gokin/releases"><img src="https://img.shields.io/github/v/release/ginkida/gokin" alt="Release"></a>
  <a href="https://github.com/ginkida/gokin/stargazers"><img src="https://img.shields.io/github/stars/ginkida/gokin" alt="Stars"></a>
  <a href="https://github.com/ginkida/gokin/releases"><img src="https://img.shields.io/github/downloads/ginkida/gokin/total" alt="Downloads"></a>
  <a href="https://github.com/ginkida/gokin/blob/main/LICENSE"><img src="https://img.shields.io/github/license/ginkida/gokin" alt="License"></a>
  <img src="https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go" alt="Go Version"></p>

<p align="center">
  <img src="https://minio.ginkida.dev/minion/github/gokin-cli-cut.gif" alt="Gokin Demo" width="800">
</p>

<h3 align="center">AI-powered coding agent for your terminal<br>Kimi · GLM · MiniMax · DeepSeek · Ollama — 100% open source</h3>

<p align="center">
  <a href="#installation">Install</a> •
  <a href="#quick-start">Quick Start</a> •
  <a href="#why-gokin">Why Gokin?</a> •
  <a href="#features">Features</a> •
  <a href="#providers">Providers</a> •
  <a href="#configuration">Config</a> •
  <a href="#contributing">Contribute</a>
</p>

---

## Why Gokin? <a id="why-gokin"></a>

Most AI coding tools are closed-source, route your code through third-party servers, and give you zero control over what gets sent to the model. Gokin is different: **a fast, secure, zero-telemetry CLI where your code goes directly to the provider you chose — and nothing else leaves your machine.**

Five providers, one interface: **Kimi, GLM, MiniMax, DeepSeek** (via Anthropic-compatible APIs) and **Ollama** (fully local). Secrets and credentials are automatically redacted before reaching any model, TLS is enforced on every connection, and no proxy or middleware ever touches your data.

| | Gokin | Claude Code | Cursor |
|---|-------|-------------|--------|
| **Price** | Free → Pay-per-use | $20+/month | $20+/month |
| **Providers** | 5 (Kimi, GLM, MiniMax, DeepSeek, Ollama) | 1 (Claude) | Multi |
| **Offline** | Ollama | — | — |
| **Tools** | 59 built-in + MCP | ~30 | ~30 |
| **Agents** | 5 parallel, shared memory | Basic | — |
| **Direct API** | Zero proxies | Yes | Routes through Cursor servers |
| **Security** | TLS 1.2+, secret redaction, sandbox, 3-level permissions | Basic | Basic |
| **Open Source** | Yes | — | — |

**Choose your stack:**

| Stack | Cost | Best For |
|-------|------|----------|
| **Gokin + DeepSeek V4** | Pay-per-use | **Recommended** — 1M context, top SWE-bench reasoning, prompt caching (95% savings) |
| **Gokin + Kimi Coding Plan** | Subscription | Default — 262K context, thinking + tool use, coding-tuned |
| **Gokin + GLM Coding Plan** | ~$3/month | Budget-friendly daily coding, GLM-5.1 with 200K context |
| **Gokin + MiniMax** | Pay-per-use | 200K context, strong on agentic coding |
| **Gokin + Ollama** | Free | Privacy, offline, no API costs |

All cloud providers are daily-driver tier — tested against every release.

---

## Installation <a id="installation"></a>

### One-liner

```bash
curl -fsSL https://raw.githubusercontent.com/ginkida/gokin/main/install.sh | sh
```

### From source

```bash
git clone https://github.com/ginkida/gokin.git
cd gokin
go build -o gokin ./cmd/gokin
./gokin --setup
```

### Requirements

- **Go 1.25+** (build from source)
- **One AI provider** (see [Providers](#providers) below)

---

## Quick Start <a id="quick-start"></a>

```bash
# Interactive setup — picks provider + API key
gokin --setup

# Or set an API key and go
export GOKIN_DEEPSEEK_KEY="sk-..."   # DeepSeek V4 (recommended)
gokin

# Other providers work the same way:
# export GOKIN_KIMI_KEY="sk-kimi-..."   # Kimi Coding Plan (default)
# export GOKIN_GLM_KEY="..."            # GLM Coding Plan
# export GOKIN_MINIMAX_KEY="..."        # MiniMax
# Ollama needs no key — just run a local model
```

**Then just talk naturally:**

```
> Explain how auth works in this project
> Add user registration endpoint with validation
> Run the tests and fix any failures
> Refactor this module to use dependency injection
> Create a PR for these changes
```

---

## Key Features <a id="features"></a>

### Code Understanding
- **Multi-file analysis** — grep + glob + read across the whole codebase
- **Session memory** — auto-summarizes files, tools, errors, decisions; survives compaction
- **Context-aware execution** — read-only tools run in parallel, write tools serialized

### Project Instructions
```
Priority:  Low ──────────────────────────────── High
           Global → User → Project → Local

Global:    ~/.config/gokin/GOKIN.md
User:      ~/.gokin/GOKIN.md
Project:   ./GOKIN.md, .gokin/rules/*.md
Local:     ./GOKIN.local.md (git-ignored)
```
All layers merged automatically. `@include` directive for composability. File watching with auto-reload.

### 59 Built-in Tools
- **Files**: read, write, edit, diff, copy, move, delete, refactor, batch
- **Search**: glob, grep, tree, history_search
- **Git**: status, commit, diff, branch, log, blame, PR
- **Run**: bash, run_tests, ssh, env, kill_shell
- **Plan**: todo, task, enter/exit plan_mode, coordinate, verify_code
- **Memory**: memorize, shared_memory, pin_context, scratchpad
- **MCP**: manage servers from chat via `mcp_admin` tool, or `/mcp add` command (stdio + http transports, per-server permissions)
- **Parallel execution** — read-only tools run concurrently when the model calls multiple

### Multi-Agent System
- Up to 5 parallel agents with shared memory
- Automatic task decomposition via coordinator
- Provider failover — agents try fallback providers on failure
- Git worktree support — isolated branch work
- Real-time streaming output

### Autonomous Loops
```bash
/loop check the deploy every 20m     # interval-based
/loop fix bugs in this app            # self-paced (model decides when to continue)
```
Background scheduler fires recurring tasks without blocking the foreground. Auto-pauses after 5 consecutive failures. Persists across sessions.

### Plan Mode
Physical tool restriction — plan mode limits the model to read-only tools (read, grep, glob, diff, git status/log). Auto-exits when you approve the plan, restoring full tool access.

### Cost Tracking
- Per-model pricing for all cloud providers (Ollama is free)
- Real-time cost in status bar
- `/cost` and `/stats` commands

### Prompt Caching
- `cache_control` breakpoints for Kimi, MiniMax, and DeepSeek — up to 90% input cost savings
- System prompt, tools, and conversation prefix cached

### Extended Thinking
- Full multi-turn support for Kimi / GLM / DeepSeek reasoning models
- Thinking blocks with `signature` preserved across turns, including tool calls

### Safety & Permissions
- **3-level permissions**: Low (auto), Medium (ask once), High (always ask)
- **Sandbox mode** for bash commands
- **Inline diff preview** — 3-line preview cards before applying changes
- **Undo/Redo** for all file operations (`/undo N` up to 20 steps)
- **Proactive compaction** — predicts token growth and compacts before hitting limits

---

## Security & Privacy <a id="security"></a>

### Zero Proxies

```
┌──────────┐          ┌──────────────────────┐
│  Gokin   │ ──TLS──▶ │  Provider API        │
│  (local) │          │  (Kimi / Z.AI / ...) │
│          │ ◀──TLS── │                      │
└──────────┘          └──────────────────────┘
       No middle servers. No telemetry. Direct.
```

Every API call goes directly from your machine to the provider's endpoint. No proxy servers, no analytics gateways. You can verify this — it's open source.

### Secret Redaction

LLM tool calls can accidentally expose secrets found in your codebase. Gokin automatically redacts them **before** they reach the model:

| Category | Examples |
|----------|----------|
| API keys | `AKIA...`, `ghp_...`, `sk_live_...`, `AIza...` |
| Tokens | Bearer tokens, JWT (`eyJ...`), Slack/Discord tokens |
| Credentials | Database URIs (`postgres://user:pass@...`), Redis, MongoDB |
| Crypto material | PEM private keys, SSH keys |

24 regex patterns, applied to every tool result and audit log.

### Defense in Depth

| Layer | What it does |
|-------|-------------|
| **TLS 1.2+** | No weak ciphers, certificate verification always on |
| **Sandbox** | Bash in isolated namespace, safe env whitelist (~35 vars) |
| **Command validation** | 50+ blocked patterns: fork bombs, reverse shells, credential theft |
| **SSH validation** | Host allowlist, loopback blocked, injection prevention |
| **Path validation** | Symlink resolution, directory traversal blocked |
| **SSRF protection** | Private IPs, loopback, link-local blocked |
| **Audit trail** | Every tool call logged with sanitized args |

### Keys Stay Local

- Loaded from env vars or `~/.config/gokin/config.yaml`
- Masked in all UI displays (`sk-12****cdef`)
- Never included in conversation history or tool results
- Ollama mode: zero network calls — fully airgapped

---

## Providers <a id="providers"></a>

| Provider | Models | Context | Cost ($/1M tokens) |
|----------|--------|---------|---------------------|
| **DeepSeek** | `deepseek-v4-pro`, `v4-flash`, `chat`, `reasoner` | 1M input, 384K output | Pro $0.44/$0.87, Flash $0.14/$0.28 |
| **Kimi** | `kimi-for-coding` | 262K input, 32K output | $0.95/$4.00 |
| **GLM** | `glm-5.1`, `glm-5`, `glm-5-turbo`, `glm-4.7`, `glm-4.5` | 200K input, 131K output | 5.1: $4/$16, 5: $1/$4 |
| **MiniMax** | `MiniMax-M2.7`, `M2.7-highspeed`, `M2.5`, `M2.5-highspeed` | 200K input, 16K output | M2.7: $0.30/$1.20 |
| **Ollama** | Any local model | Varies | Free |

All cloud providers use Anthropic-compatible APIs and share the same client — fewer moving parts, consistent behavior. Prompt caching is supported on Kimi, MiniMax, and DeepSeek (live-verified 95% savings on repeat prefixes). Ollama uses its own native client and makes zero network calls.

Switch anytime:
```
/provider deepseek    →  /model deepseek-v4-pro
/provider kimi        →  /model kimi-for-coding
/provider glm         →  /model glm-5.1
/provider minimax     →  /model MiniMax-M2.7
/provider ollama      →  /model llama3.2
```

---

## Commands <a id="commands"></a>

65 slash commands. Some highlights:

| Command | Description |
|---------|-------------|
| `/login <provider> <key>` | Set API key |
| `/provider` / `/model` | Switch provider or model |
| `/plan` | Enter read-only planning mode |
| `/commit` / `/pr` | Git commit, create GitHub PR |
| `/undo [N]` | Undo last N file changes (max 20) |
| `/loop <task> [interval]` | Autonomous background loop |
| `/mcp [list\|add\|remove]` | Manage MCP servers |
| `/stats` / `/cost` | Session statistics, token costs |
| `/doctor` | Diagnostics check |
| `/shortcuts` | Keyboard shortcuts reference |
| `/help` | Show all 65 commands |

**Aliases:** `p`=plan, `c`=commit, `m`=model, `s`=status, `u`=undo, `r`=redo, `h`=help, `q`=clear, `st`=stats, `pr`=pr

### Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Ctrl+C` | Interrupt / cancel |
| `Ctrl+K` | Model selector |
| `Ctrl+E` | Expand/collapse tool output |
| `Ctrl+P` | Command palette |
| `Tab` | Autocomplete |
| `↑/↓` | History |
| `y/n` | Accept/reject diff |

---

## Configuration <a id="configuration"></a>

`~/.config/gokin/config.yaml`

### Minimal

```yaml
# DeepSeek (recommended)
api: { deepseek_key: "sk-...", active_provider: "deepseek" }
model: { name: "deepseek-v4-pro" }

# Or Kimi (default)
api: { kimi_key: "sk-kimi-...", active_provider: "kimi" }
model: { name: "kimi-for-coding" }

# Or GLM / MiniMax
api: { glm_key: "...", active_provider: "glm" }
model: { name: "glm-5.1" }
```

### Full Reference

```yaml
api:
  kimi_key: ""
  deepseek_key: ""
  glm_key: ""
  minimax_key: ""
  ollama_key: ""                  # optional, for remote Ollama with auth
  active_provider: "kimi"         # kimi | deepseek | glm | minimax | ollama
  ollama_base_url: "http://localhost:11434"
  retry:
    max_retries: 10
    http_timeout: 120s
    stream_idle_timeout: 30s

model:
  name: "kimi-for-coding"
  temperature: 0.6
  max_output_tokens: 32768
  custom_base_url: ""             # override endpoint
  enable_thinking: false          # supported on Kimi, DeepSeek, GLM, MiniMax
  thinking_budget: 0              # 0 = provider default

tools:
  timeout: 2m
  model_round_timeout: 5m
  bash: { sandbox: true }

permission:
  enabled: true
  default_policy: "ask"           # allow | ask | deny

plan:
  enabled: true
  require_approval: true

mcp:
  enabled: false                  # enable MCP server support
  servers: {}                     # server configs (stdio/http)
```

---

## Architecture <a id="architecture"></a>

```
gokin/
├── cmd/gokin/          # CLI entry point
├── internal/
│   ├── app/            # Orchestrator (~2.7K LOC) & message loop (~3.7K LOC)
│   ├── agent/          # Multi-agent system (~4.8K LOC)
│   ├── client/         # AnthropicClient (Kimi/GLM/MiniMax/DeepSeek) + OllamaClient
│   ├── tools/          # 59 built-in tools, 9 tool sets
│   ├── mcp/            # MCP client + manager (stdio/http)
│   ├── loops/          # Autonomous loop scheduler
│   ├── ui/             # Bubble Tea TUI (46 source files, Graphite+Violet theme)
│   ├── config/         # YAML config
│   ├── permission/     # 3-level security + per-MCP-server isolation
│   ├── memory/         # Persistent memory
│   └── ...             # 36 packages total
```

**610 Go files (376 source, 234 test) • 100% Go • 65 slash commands**

---

## Contributing <a id="contributing"></a>

```bash
git clone https://github.com/ginkida/gokin.git
cd gokin
go build -o gokin ./cmd/gokin
go test -race ./...    # 36 packages, all must pass
go vet ./...
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for code style and PR process.

---

## License <a id="license"></a>

[MIT](LICENSE)

---

## Acknowledgments <a id="acknowledgments"></a>

- [Bubble Tea](https://github.com/charmbracelet/bubbletea) — TUI framework
- [Lipgloss](https://github.com/charmbracelet/lipgloss) — Terminal styling
- [Ollama](https://github.com/ollama/ollama) — Local LLM runtime
