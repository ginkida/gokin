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

<h3 align="center">🤖 AI-powered coding assistant for your terminal<br>Multi-provider • Multi-agent • 100% Open Source</h3>

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

This matters especially when you work with multiple LLM providers across different jurisdictions (DeepSeek, GLM, Kimi, MiniMax, Gemini, Claude). Gokin ensures that secrets, credentials, and sensitive code are automatically redacted before reaching any model, TLS is enforced on every connection, and no proxy or middleware ever touches your data. You pick the provider — Gokin handles the rest.

| Feature | Gokin | Claude Code | Cursor |
|---------|-------|-------------|--------|
| **Price** | Free → Pay-per-use | $20+/month | $20+/month |
| **Providers** | 7 (Gemini, Claude, DeepSeek, GLM, Kimi, MiniMax, Ollama) | 1 (Claude) | 1 (Claude) |
| **Offline** | ✅ Ollama | ❌ | ❌ |
| **52 Tools** | ✅ | ~30 | ~30 |
| **Multi-agent** | ✅ 5 parallel | Basic | ❌ |
| **Direct API** | ✅ Zero proxies | ✅ | ❌ Routes through Cursor servers |
| **Security** | ✅ TLS 1.2+, secret redaction (24 patterns), sandbox, 3-level permissions | Basic | ❌ |
| **Open Source** | ✅ | ❌ | ❌ |
| **Self-hosting** | ✅ | ❌ | ❌ |

**Choose your price tier:**

| Stack | Cost | Best For |
|-------|------|----------|
| **Gokin + Ollama** | 🆓 Free | Privacy, offline, no API costs |
| **Gokin + Gemini Flash** | 🆓 Free tier | Fast iterations, prototyping |
| **Gokin + DeepSeek** | ~$1/month | Daily coding, best value |
| **Gokin + Kimi** | Pay-per-use | Fast reasoning, 256K context |
| **Gokin + MiniMax** | Pay-per-use | 1M context, strong coding |
| **Gokin + Claude** | Pay-per-use | Complex reasoning |

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
# Launch with interactive setup
gokin --setup

# Or set API key and run
export GEMINI_API_KEY="your-key"
gokin
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
- **Semantic Search** — Find code by meaning, not just keywords
- **Code Graph** — Dependency visualization
- **Multi-file Analysis** — Understand entire modules

### ⚒️ 52 Built-in Tools
- **Files**: read, write, edit, diff, batch
- **Search**: glob, grep, semantic_search, tree
- **Git**: status, commit, diff, branch, PR
- **Run**: bash, run_tests, ssh
- **Plan**: todo, task, enter_plan_mode
- **Memory**: memorize, shared_memory

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

### 🛡️ Safety & Permissions
- **3-level permissions**: Low (auto), Medium (ask once), High (always ask)
- **Sandbox mode** for bash commands
- **Diff preview** before applying changes
- **Undo/Redo** for all file operations
- **Audit logging**

---

## 🔒 Security & Privacy <a id="security"></a>

### Zero Proxies — Your Code Goes Nowhere Except the LLM

```
┌──────────┐          ┌──────────────────┐
│  Gokin   │ ──TLS──▶ │  Provider API    │
│  (local) │          │  (Gemini/Claude/  │
│          │ ◀──TLS── │   DeepSeek/etc.) │
└──────────┘          └──────────────────┘

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

| Provider | Models | Auth | Notes |
|----------|--------|------|-------|
| **Gemini** | 2.5-pro, 2.5-flash, 3-pro | API key / OAuth | Free tier, native tools |
| **Anthropic** | Opus 4, Sonnet 4, Haiku | API key | Best reasoning |
| **DeepSeek** | Chat, Reasoner | API key | Best price/quality |
| **Kimi** | K2.5, K2 Thinking Turbo, K2 Turbo | API key | Fast reasoning, 256K context |
| **MiniMax** | M2.5 | API key | 1M context, strong coding |
| **GLM** | GLM-5, GLM-4.7 | API key | Budget option |
| **Ollama** | Any local model | None | 100% offline |

Switch anytime:
```
> /provider gemini
> /model 2.5-flash
> /provider anthropic
> /model sonnet
```

---

## ⌨️ Commands <a id="commands"></a>

| Command | Description |
|---------|-------------|
| `/login <provider> <key>` | Set API key |
| `/provider <name>` | Switch provider |
| `/model <name>` | Switch model |
| `/plan` | Enter planning mode |
| `/save` / `/load` | Session management |
| `/commit [-m "msg"]` | Git commit |
| `/pr --title "..."` | Create GitHub PR |
| `/theme` | Switch UI theme |
| `/help` | Show all commands |

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

### Minimal

```yaml
api:
  gemini_key: "your-key"
  active_provider: "gemini"
model:
  name: "gemini-3-flash-preview"
```

### Full Reference

```yaml
api:
  gemini_key: ""
  anthropic_key: ""
  deepseek_key: ""
  glm_key: ""
  kimi_key: ""
  minimax_key: ""
  active_provider: "gemini"
  ollama_base_url: "http://localhost:11434"

model:
  name: "gemini-3-flash-preview"
  temperature: 1.0
  max_output_tokens: 8192
  enable_thinking: false       # Anthropic extended thinking

tools:
  timeout: 2m
  bash:
    sandbox: true
  allowed_dirs: []

permission:
  enabled: true
  default_policy: "ask"       # allow, ask, deny

plan:
  enabled: true
  require_approval: true

ui:
  theme: "dark"               # dark, macos, light
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
│   ├── client/         # 7 API providers
│   ├── tools/          # 52 built-in tools
│   ├── ui/             # Bubble Tea TUI
│   ├── config/         # YAML config
│   ├── permission/     # 3-level security
│   ├── memory/         # Persistent memory
│   ├── semantic/       # Embeddings & search
│   └── ...
```

**~100K LOC • 100% Go • Production-ready**

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
- [Gemini API](https://github.com/google/generative-ai-go) — Google AI SDK
- [Lipgloss](https://github.com/charmbracelet/lipgloss) — Terminal styling

---

<p align="center">
  <sub>Made with ❤️ by developers, for developers</sub>
</p>
