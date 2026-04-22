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

<h3 align="center">🤖 AI-powered coding assistant for your terminal<br>Kimi · GLM · MiniMax · Ollama — 100% open source</h3>

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

## ✨ Why Gokin? <a id="why-gokin"></a>

Most AI coding tools are closed-source, route your code through third-party servers, and give you zero control over what gets sent to the model. Gokin was built with a different goal: **a fast, secure, zero-telemetry CLI where your code goes directly to the provider you chose — and nothing else leaves your machine.**

Gokin focuses on a small, well-tested set of providers: **Kimi, GLM, MiniMax** (via Anthropic-compatible APIs) and **Ollama** (fully local). Secrets, credentials, and sensitive code are automatically redacted before reaching any model, TLS is enforced on every connection, and no proxy or middleware ever touches your data.

| Feature | Gokin | Claude Code | Cursor |
|---------|-------|-------------|--------|
| **Price** | Free → Pay-per-use | $20+/month | $20+/month |
| **Providers** | 4 (Kimi, GLM, MiniMax, Ollama) | 1 (Claude) | Multi |
| **Offline** | ✅ Ollama | ❌ | ❌ |
| **54 Tools** | ✅ | ~30 | ~30 |
| **Multi-agent** | ✅ 5 parallel | Basic | ❌ |
| **Direct API** | ✅ Zero proxies | ✅ | ❌ Routes through Cursor servers |
| **Security** | ✅ TLS 1.2+, secret redaction (24 patterns), sandbox, 3-level permissions | Basic | Basic |
| **Open Source** | ✅ | ❌ | ❌ |
| **Self-hosting** | ✅ | ❌ | ❌ |

**Choose your stack:**

| Stack | Cost | Best For |
|-------|------|----------|
| **Gokin + Kimi Coding Plan** ⭐ | Subscription | **Default** — Kimi K2.6, 262K context, thinking + tool use, coding-tuned |
| **Gokin + GLM Coding Plan** ⭐ | ~$3/month | Budget-friendly daily coding, GLM-5/5.1 with thinking |
| **Gokin + MiniMax** ⭐ | Pay-per-use | 200K context, strong on agentic coding |
| **Gokin + Ollama** | 🆓 Free | Privacy, offline, no API costs |

All three cloud providers are actively recommended — they're the daily-driver tier gokin is tested against every release.

---

## 🚀 Installation <a id="installation"></a>

### One-liner (recommended)

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

## ⚡ Quick Start <a id="quick-start"></a>

```bash
# Launch with interactive setup (picks provider + key)
gokin --setup

# Or set an API key and run — Kimi Coding Plan is the v0.69+ default
export GOKIN_KIMI_KEY="sk-kimi-..."
gokin

# Prefer another provider? Any of these also works out of the box:
# export GOKIN_GLM_KEY="..."      # GLM Coding Plan
# export GOKIN_MINIMAX_KEY="..."  # MiniMax
# (Ollama needs no key — just run a local model)
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

## 🎯 Key Features <a id="features"></a>

### 🧠 Smart Code Understanding
- **Multi-file Analysis** — Understand entire modules via grep + glob + read
- **Session Memory** — Auto-summarizes your session (files, tools, errors, decisions). Survives context compaction. Optional LLM-based summarization every 3rd extraction.
- **Context-aware agents** — Read-only tools run in parallel, write tools serialized

### 📝 Multi-Layer Project Instructions
```
Priority:  Low ──────────────────────────────── High
           Global → User → Project → Local

Global:    ~/.config/gokin/GOKIN.md
User:      ~/.gokin/GOKIN.md
Project:   ./GOKIN.md, .gokin/rules/*.md
Local:     ./GOKIN.local.md (git-ignored)
```
- All layers merged automatically
- `@include` directive: `@./path`, `@~/path`, `@/absolute/path`
- File watching with auto-reload on changes

### ⚒️ 54 Built-in Tools
- **Files**: read, write, edit, diff, batch, copy, move, delete
- **Search**: glob, grep, tree
- **Git**: status, commit, diff, branch, log, blame, PR
- **Run**: bash, run_tests, ssh, env
- **Plan**: todo, task, enter_plan_mode, coordinate
- **Memory**: memorize, shared_memory, pin_context
- **MCP servers**: add your own via `/mcp add` (Model Context Protocol, stdio + http transports, per-server permissions)
- **Parallel execution**: Read-only tools (read, grep, glob) run in parallel when model calls multiple

### 🤝 Multi-Agent System
```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   Explore  │────▶│   General   │────▶│    Bash    │
│  (read)    │     │   (write)   │     │  (execute) │
└─────────────┘     └─────────────┘     └─────────────┘
       │                   │                   │
       └───────────────────┴───────────────────┘
                         │
                  [Progress UI]
```
- Up to 5 parallel agents
- Shared memory between agents
- Automatic task decomposition
- **API retry with exponential backoff** — agents survive transient API errors (rate limits, timeouts, 500s)
- **Provider failover** — agents automatically try fallback providers when primary fails
- **Real-time streaming** — agent output streamed to UI as it's generated
- **Git worktree support** — parallel branch work with isolated sessions

### 💰 Cost Tracking
- Per-model pricing for Kimi, GLM, MiniMax models (Ollama is free)
- Real-time cost in status bar (`$0.0243`)
- Per-response cost in message footer
- `/cost` and `/stats` commands with accurate model-specific pricing

### 🔄 Prompt Caching
- Explicit `cache_control` breakpoints for Kimi, MiniMax, and GLM (all Anthropic-compat providers)
- System prompt, tools, and conversation prefix cached — up to 90% input cost savings
- Cache break detection with efficiency tracking

### 🧠 Extended Thinking with Tools
- Full multi-turn support for Kimi K2.6 / GLM / Anthropic-style reasoning
- Thinking blocks (with `signature`) preserved across turns, including tool calls
- Signature-aware history reconstruction — no "reasoning_content missing" errors

### 🛡️ Safety & Permissions
- **3-level permissions**: Low (auto), Medium (ask once), High (always ask)
- **Sandbox mode** for bash commands
- **Diff preview** before applying changes (single-file and multi-file)
- **Undo/Redo** for all file operations
- **Audit logging**
- **Proactive context compaction** — predicts token growth and compacts before hitting model limits

---

## 🔒 Security & Privacy <a id="security"></a>

### Zero Proxies — Your Code Goes Nowhere Except the LLM

```
┌──────────┐          ┌──────────────────────┐
│  Gokin   │ ──TLS──▶ │  Provider API        │
│  (local) │          │  (Kimi / Z.AI / ...) │
│          │ ◀──TLS── │                      │
└──────────┘          └──────────────────────┘

No middle servers. No Vercel. No telemetry proxies.
Your API key, your code, your conversation — direct.
```

Some CLI tools route requests through their own proxy servers (Vercel Edge, custom gateways) for telemetry, analytics, or API key management. **Gokin does none of this.** Every API call goes directly from your machine to the provider's endpoint. You can verify this — it's open source.

### Secret Redaction in Terminal Output

LLM tool calls can accidentally expose secrets found in your codebase. Gokin automatically redacts them **before** they reach the model or your terminal:

| Category | Examples |
|----------|----------|
| API keys | `AKIA...`, `ghp_...`, `sk_live_...`, `AIza...` |
| Tokens | Bearer tokens, JWT (`eyJ...`), Slack/Discord tokens |
| Credentials | Database URIs (`postgres://user:pass@...`), Redis, MongoDB |
| Crypto material | PEM private keys, SSH keys |

24 regex patterns, applied to every tool result and audit log. Handles any data type — strings, maps, typed slices, structs. Custom patterns supported via API.

### Defense in Depth

| Layer | What it does |
|-------|-------------|
| **TLS 1.2+ enforced** | No weak ciphers, certificate verification always on |
| **Sandbox mode** | Bash runs in isolated namespace (Linux), safe env whitelist (~35 vars) — API keys never leak to subprocesses |
| **Command validation** | 50+ blocked patterns: fork bombs, reverse shells, `rm -rf /`, credential theft, env injection |
| **SSH validation** | Host allowlist, loopback blocked, username injection prevention |
| **Path validation** | Symlink resolution, directory traversal blocked, TOCTOU prevention |
| **SSRF protection** | Private IPs, loopback, link-local blocked; all DNS results checked |
| **Audit trail** | Every tool call logged with sanitized args |

### Keys Stay Local

- API keys loaded from env vars or local config (`~/.config/gokin/config.yaml`)
- Keys are **masked** in all UI displays (`sk-12****cdef`)
- Keys are **never** included in conversation history or tool results
- Ollama mode: **zero network calls** — fully airgapped

### 💾 Memory That Persists
```
> Remember we use PostgreSQL with pgx driver
> What were our database conventions?
```
- Project-specific memories
- Auto-inject relevant context
- Stored locally (your data stays yours)

---

## ☁️ Providers <a id="providers"></a>

> [!IMPORTANT]
> **Recommended providers** — daily-driver tier, tested every release:
> - **Kimi Coding Plan** (Kimi K2.6) — **default as of v0.69**, 262K context, thinking + tools
> - **GLM Coding Plan** (GLM-5 / GLM-5.1) — budget option (~$3/month), thinking supported
> - **MiniMax** (M2.7 / M2.5) — 200K context, strong on agentic coding
>
> Ollama is fully supported for offline / zero-cost workflows.

| Provider | Models | Endpoint | Notes |
|----------|--------|----------|-------|
| **Kimi** ⭐ | `kimi-for-coding` (K2.6) | `api.kimi.com/coding` | **Default.** Coding Plan subscription; 262K context, reasoning + vision + video, thinking mode |
| **GLM** ⭐ | `glm-5.1`, `glm-5`, `glm-4.7` | `api.z.ai/api/anthropic` | Budget-friendly Coding Plan, thinking mode |
| **MiniMax** ⭐ | `MiniMax-M2.7`, `M2.7-highspeed`, `M2.5`, `M2.5-highspeed` | `api.minimax.io/anthropic` | 200K context, strong on agentic coding |
| **Ollama** | Any local model (`llama3.2`, `qwen2.5-coder`, ...) | `localhost:11434` | 100% offline, no network calls |

All cloud providers use Anthropic-compatible APIs and share the same client (`internal/client/anthropic.go`) — fewer moving parts, consistent behavior. Kimi auth uses Bearer tokens; GLM and MiniMax accept both Bearer and `x-api-key`. Ollama uses its own native client.

Switch anytime:
```
> /provider kimi
> /model kimi-for-coding
> /provider glm
> /model glm-5.1
> /provider minimax
> /model MiniMax-M2.7
> /provider ollama
> /model llama3.2
```

### Moonshot Developer API (legacy)

Gokin's `kimi` provider points at Kimi Coding Plan (`api.kimi.com/coding`) by default — that's where `kimi-for-coding` lives. If you have a Moonshot Developer API key instead, set `model.custom_base_url: https://api.moonshot.ai/anthropic` in your config; gokin will route through the Developer endpoint using your key. Legacy model names (`kimi-k2.5`, `kimi-k2-thinking-turbo`, `kimi-k2-turbo-preview`) are silently migrated to `kimi-for-coding` on load — UNLESS `custom_base_url` is set, in which case gokin respects the name you picked.

---

## ⌨️ Commands <a id="commands"></a>

| Command | Description |
|---------|-------------|
| `/login <provider> <key>` | Set API key |
| `/provider <name>` | Switch provider |
| `/model <name>` | Switch model |
| `/mcp [list\|add\|status\|remove]` | Manage MCP servers (Model Context Protocol) |
| `/plan` | Enter planning mode |
| `/save` / `/load` | Session management |
| `/commit [-m "msg"]` | Git commit |
| `/pr --title "..."` | Create GitHub PR |
| `/undo [N]` | Undo last N file changes (max 20) |
| `/stats` | Session statistics incl. per-provider token/cost |
| `/theme` | Switch UI theme |
| `/help` | Show all commands (55+ available) |

### Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Ctrl+C` | Interrupt |
| `Ctrl+P` | Command palette |
| `↑/↓` | History |
| `Tab` | Autocomplete |
| `?` | Show help |

---

## ⚙️ Configuration <a id="configuration"></a>

**Location:** `~/.config/gokin/config.yaml`

### Minimal (v0.69+ defaults — Kimi Coding Plan)

```yaml
api:
  kimi_key: "sk-kimi-..."
  active_provider: "kimi"
model:
  name: "kimi-for-coding"
```

Prefer GLM or MiniMax? Just swap:

```yaml
# GLM
api: { glm_key: "...", active_provider: "glm" }
model: { name: "glm-5" }

# MiniMax
api: { minimax_key: "...", active_provider: "minimax" }
model: { name: "MiniMax-M2.7" }
```

### Full Reference

```yaml
api:
  kimi_key: ""                    # Kimi Coding Plan key (sk-kimi-...)
  glm_key: ""
  minimax_key: ""
  ollama_key: ""                  # optional, only for remote Ollama with auth
  active_provider: "kimi"         # kimi | glm | minimax | ollama
  ollama_base_url: "http://localhost:11434"
  retry:
    max_retries: 10
    retry_delay: 1s
    http_timeout: 120s
    stream_idle_timeout: 30s
    providers:
      kimi:
        http_timeout: 5m
        stream_idle_timeout: 120s
      glm:
        http_timeout: 5m
        stream_idle_timeout: 180s
      minimax:
        http_timeout: 5m
        stream_idle_timeout: 120s

model:
  name: "kimi-for-coding"          # Kimi K2.6 (Coding Plan)
  temperature: 0.6
  max_output_tokens: 32768
  custom_base_url: ""              # override endpoint (e.g. Moonshot Dev API)
  enable_thinking: false           # Extended thinking — supported on Kimi, GLM, MiniMax
  thinking_budget: 0               # 0 = provider default (Kimi, GLM 4.7+/5.x)
  force_weak_optimizations: false  # opt Strong-tier models into weak-tier safeguards

tools:
  timeout: 2m
  model_round_timeout: 5m
  bash:
    sandbox: true
  allowed_dirs: []

permission:
  enabled: true
  default_policy: "ask"           # allow, ask, deny

plan:
  enabled: true
  require_approval: true

ui:
  theme: "dark"                   # dark, macos, light
  stream_output: true
  markdown_rendering: true
```

---

## 🏗️ Architecture <a id="architecture"></a>

```
gokin/
├── cmd/gokin/          # CLI entry point
├── internal/
│   ├── app/            # Orchestrator & message loop
│   ├── agent/          # Multi-agent system
│   ├── client/         # AnthropicClient (compat: Kimi/GLM/MiniMax) + OllamaClient
│   ├── tools/          # 54 built-in tools
│   ├── mcp/            # MCP (Model Context Protocol) client + manager
│   ├── ui/             # Bubble Tea TUI
│   ├── config/         # YAML config
│   ├── permission/     # 3-level security + per-MCP-server isolation
│   ├── memory/         # Persistent memory
│   └── ...
```

**~120K LOC • 100% Go • Production-ready**

---

## 🤝 Contributing <a id="contributing"></a>

Contributions welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for:

- Development setup
- Code style guide
- Pull request process

```bash
# Dev setup
git clone https://github.com/ginkida/gokin.git
cd gokin
go mod download
go build -o gokin ./cmd/gokin

# Test
go test -race ./...

# Format
go fmt ./...
go vet ./...
```

---

## 📝 License <a id="license"></a>

[MIT](LICENSE) — Use freely, modify, distribute.

---

## 🙏 Acknowledgments <a id="acknowledgments"></a>

- [Bubble Tea](https://github.com/charmbracelet/bubbletea) — TUI framework
- [Lipgloss](https://github.com/charmbracelet/lipgloss) — Terminal styling
- [Ollama](https://github.com/ollama/ollama) — Local LLM runtime

---

<p align="center">
  <sub>Made with ❤️ by developers, for developers</sub>
</p>
