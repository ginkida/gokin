![Gokin](https://minio.ginkida.dev/minion/github/gokin.jpg)

<p align="center">
  <a href="https://github.com/ginkida/gokin/releases"><img src="https://img.shields.io/github/v/release/ginkida/gokin" alt="Release"></a>
  <a href="https://github.com/ginkida/gokin/stargazers"><img src="https://img.shields.io/github/stars/ginkida/gokin" alt="Stars"></a>
  <a href="https://github.com/ginkida/gokin/releases"><img src="https://img.shields.io/github/downloads/ginkida/gokin/total" alt="Downloads"></a>
  <a href="https://github.com/ginkida/gokin/blob/main/LICENSE"><img src="https://img.shields.io/github/license/ginkida/gokin" alt="License"></a>
  <img src="https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go" alt="Go Version"></p>

<p align="center">
  <img src="https://minio.ginkida.dev/minion/github/gokin-demo.gif" alt="Gokin Demo" width="800">
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
| **Gokin + Kimi Coding Plan** | Subscription | K3: up to 1M context; K2.7: 256K, thinking + tool use |
| **Gokin + GLM Coding Plan** | ~$3/month | **Default** — GLM-5.2, 1M context, extended thinking |
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
- **Optional:** a recent `gopls` with MCP support for managed Go symbol
  search, references, and immediate post-edit diagnostics. Gokin detects it
  automatically and falls back to bounded AST/text search when unavailable.

---

## Quick Start <a id="quick-start"></a>

```bash
# Interactive setup — picks provider + API key
gokin --setup

# Or set an API key and go
export GOKIN_DEEPSEEK_KEY="sk-..."   # DeepSeek V4 (recommended)
gokin

# Other providers work the same way:
# export GOKIN_KIMI_KEY="sk-kimi-..."   # Kimi Coding Plan
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

### Automation and CI

Run one request without the TUI with `-p` (`--print`) or `--headless`.
The prompt may be positional, passed with `--prompt`, or read from stdin:

```bash
gokin -p "summarize the current repository"
gokin --headless --prompt "run the targeted tests and fix failures"
git diff | gokin -p "review this patch"
printf 'explain this panic\n' | gokin --headless

# Start the interactive TUI and immediately submit the first task.
gokin "inspect the failing tests and propose a fix"
```

For scripts, `json` emits one terminal result. `stream-json` emits newline
delimited progress records while the agent works (tool start/result, progress,
answer deltas), followed by exactly one terminal `result` record:

```bash
gokin -p "run the test suite" --output-format json
gokin -p "one-off review" --no-session-persistence
gokin -p "run the test suite" --max-turns 40 --timeout 45m \
  --max-budget-usd 2.50 \
  --output-format stream-json |
  jq --unbuffered -c .

# Fast, isolated CI startup: expose only Read, Edit, and Bash and skip
# project instructions, skills, hooks, plugins, MCP, memory, custom commands,
# agent discovery, file watching, auto-resume, and update checks.
gokin -p --bare "apply the focused fix and run the targeted test" \
  --permission-mode dontAsk \
  --allowedTools "Read,Edit,Bash(go test *)"

# Capture diagnostics without contaminating stdout. An explicit file also
# implicitly enables debug mode.
gokin -p "reproduce the failure" --output-format json \
  --debug-file /tmp/gokin-debug.jsonl

# Optional category filters support positive and negative terms.
gokin --debug "api,mcp,!health"

# Validate the agent's final value locally and expose it as structured_output.
gokin -p "summarize test health" --output-format json \
  --json-schema '{"type":"object","properties":{"passing":{"type":"boolean"},"failures":{"type":"array","items":{"type":"string"}}},"required":["passing","failures"],"additionalProperties":false}'

# Read-only unattended review: omitted tools are hidden and runtime-blocked.
git diff | gokin -p "review this patch" \
  --tools read,grep,git_diff,review_changes

# Keep the normal toolkit except selected capabilities.
gokin -p "inspect the repository" \
  --disallowed-tools write,edit,delete,bash,git_commit

# Pre-approve only matching calls, without exposing tools hidden by --tools.
gokin -p "inspect status and summarize it" \
  --allowedTools "Read,Bash(git status *)"

# Keep Bash available but block outward-facing pushes at runtime.
gokin -p "prepare the release locally" \
  --disallowedTools "Bash(git push *)"

# Allow ordinary file edits without prompts, while bash/SSH/commits remain
# subject to their configured permission rules.
gokin -p "apply the requested refactor" --permission-mode acceptEdits

# Locked-down CI: explicit pre-approvals run; anything that would prompt is
# denied immediately instead of waiting for an unavailable operator.
gokin -p "verify the repository" \
  --permission-mode dontAsk \
  --allowedTools "Read,Grep,Bash(go test *)"

# Fully unattended permission prompts. This does not disable Gokin's sandbox,
# workspace boundary, command safety checks, hooks, or --tools ceiling.
gokin -p "run the approved migration" \
  --permission-mode bypassPermissions

# Start read-only planning; leaving plan mode still requires approval.
gokin -p "design the migration without applying it" --permission-mode plan

# Add a CI-specific contract without replacing Gokin's generated project,
# safety, tool, and model guidance.
gokin -p "review and verify this change" \
  --append-system-prompt "Report findings as SARIF-compatible JSON."

# Replace the generated prompt and then append a second instruction. Text and
# file variants may be combined across replace/append, but not within one role.
gokin -p "perform the requested audit" \
  --system-prompt-file ./ci/reviewer-system.md \
  --append-system-prompt-file ./ci/output-contract.md

# Give a fresh automation run a deterministic identity. The ID must be a
# canonical UUID and is refused if any persisted entry already owns it.
gokin -p "continue the CI task" \
  --session-id 67c220a6-5ba6-4d36-95bd-2df9a9f49d94 \
  --output-format json

# Detach a complete agent session. The launcher returns immediately while a
# private worker owns the session, model calls, tools, and durable logs.
gokin --bg "investigate the flaky integration test and fix it"
gokin agents
gokin agents --json --all
gokin logs 67c220a6 --follow
gokin send 67c220a6 "also verify the cancellation path"
gokin attach 67c220a6
gokin stop 67c220a6

# Branch from an existing conversation without modifying its source snapshot.
# The fork receives a fresh UUID and never inherits pending automatic retries.
gokin -p "try the alternate implementation" \
  --resume 20260730-120000-a1b2c3 --fork-session

# Long-lived JSONL session: each user record runs after the previous one.
printf '%s\n' \
  '{"type":"user","prompt":"inspect the failing tests"}' \
  '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"apply the fix and verify it"}]}}' |
  gokin -p --input-format stream-json --output-format stream-json
```

Redirected stdin is bounded to 16 MiB; a live terminal is never read by
headless text mode. Stream input accepts one user JSON object per line, bounds
each record to 16 MiB, applies backpressure by completing one turn before
reading the next, and keeps event sequence numbers monotonic across the
connection. A failed, cancelled, timed-out, policy-blocked, malformed-input,
or unpersisted turn exits non-zero and is represented in a terminal JSON
record.
`--bare` is a runtime-only, Claude-compatible isolation mode intended for CI
and one-off scripts. It constructs a physical three-tool registry (`read`,
`edit`, `bash`) and a minimal system prompt rather than constructing the full
registry and hiding it afterward, so skill/plugin constructors and project
instruction discovery never run. It also ignores saved full prompts when
resuming and sets `CLAUDE_CODE_SIMPLE=1` for Bash child processes. Provider
credentials, the sandbox, permission rules, workspace boundaries, explicit
`--add-dir`, system-prompt flags, budgets, output formats, and session saving
still work. `--tools` and deny rules may further narrow the three tools but
cannot widen them. In headless mode detached Bash remains disabled because the
process exits after the result; interactive `--bare` retains durable background
Bash task output.
`--debug [filter]` and `--debug-file <path>` write JSONL diagnostics to a file,
never to stdout. Without an explicit path, logs go to
`~/.config/gokin/debug/gokin-<timestamp>-<pid>.jsonl` (under
`XDG_CONFIG_HOME` when set). `--debug-file` takes precedence over
`GOKIN_DEBUG_LOG_FILE`, then the Claude-compatible
`CLAUDE_CODE_DEBUG_LOGS_DIR`. `GOKIN_DEBUG_LOG_FILE` names the log file itself;
`CLAUDE_CODE_DEBUG_LOGS_DIR` names a directory that receives a generated
`gokin-<timestamp>-<pid>.jsonl` file, and an explicit `--debug-file` that points
at an existing directory is treated the same way. The environment variables
select a destination but do not enable debug on their own. `GOKIN_DEBUG_LOG_LEVEL` and
`CLAUDE_CODE_DEBUG_LOG_LEVEL` accept `verbose`, `debug`, `info`, `warn`, or
`error`. Files are created with mode `0600`, rotate after 10 MiB, and pass
through centralized secret redaction even when callers use the raw logger.
Positive filter terms retain matching messages, attribute keys, or explicit
`category`/`component`/`subsystem` values; `!term` excludes matches. Headless
JSON/stream-JSON output remains machine-clean,
while early configuration failures and final lifecycle status are still
recorded.
`--json-schema '<schema>'` works with `json` and `stream-json` output. Gokin
compiles the self-contained Draft 2020-12 schema before contacting a provider,
adds the contract only to the invocation's live system prompt, and validates
the exact final JSON value locally. The terminal envelope exposes that value
as `structured_output`. Invalid output gets up to two tool-free format
corrections under the same timeout and cost ledger, then fails closed with
`error.kind: "structured_output"`. Schemas are limited to 64 KiB; external
file/network `$ref` values are rejected (use local `$defs`). The schema is
never written into the persisted session system prompt.
`--session-id <uuid>` replaces the generated identity only for a fresh
session. It cannot be combined with `--resume`, `--continue`, or
`--fork-session`, and it never overwrites an existing, corrupt, or unreadable
session entry. `--fork-session` requires `--resume` or `--continue`, briefly
locks the source while taking a consistent snapshot, atomically saves the
conversation under a fresh UUID, and then releases the source. Conversation
history, scratchpad, branches, and named checkpoints are copied; pending
retries and executor tool-checkpoint journals are deliberately cleared so the
fork cannot repeat a source session's interrupted side effect.
`--no-session-persistence` makes the process ephemeral and cannot be combined
with `--resume`, `--continue`, or `--fork-session`; a deterministic
`--session-id` remains available for ephemeral JSON automation.
`--background` (`--bg`) starts a detached headless session and returns its
durable job ID immediately. The worker runs in a separate process group,
forces stream-JSON output into private `0600` logs, persists non-secret control
metadata atomically, and holds an OS file lease for its entire lifetime.
`gokin agents [--json] [--all] [--cwd <dir>]` works from another process;
`gokin logs <id> --follow` streams stdout/stderr; and `gokin stop <id>` signals
only a PID whose matching worker lease is still held, preventing stale PID
metadata from killing an unrelated process. UUID prefixes are accepted when
unambiguous. Crashed or externally killed workers are reconciled to
`interrupted`, while an explicitly stopped worker becomes `stopped`.
`gokin send <id> <message>` writes a bounded private control record. The worker
atomically claims it and steers it into the active model/tool loop; if the
steering window has already closed, it becomes the next synchronous turn with
the same App and session. `gokin attach <id>` combines live JSONL/stderr
following with line-oriented input (`/detach` leaves the worker running).
`gokin respawn <id> <prompt>` continues a completed job's exact persisted
session from its original working directory as a new detached job. The new job
records only the parent job ID, not either prompt or launch arguments. It
inherits explicitly supplied runtime flags, restores the session's provider
when no override was supplied, and supports `--fork-session` when the caller
wants a new history identity. Live jobs, busy session leases, provider
mismatches, and jobs with unresolved pending/ambiguous input fail closed.
Inbox admission and worker completion share one metadata lock, so a message
cannot be accepted after the worker's final empty-inbox proof. Claimed input is
removed only after its turn commits. A crash in the claim/delivery gap is
reported by `agents` as `ambiguous_input` and is never replayed automatically;
pending-but-unclaimed messages are reported separately.
Detached sessions require persistence and an explicit text prompt; they cannot
be combined with `--print`, `--headless`, piped stream input, or the setup
wizard.
`gokin doctor` checks the selected provider's authentication contract, config
path, git/GitHub CLI availability, repository state, instruction discovery,
and data directory without constructing a provider client or starting the TUI.
This makes malformed configuration and pre-startup failures diagnosable;
`--provider`, `--model`, `--base-url`, and `--config` remain runtime-only
overrides for the check. Ollama is correctly reported as key-optional, while a
key belonging only to a different provider no longer masks missing active
credentials. The in-session `/doctor` command uses the same renderer.
`--system-prompt` / `--system-prompt-file` replace the generated instruction
for this invocation; `--append-system-prompt` /
`--append-system-prompt-file` extend it. A replacement and an appendix can be
used together, with the appendix applied last. The text/file variants for the
same role are mutually exclusive, files must be valid UTF-8 without NUL bytes,
and all custom prompt input is bounded to 64 KiB combined. These instructions
survive plan-mode changes, provider failover, session resume, headless
multi-record input, and delegated work. They are deliberately never written
to resumable session state; the session retains only Gokin's canonical prompt.
`--max-turns 0` adds no turn cap, `--timeout 0` adds no overall deadline, and
`--max-budget-usd 0` disables the cost ceiling. A positive cost ceiling uses
the accumulated price of every provider round, stops pending tools before
they can produce side effects, and fails closed before the first request when
the selected model has no explicitly maintained tariff. Foreground execution,
delegated `task` agents, retries, planning, summarization, and semantic
reflection share one atomic ledger; budgeted provider rounds are serialized so
parallel agents cannot independently race past the same remaining allowance.
Hitting an explicit turn cap is a typed `max_turns` failure rather than a
successful-but-incomplete response. Interactive turns retain Gokin's adaptive
safety budget, whose iteration limit ends a turn with an incomplete-work notice
rather than a typed failure — the hard `max_turns` contract belongs to
`--max-turns`.

All three limits are **per turn**, not per connection. A single `-p` run has
exactly one turn, so there the distinction does not arise; but every record of a
`--input-format stream-json` session and every `gokin send` follow-up on a
detached job starts a fresh deadline, turn count and cost ledger. Budget a
long-lived detached session by the work you expect one turn to do, and cap the
whole session from the outside.

`--tools` is an exact capability allowlist. `--allowedTools` (also
`--allowed-tools`) pre-approves matching calls but never widens that allowlist.
`--disallowedTools` (also `--disallowed-tools`) installs run-wide deny rules:
bare names and name wildcards are also hidden from the model, while
argument-scoped rules such as `Bash(git push *)` leave the tool visible and
block only matching calls. Scoped rules also support gitignore-style
`Read(/src/**)` and `Edit(/generated/**)` paths,
`WebFetch(domain:example.com)`, and `Agent(Explore)`.
Path rules use the exact foreground or isolated-agent worktree and check both
symlink and resolved targets. Explicit denies win over pre-approvals and
`bypassPermissions`, and all rules are inherited by delegated agents. Unknown
bare names fail startup so a typo cannot silently weaken an unattended run.
MCP tools are registered under Gokin's own `<server>_<tool>` naming, so a deny
rule can target them directly as `github_*` or by exact name. Claude-style
`mcp__*`, `mcp__<server>`, `mcp__<server>__*`, and `mcp__<server>__<tool>` rules
resolve onto that naming as well, and match only tools an MCP server actually
registered — a built-in that happens to share a prefix is never in range.

A scoped `Bash(...)` pre-approval covers only the command it names: its
wildcards never expand across `|`, `&`, `;`, a newline, a redirect, or a
command substitution, so `Bash(git status *)` cannot carry
`&& curl … | sh` in behind it. A scoped Bash **deny** is matched against the
whole command line *and* every individual segment of it, so prefixing or
chaining (`cd . && git push`) cannot walk past it.
`--permission-mode` is a run-only override with `default`, `acceptEdits`,
`dontAsk`, `bypassPermissions`, and `plan` modes. `dontAsk` executes explicit
configuration/CLI/skill pre-approvals and conservative read-only Bash calls,
but immediately denies anything that would otherwise open a prompt. This makes
locked-down CI deterministic without granting the broad authority of
`bypassPermissions`. The Claude-compatible
`--dangerously-skip-permissions` flag aliases `bypassPermissions`; despite its
name, Gokin continues enforcing sandbox, path-boundary, hard command-safety,
hook, and tool-capability controls. When the flag is omitted, the configured
permission and plan modes are preserved.

---

## Key Features <a id="features"></a>

### Code Understanding
- **Multi-file analysis** — grep + glob + read across the whole codebase
- **Managed Go intelligence** — automatically uses `gopls mcp` for workspace
  symbols, references, and post-edit diagnostics when installed
- **Session memory** — auto-summarizes files, tools, errors, decisions; survives compaction
- **Context-aware execution** — read-only tools run in parallel, write tools serialized

### Reusable Skills

Gokin discovers `SKILL.md` workflows from `.gokin/skills/`,
`.claude/skills/`, `~/.config/gokin/skills/`, and `~/.claude/skills/`.
Skill bodies load on demand and their exact rendered instructions survive
context compaction. Claude-compatible `allowed-tools` and `disallowed-tools`
metadata accept a space-separated string or YAML list, including scoped rules
such as `Bash(git status *)`, `Read(/src/**)`,
`WebFetch(domain:example.com)`, and `Agent(Explore)`.

Allowed entries pre-approve matching tools only for the request in which the
skill is invoked; denied entries block matching calls for that request and win
over pre-approvals and permission-bypass mode. Neither form persists into the
next request, exposes hidden tools, bypasses hooks/path boundaries, or
suppresses the confirmation floor for elevated shell commands. User-level
skills are trusted. Repository-owned skills activate authority-expanding
`allowed-tools` only when the exact workspace is listed in the user config
under `hooks.trusted_workspaces`, the same trust boundary used for project
executable hooks. Restrictive `disallowed-tools` rules do not require trust.

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
- `cache_control` breakpoints for Kimi, MiniMax, and DeepSeek — Kimi cache hits are priced at 10–20% of ordinary input
- System prompt, tools, and conversation prefix cached

### Extended Thinking
- Full multi-turn support for Kimi / GLM / DeepSeek reasoning models
- Thinking blocks with `signature` preserved across turns, including tool calls

### Safety & Permissions
- **3-level permissions**: Low (auto), Medium (ask once), High (always ask)
- **Sandbox mode** for bash commands
- **Inline diff preview** — 3-line preview cards before applying changes
- **Undo/Redo** for all file operations (`/undo N` up to 20 steps)
- **Plan-scoped undo/redo** — `undo_plan` and `redo_plan` target only the exact
  tracked changes from the latest executed plan and refuse conflicting or
  incomplete history instead of touching unrelated work
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
| **Kimi** | `k3`, `kimi-for-coding`, `kimi-for-coding-highspeed` | K3: up to 1M; K2.7: 256K | K3 $3/$15, K2.7 $0.95/$4 |
| **GLM** | `glm-5.2`, `glm-5.1`, `glm-5`, `glm-5-turbo`, `glm-4.7`, `glm-4.5` | 5.2: 1M input, 131K output | 5.2/5.1: $4/$16, 5: $1/$4 |
| **MiniMax** | `MiniMax-M2.7`, `M2.7-highspeed`, `M2.5`, `M2.5-highspeed` | 200K input, 16K output | M2.7: $0.30/$1.20 |
| **Ollama** | Any local model | Varies | Free |

All cloud providers use Anthropic-compatible APIs and share the same client — fewer moving parts, consistent behavior. GLM uses server-managed implicit prefix caching; Kimi, MiniMax, and DeepSeek use explicit prompt caching markers. Ollama uses its own native client and makes zero network calls.

GLM Coding Plan users can enable the first-party `web_search_prime` tool with `/set glmsearch on`. It reuses `GOKIN_GLM_KEY`, `GLM_API_KEY`, or `api.glm_key`; no second credential is required. This boot-wired setting takes effect after restart.

Switch anytime:
```
/provider deepseek    →  /model deepseek-v4-pro
/provider kimi        →  /model k3
/provider glm         →  /model glm-5.2
/provider minimax     →  /model MiniMax-M2.7
/provider ollama      →  /model llama3.2
```

Kimi's prompt cache is model-specific. After switching between `k3`, Standard,
and HighSpeed, start a fresh session with `/clear` to avoid re-prefilling a long
history under the new model.

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
| `/settings` | Open interactive settings |
| `/set <key> <on\|off>` | Change a setting directly |
| `/shortcuts` | Keyboard shortcuts reference |
| `/help` | Show all 65 commands |

**Aliases:** `p`=plan, `c`=commit, `m`=model, `s`=status, `u`=undo, `r`=redo, `h`=help, `q`=clear, `st`=stats, `pr`=pr

### Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Ctrl+C` | Interrupt / cancel |
| `Ctrl+S` | Open settings |
| `Ctrl+K` | Model selector |
| `Ctrl+E` | Expand/collapse tool output |
| `Ctrl+P` | Command palette |
| `Shift+Tab` | Cycle Normal → Plan → YOLO |
| `Ctrl+T` / `Ctrl+O` | Task list / live activity |
| `Alt+C` | Copy last response |
| `?` | Searchable shortcuts overlay |
| `Tab` | Autocomplete |
| `↑/↓` | History |
| `y/n` | Accept/reject diff |

---

## Configuration <a id="configuration"></a>

`~/.config/gokin/config.yaml`

Use `--config /path/to/config.yaml` to load an explicit file. An explicit
file must already exist; runtime configuration saves are written back to that
same file rather than the default location.

### Minimal

```yaml
# DeepSeek (recommended)
api: { deepseek_key: "sk-...", active_provider: "deepseek" }
model: { name: "deepseek-v4-pro" }

# Or Kimi
api: { kimi_key: "sk-kimi-...", active_provider: "kimi" }
model: { name: "kimi-for-coding" }

# Or GLM / MiniMax
api: { glm_key: "...", active_provider: "glm" }
model: { name: "glm-5.2" }
```

### Full Reference

```yaml
api:
  kimi_key: ""
  deepseek_key: ""
  glm_key: ""
  minimax_key: ""
  ollama_key: ""                  # optional, for remote Ollama with auth
  active_provider: "glm"          # glm | deepseek | kimi | minimax | ollama
  ollama_base_url: "http://localhost:11434"
  retry:
    max_retries: 10
    http_timeout: 0s               # 0 = provider-specific first-header default
    stream_idle_timeout: 0s        # 0 = provider-specific SSE pause default

model:
  name: "glm-5.2"
  temperature: 0.6
  max_output_tokens: 65536         # headroom below GLM-5.2's 131K API cap
  custom_base_url: ""             # override endpoint
  thinking_mode: "auto"           # auto | on | off; adapts per request
  enable_thinking: false          # legacy static switch
  thinking_budget: 0              # 0 = adaptive/provider default

engine:
  # auto probes a real OS sandbox and enables the stateful hybrid engine only
  # when isolation succeeds; otherwise Gokin transparently uses normal tools.
  # tools disables the REPL; hybrid fails startup if a secure runtime is absent.
  mode: "auto"                    # auto | tools | hybrid
  repl:
    cell_timeout: 30s             # Python compute inactivity; pauses for callbacks
    max_code_bytes: 65536
    max_response_bytes: 1048576

ui:
  stream_output: true              # compatibility field; responses always stream
  markdown_rendering: true         # /set markdown off
  show_tool_calls: true            # /set toolcalls off hides transcript rows
  show_token_usage: true           # /set tokens off
  theme: "dark"                    # Graphite + violet (only active theme)
  show_welcome: true
  hints_enabled: true              # /set hints off; composer + status-bar tips
  compact_mode: false              # /set compactui on
  reduced_motion: false            # /set reducedmotion on
  bell: true                       # /set bell off
  native_notifications: false      # /set nativealerts on (macOS)

tools:
  timeout: 2m
  # Hard cap per provider round, shared by foreground and sub-agents.
  # Use 0 to restore the default; reasoning-heavy GLM rounds need a generous cap.
  # Context/session summaries inherit this cap. Normal/thorough agent, plan,
  # coordinate, MetaAgent, and UI watchdogs leave extra
  # headroom; shorter explicit quick/plan/coordinate/loop/headless budgets win.
  model_round_timeout: 14m
  bash: { sandbox: true }

permission:
  enabled: true
  default_policy: "ask"           # allow | ask | deny

plan:
  enabled: true
  require_approval: true
  planning_timeout: 0s            # 0 = follow tools.model_round_timeout
  default_step_timeout: 0s        # 0 = dynamic model round + agent headroom

memory:
  enabled: true
  max_entries: 1000
  auto_inject: true
  allow_global: false              # opt in to user-wide cross-project memory

mcp:
  enabled: false                  # enable MCP server support
  servers: {}                     # server configs (stdio/http)
```

Change the model-round cap live with `/timeout 20m`; `/timeout default`
restores the recommended value for foreground and sub-agent requests.

In the default `engine.mode: auto`, a successful sandbox probe exposes a
persistent, workspace-read-only Python `repl_exec`. Its `context` object keeps
large repository analysis out of the model transcript, while `rlm()` delegates
bounded work through the existing permission and audit pipeline. The optional
`rlm.harness` surface can add session-only prompt adjustments, project episodic
memory, and inert skill proposals. Harness mutations require approval by
default; proposals remain under `.gokin/harness/proposals/` and are never
auto-loaded from `.gokin/skills/`. Python cannot change permissions, sandbox
policy, built-in tools, or immutable system instructions. Direct Python file
writes, subprocesses, sockets, and native-library loading are denied; resource
limits and the OS sandbox remain the hard boundary. `context.git_status()` and
`context.git_diff()` use the one fixed read-only subprocess path.

Use `repl_exec` with `action: status` to inspect bounded kernel health
(generation, restarts, executions, transport failures, and timeouts), or
`action: reset` to discard Python globals/artifacts deliberately. Protocol
failures and inactive cells discard the affected kernel automatically; the next
cell starts a clean generation. Episodic-memory writes use an advisory lock plus
atomic snapshots, so concurrent Gokin terminals merge updates instead of
silently overwriting one another.

Open the interactive settings screen with `Ctrl+S` or `/settings`. Interface
toggles such as `hints`, `toolcalls`, `tokens`, `compactui`, and
`reducedmotion`, `markdown`, `bell`, and `nativealerts` apply immediately;
`/set` marks restart-only settings explicitly.

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
│   ├── repl/           # Sandboxed stateful Python + typed callback protocol
│   ├── harness/        # Bounded continual memory and inert skill proposals
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
