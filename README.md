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

<h3 align="center">AI-powered coding agent for your terminal<br>GLM · DeepSeek · Ollama — 100% open source</h3>

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

Three providers, one interface: **GLM (default), DeepSeek** (via Anthropic-compatible APIs) and **Ollama** (fully local). Secrets and credentials are automatically redacted before reaching any model, TLS is enforced on every connection, and no proxy or middleware ever touches your data.

| | Gokin | Claude Code | Cursor |
|---|-------|-------------|--------|
| **Price** | Free → Pay-per-use | $20+/month | $20+/month |
| **Providers** | 3 (GLM, DeepSeek, Ollama) | 1 (Claude) | Multi |
| **Offline** | Ollama | — | — |
| **Tools** | 59 built-in + MCP | ~30 | ~30 |
| **Agents** | 5 parallel, shared memory | Basic | — |
| **Direct API** | Zero proxies | Yes | Routes through Cursor servers |
| **Security** | TLS 1.2+, secret redaction, sandbox, 3-level permissions | Basic | Basic |
| **Open Source** | Yes | — | — |

**Choose your stack:**

| Stack | Cost | Best For |
|-------|------|----------|
| **Gokin + GLM Coding Plan** | Subscription | **Default** — GLM-5.3-Flash, 1M context, low/high/max reasoning and native image input |
| **Gokin + DeepSeek** | Pay-per-use | 1M context, reasoning, implicit prompt caching |
| **Gokin + Ollama** | Free | Privacy, offline, no API costs |

Automated tests exercise provider protocol handling. Live model availability and quota limits depend on your account.

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
export GOKIN_GLM_KEY="..."          # GLM Coding Plan (default)
gokin

# Other providers work the same way:
# export GOKIN_DEEPSEEK_KEY="sk-..."    # then run gokin --provider deepseek
# Ollama needs no key — just run a local model
```

Keys supplied through environment variables apply to the current process.
Saving settings, onboarding or model preferences does not copy those keys into
the configuration file or overwrite an existing saved key. Use `/login` when
you explicitly want to save credentials for later launches.

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
# Fast agent-worker output; retain native skills, hooks, settings and MCP.
gokin -p "apply the focused fix" --no-plugins --brief --no-self-review \
  --permission-mode acceptEdits --output-format json
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
headless text mode. Stream input accepts one JSON object per line and bounds
each record to 16 MiB. User turns run in order with one history writer; the
connection can queue up to 64 user records or 16 MiB while receiving control
frames during an active turn. Event sequence numbers remain monotonic across
the connection. A failed, cancelled, timed-out, policy-blocked, malformed-input,
or unpersisted turn exits non-zero and is represented in a terminal JSON
record.

Stream input also accepts Claude SDK-style `control_request` frames with
`request_id` and `request.subtype`. Each receives a `control_response` with the
same ID, `success` plus `response`, or `error` plus an error string. Supported
requests are `initialize`, `get_binary_version`, `interrupt`, `reload_skills`
and `register_repo_root`. `interrupt` cancels the active turn and leaves the
connection available for the next one. `control_cancel_request` cancels the
matching control operation. Unknown requests return an error without becoming
model prompts. EOF drains accepted work. Pending controls are bounded to 32
requests or 16 MiB; duplicate JSON keys and in-flight request IDs are rejected.

`register_repo_root` accepts `directory` and optional `reload_claude_md`,
`reload_skills`, `reload_plugins` booleans. The resolved directory must be a
strict child of the original workspace or a launch-time `--add-dir` root.
Runtime permission grants do not widen this boundary. Duplicates, including
symlink aliases, fail without rerunning setup. Optional reloads add the root's
instructions, skills/commands and plugin/settings sources to the existing
loaders, retaining containment, workspace trust and managed policy. Successful
registration returns `directory` and any reload `warnings`; up to 128 roots
may be registered. Clear/resume drops the old conversation's registrations and
discovery roots after its SessionEnd cleanup. Bare mode rejects discovery.

Control frames use SDK envelopes; model/tool progress still uses Gokin's
existing event schema. Full Agent SDK interoperability and host-provided hook
callbacks/MCP servers remain incomplete. Initialization explicitly rejects
unsupported callback/server setup rather than acknowledging unused settings.
Headless user records resolve slash workflows against the live skill/command
catalog after startup and reloads. Prompt submission hooks run before dynamic
shell rendering. Local commands such as `/clear` execute without a model call;
commands that require an interactive modal return an error.
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

Interactive `/resume` opens a searchable list of saved sessions for the
current project. Type a topic or session ID, select with **↑/↓**, and press
**Enter** to resume; **Esc** cancels and **Ctrl+U** clears the search. The same
picker is available through the command palette. It preserves the composer
draft and does not contact the model. Explicit `/resume <id> [--force]` and
the plain session list remain available in noninteractive runtimes.

Interactive `--resume`, `--continue`, and `/resume <id>` restore the visible
conversation as well as the model's context. Use **Ctrl+O** to search saved
messages and **Tab** inside the reader for saved tool results. The composer
draft survives a session switch; a failed switch leaves the visible history
in place. Results are recovered from the retained session context, so older
outputs that were compacted remain summaries rather than reconstructed logs.

`/rewind` opens a prompt picker. Select a prompt, then restore code,
conversation, or both to **before** that prompt. Double `Esc` at an empty idle
input opens the picker; with a draft it clears the draft into input history
first. `/rewind <point-id|number> code|conversation|both` also works directly.
Conversation restoration places the original prompt back into the composer
without sending it. The last 100 prompt checkpoints persist across resume and
are copied independently by `--fork-session`. Undo/redo actions are included
in the file timeline; its retention is independent of the rolling undo stack.

The picker also offers **Summarize from here** and **Summarize up to here**.
The first replaces the selected prompt and later context with a summary, then
restores that prompt to the composer. The second summarizes earlier context
and keeps the selected prompt and later messages intact, with an empty composer.
Type optional focus instructions on a summary row, or press its number to
summarize immediately without instructions. Direct commands are
`/rewind <point> summarize-from [-- guidance]` and
`/rewind <point> summarize-before [-- guidance]`.

Original messages stay in private session archives, including after resume,
fork and checkpoint pruning. `/rewind transcript <summary-id>` displays them;
the model can page them with `read` using the `gokin://summary/<id>` reference
in the summary marker. This requires no project writes and gives child agents
no access to the parent's transcript. Summarization preserves code, active
skills, consumed hooks and delayed retry generations. Empty, longer or failed
summaries leave the conversation intact. Repeated summaries resolve original
turn boundaries through earlier summaries. Automatic/manual summarization,
incremental and emergency compaction, and old tool-output cleanup also archive
the replaced originals before publishing context changes. Selected boundaries
continue to resolve after these operations, resume and fork, including when
importance scoring retained or reordered messages inside a compacted span.
The model can read these archives through the same `gokin://summary/<id>`
references. Image/media payloads and metadata are preserved in saved sessions,
rewind snapshots and original-message archives. Summary archive failures preserve
the current context; stale background
summaries cannot overwrite a new turn or a cleared conversation. A point that
no longer matches after an unregistered history rewrite must be restored first.

`/clear` saves the previous dialogue and starts a new session ID with its own
rewind timeline. The picker then includes `/resume <id> (previous session)` at
the top; select it or run `/rewind previous` to return. This entry lasts only
within the current process and disappears after a successful switch to another
session. Saved dialogues also remain available through an explicit `/resume`.
The previous session's writer lease stays held while its entry is available,
protecting it and its snapshots from cleanup and concurrent writers.
Returning preserves current files and restores the previous dialogue's file
checkpoints and active skill hooks, including consumed `once` state. Cancelled
automatic retries remain cancelled. Writer leases and current hook definitions
are validated before switching; a failed switch keeps the entry available.
With session persistence disabled, the previous dialogue is held only in memory
and is discarded when the process exits. Clearing an unused fresh root keeps
the last meaningful previous entry.

Snapshots live in private files beside session storage and are removed with
expired sessions. Rewind checks the entire file timeline before changing
anything and rolls back completed changes on a later failure. Conflicting
manual edits, linked paths and incomplete file snapshots stop code restoration.
Shell/external changes are not captured. Stop background tasks before rewind;
restoring invalidates delayed tool retries. Disabling session persistence also
disables file snapshots. Both soft history governance and the hard 100-message
window preserve dropped originals through the same journal. An interrupted or
failed handoff stays in the saved session until the journal acknowledges it;
resume retries that transfer before starting new model work. A storage failure
therefore pauses further model exchanges while keeping the originals available
for recovery. The full rewind UX remains under validation against the
[checkpointing reference](https://code.claude.com/docs/en/checkpointing).

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
successful-but-incomplete response. Foreground retries, completion reviews,
automatic verification fixes and Stop-hook follow-ups share that turn count.
Interactive turns retain Gokin's adaptive
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

In headless mode, a permission-mode or permission-rule denial is an ordinary
error tool result: the model is told not to retry that call and may finish the
request using its remaining tools. `dontAsk` never prompts, denies calls that
are not already allowed, and continues; conservative read-only Bash remains
allowed by that mode.
`acceptEdits` permits edits; an unapproved Bash check can be denied without
reverting the edit or failing the whole invocation. JSON results always include
`permission_denials`, with `tool`, optional `tool_use_id`, and a short reason;
raw inputs and potentially sensitive policy text are omitted. Any recorded
permission denial sets `incomplete: true` and adds a `warnings` entry with the
denied call count and tool names, warning that the answer may not be based on
those calls. These fields do not change `status: "success"` or the exit code.
Other failures, unfinished todos, incomplete delegated work, and mismatched
file-change claims can also set `incomplete: true`. Use
`--strict-permissions` to stop on the first ordinary denial with
`status: "policy_blocked"`. Capability/schema ceilings, hard safety blocks and
blocking pre-tool hooks, hook `continue:false`, and missing required managed
hooks remain terminal with `status: "policy_blocked"`. Hook stops retain the
existing `error.kind: "hook_stopped"` diagnostic.

`--brief` requests one final summary line without verification narration. JSON
results always include `files_changed`, a sorted array of workspace-relative
paths reported as mutated by the foreground and invocation-owned child tool
ledgers, including edits without Bash. Text mode buffers the answer and prints
the summary followed by one changed path per line. Read-only and denied calls
add no paths. Unreported shell side effects are not inferred from unrelated
pre-existing Git changes. A supplied `--json-schema` remains authoritative.

`--no-self-review` (or `GOKIN_NO_SELF_REVIEW=1`) disables the optional completion
review, multi-file verification reminders, and delegated verification nudges
for that invocation. The hard done-gate and its bounded auto-fixes remain;
the existing `done_gate.enabled: false` setting (also implicit in `--bare`)
disables that gate. Incomplete-todo continuation and Stop hooks remain active.
Headless completion review also skips a single successful edit whose reported
diff adds and removes exactly one line; unknown diff sizes, repeated mutations
and multi-file changes retain review. A passing hard gate runs checks without
adding model rounds; a failed check can still require an auto-fix round.

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
Legacy Markdown commands also load from project `.claude/commands/` and the
Claude user directory's `commands/`. For example,
`.claude/commands/frontend/review.md` becomes `/frontend:review`. Frontmatter
is optional; `name` and `paths` are ignored in command files. Skills take
precedence over command files with the same invocation name, and personal
commands precede project commands. Command files share skill validation,
permission rules, dynamic rendering, live refresh and compaction carry.
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

Dynamic context supports inline shell commands (`` !`git status --short` ``)
and multiline ` ```! ` blocks. Commands run sequentially through the owning
Bash tool, retaining permissions, hooks, workspace boundaries, cancellation
and output limits. A command needing new approval aborts rendering without a
prompt; trusted `allowed-tools` can pre-approve it. Failed renders do not retain
instructions or activate permission rules. Arguments and command output never
introduce additional dynamic commands. Set
`tools.disable_skill_shell_execution: true` before startup to substitute a
policy marker for shell blocks, including in delegated agents.

Claude skills use zero-based `$0`, `$1`, and `$ARGUMENTS[N]`; unavailable indexed
arguments remain literal. Native Gokin skills retain `$1` as their first
argument. `$ARGUMENTS` preserves the invocation text, and named parameters use
the `arguments` frontmatter list. `${CLAUDE_SKILL_DIR}` and
`${CLAUDE_PROJECT_DIR}` work in the body and permission rules.

Skills with `context: fork` run through the normal `task` tool in a fresh agent
without the parent conversation. `agent: Explore`, `Plan`, `general-purpose`,
and registered custom agent names select the worker. Forked skills default to
background execution; `background: false`, headless mode, or
`CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1` waits for the result. Calling a skill
again while its earlier worker is still running also runs the new invocation
in the foreground, with its new arguments. Background forks keep code/search,
shell and MCP tools, with interactive question and plan workflows excluded.
The child retains the rendered instructions through compaction. Its grants
stay in that child's run, its restrictions follow further delegation, and the
parent receives the worker's result without loading those instructions or
activating the child's grants.

Skills can set `model` and `effort` without changing the session's saved
settings. Inline overrides apply to the rest of the current turn and clear on
the next prompt, while their rendered instructions stay in context. `/skill`
prepares its override for the immediately following prompt. A failed load or
cancelled handoff does not activate settings. `model: inherit` keeps the invoking model, including when the caller
is itself a sub-agent. Explicit model IDs must belong to the invoking provider's
catalog. On GLM, `haiku`/`fast`/`flash` select `glm-5.3-flash`, while
`sonnet`/`opus`/`pro`/`strong` select `glm-5.3`. These are native replacement
tier mappings, not requests to Anthropic. Skill effort overrides currently
require a GLM-5.3 model and use its `low`, `high`, or `max` level; Claude's
`medium` maps to `high` and `xhigh` to `max`. The launch result reports the
resolved model and effort. Validation runs before dynamic shell commands.
Both foreground and sub-agent requests, tool continuations, cost accounting
and auxiliary calls use the selected model. Paths and shell selection remain
unsupported.

Skill frontmatter supports command, prompt, agent, HTTP and MCP-tool hooks for `PreToolUse`, `PostToolUse`,
`PostToolUseFailure`, and `Stop`. Enable `hooks.enabled: true`; project-owned
hooks also require the workspace in `hooks.trusted_workspaces`. A successfully
loaded inline skill activates its hooks for the rest of the conversation,
including later prompts. `/hooks` lists their sources and matchers. New sessions
and `/clear` remove the previous conversation's registrations. Saved sessions,
session switches, forks and named checkpoints retain hook references and once
state. Resuming resolves commands from the current catalog and requires the
same source and hook definitions, enabled hooks and current workspace trust.
Missing, changed or invalid definitions stop execution before a model request;
`/hooks` reports the reason. Restore does not re-run skill shell rendering or
activate its temporary permission grants. Older saved sessions without hook
references cannot reconstruct previously active registrations.

Handlers receive Claude-style JSON on stdin with the tool name and input,
session and call IDs, agent identity, and effective GLM effort when available.
Regex matchers use names such as `Bash`, `Write`, and `mcp__server__tool`.
Shell commands keep their own variable expansion; `args: [...]` uses direct
process execution. Matching handlers run in parallel. `once: true` consumes a
handler after its first successful execution; cancellation and failures allow
a later retry. Saved once state survives resume; reloading identical definitions
does not rearm it. Named checkpoint restoration rolls it back to that snapshot.
Command timeouts default to 600 seconds and respect caller cancellation. Revoking
workspace trust stops project-owned skill hooks, including inherited checks in
already-running children.

HTTP hooks use `type: http` and an absolute `url`. They POST the same event JSON
with `Content-Type: application/json`. Optional `headers` interpolate `$NAME` or
`${NAME}` only for names in the handler's `allowedEnvVars`; other environment
references become empty strings. Settings-level `httpHookAllowedEnvVars` further
restricts that list. `allowedHttpHookUrls` restricts destinations using `*`
patterns; an empty list skips all HTTP handlers. Lists merge across trusted
settings sources and also apply to managed and plugin handlers. Changes take
effect at the next hook execution. URL host matching ignores case and a trailing
DNS dot; paths remain case-sensitive. Redirects are rejected, and responses are
limited to 64 KiB. The default timeout is 600 seconds.

An empty 2xx response succeeds; a JSON object uses the same decision fields as
command hooks. Non-2xx responses, connection failures, invalid output and timeouts
report an error and let the action continue by default. Set `onFailure: block`
on a command or HTTP hook to deny a pre-tool call on failure. Post-tool failures
with that setting feed the error back to the model. The setting has no effect on
`Stop` or `SubagentStop`; valid blocking responses still request continuation.
Failed or skipped HTTP handlers do not consume a skill's `once` state.

MCP-tool hooks use `type: mcp_tool`, the exact configured `server` name and the
raw `tool` name. Plugin servers use `plugin:<plugin-name>:<server-name>`.
Optional `input` is a JSON object; string values can reference event fields,
for example `{"file_path": "${tool_input.file_path}"}`. A whole-field reference
retains its JSON type; embedded references become text. Nested objects and
arrays are supported, and inserted data is never expanded again. Missing fields
report a non-blocking hook error. Plugin input values can also use configured
`${user_config.*}` options and plugin/project path placeholders. Expanded input
and text output are limited to 64 KiB.

These handlers run under their trusted hook configuration, like command hooks,
without recursively firing tool hooks. The MCP server's `allowed_tools`, paused
state and current plugin authority still apply. Lazy servers connect on demand;
concurrent handlers share an in-flight connection, and each wait respects the
hook's timeout. `MCP_TIMEOUT` can limit connection waiting in milliseconds
(up to 600000; otherwise the existing 15-second default applies). The default
overall hook timeout is 600 seconds. Only MCP text content is read as hook
stdout; images and resources are ignored. `isError: true`, connection failures,
timeouts and invalid output produce non-blocking errors and leave `once` armed.
Valid decisions, `continue: false` and Stop continuation use the command-hook
rules. MCP handlers do not accept `onFailure` or start an OAuth flow. `/hooks`
shows their server and tool without displaying input values.

Prompt hooks use `type: prompt` with a `prompt` string. `$ARGUMENTS` inserts the
event's JSON input; without that placeholder the input is appended. Each check
runs on an isolated model client without tools or foreground instructions.
The default is the configured small fast model; `model` selects a provider-local
model explicitly. The default timeout is 30 seconds. Checks share invocation
turn, time and cost limits, and their usage contributes to session and goal totals.
Partial or invalid responses retain reported usage. Duplicate JSON fields,
missing `ok`, tool calls and a blocking response without a reason stop execution.

A response of `{"ok": false, "reason": "..."}` ends the turn by default for
`PreToolUse` and `PostToolUse`. With `continueOnBlock: true`, a pre-tool denial
becomes a tool error and a post-tool reason reaches the next model request.
`PostToolUseFailure` always feeds the reason back. A Stop denial uses the
existing bounded continuation; `impossible: true` allows that turn to finish.
Settings and plugin hooks also support `SubagentStop`. `/hooks` displays the
prompt and model. Prompt controls participate in saved hook fingerprints and
once state; command-only fingerprints retain their existing format.

`UserPromptSubmit` runs before a submitted prompt reaches the model, including
automatic turns. For typed slash commands and skills it sees the original
line before expansion or dynamic shell rendering; an expanded workflow retains
the same check through the foreground queue. Informational and session-control
commands that do not submit prompts do not fire it. This event ignores `matcher`.
Command, HTTP, MCP-tool, prompt and agent handlers are supported. Exit code 2,
`decision: block`, or `continue: false` prevents processing. A blocked prompt
does not enter model history. `hookSpecificOutput.suppressOriginalPrompt: true`
omits its text from the block message; local prompt history and journals may
still contain it. Plain stdout on success and
`hookSpecificOutput.additionalContext` accompany the model prompt and remain
in saved history. `hookSpecificOutput.sessionTitle` sets a persisted session
title, used in session previews; titles are normalized to one line and limited
to 200 characters. A fresh conversation clears the title, and a fork copies it.

The default UserPromptSubmit timeout is 30 seconds for command, HTTP, MCP-tool
and prompt handlers, and 60 seconds for agent handlers. Failed or timed-out
command/HTTP/MCP checks discard their output and allow submission unless an
explicit blocking decision or `onFailure: block` applies. Model check failures
stop the submission. Model usage shares invocation limits and contributes to
session and goal totals, including checks before `/goal` starts its worker.
Command-only async controls retain their background semantics.

`SubagentStart` runs for each new or resumed child run. Its matcher uses the
Claude-style agent type (`Explore`, `Plan`, `general-purpose`, or the scoped
plugin agent name), and input includes the child's `agent_id` and `agent_type`.
Command, HTTP and MCP-tool handlers can return
`hookSpecificOutput.additionalContext` for that child's history. They cannot
block creation. Resuming a child retains an existing identical context block;
after compaction removes it, a later run can add it again. Startup context and
background hook results remain owned by the child.

`SessionStart` runs at launch and after `/clear`, interactive `/resume`, a fork,
or compaction. Its matcher selects `startup`, `clear`, `resume`, `fork`, or
`compact`. Launch and clear run in the background; the first prompt, including
shell expansion in a typed workflow, waits for completion. Esc returns a waiting
prompt to the composer and leaves startup hooks running. A successful clear or
session switch cancels the old startup and discards its context and environment;
a failed replacement retains the old owner. Interactive `/resume` joins the new
session's hooks before completing the switch.

Command and MCP-tool handlers are supported. Following the
[Claude Code lifecycle contract](https://code.claude.com/docs/en/hooks#sessionstart),
MCP handlers are skipped at launch (including CLI resume/fork), and can run on
later session events. Plain command stdout and `additionalContext` reach the
root model history; children have separate hook inboxes. `sessionTitle` applies
to startup/resume/fork; `reloadSkills` refreshes the skill/command catalog.
In headless mode, `initialUserMessage` supplies the first turn even without a
CLI prompt. An explicit prompt then runs as the next turn, sharing the same
invocation turn, cost and timeout limits. Stream input retains one startup for
the CLI session. Hooks cannot block startup; failed handlers lose their output.

SessionStart commands receive `CLAUDE_ENV_FILE`: append shell exports there to
set variables for subsequent Bash commands. The file is sourced inside each
command's process/sandbox, including child workflows; the application process
environment is unchanged. Compaction retains it, and a new conversation gets
a fresh file. Async startup handlers share that conversation's lifetime and can
deliver context or request an `asyncRewake` turn. The optional resumed-cache
age/cost fields are not implemented yet.

`FileChanged` observes actual filesystem changes, including external processes,
atomic saves, symlink targets, newly created files and recreated parent
directories. Its matcher has two roles: split `a|b` into literal paths to watch
in the working directory, then apply the normal matcher rules to the changed
file's basename. An omitted matcher adds no watch seed and handles any watched
file; `*` matches any basename but also seeds a literal file named `*`.
The watcher starts only when a path is named. See the
[FileChanged contract](https://code.claude.com/docs/en/hooks#filechanged).

`SessionStart` can return `watchPaths` with absolute filenames. Multiple startup
handlers contribute paths. A FileChanged handler's `watchPaths` replaces the
dynamic list; `[]` clears it, and omission retains it. Configured matcher paths
remain watched. There is a 512-path resource limit, with configured paths taking
priority over dynamic additions. Filesystem bursts are coalesced at 100 ms;
a one-second metadata rescan repairs lost/replaced parent watches. Active
watchers refresh configured paths and check current settings and skill authority
before executing a handler.

FileChanged input contains `file_path` and `event` (`add`, `change`, `unlink`),
plus the common session fields. Command, HTTP and MCP-tool handlers are supported;
prompt/agent handlers are rejected. Decision output and `additionalContext` are
discarded. `systemMessage` is an interactive notification and never enters the
model history or headless stream. Async results can update watches after the
handler completes. Explicit `asyncRewake` can wake an interactive conversation;
in headless mode it waits for an explicit prompt, including between stream-json
records. Esc cancels explicit asynchronous file hooks without cancelling startup
hooks or the file observer. Clear/resume cancels the old observer and its workers;
a failed conversation replacement retains them.

FileChanged and CwdChanged command hooks share a separate `CLAUDE_ENV_FILE`,
sourced after the startup environment in subsequent Bash commands. A main
conversation directory change clears those transient exports while retaining
SessionStart exports. Replacing the conversation clears both environments.

`CwdChanged` runs after a main conversation Bash command publishes a new working
directory. It receives `old_cwd`, `new_cwd` and the new directory as `cwd`.
Matchers are ignored. Command, HTTP and MCP-tool handlers are supported;
prompt/agent handlers are rejected. The handler runs in the background and
cannot block the directory change or add model context. Its `watchPaths` replaces
the dynamic list, including `[]` to clear it; configured relative paths follow
the new directory. `systemMessage` is an interactive notification only. See the
[CwdChanged contract](https://code.claude.com/docs/en/hooks#cwdchanged).

Each directory gets a fresh transient environment file. Moving again cancels
previous directory handlers and discards their late watch updates and exports.
Subagent shells, subshells, failed `cd`, printed directory markers and stale
conversation results cannot publish a main conversation directory change.
Directory tracking also covers sandboxed commands and explicit `exit`/`exec`.

`DirectoryAdded` runs in the background after `/add-dir` publishes a new
directory and refreshes access for foreground tools and future subagents.
Boot `--add-dir` flags, permission-prompt grants and paths already covered by
the workspace or an existing grant do not fire it. Matchers select the source;
the command path uses `slash_command`. Input includes the absolute `directory`
and `source`, with the current shell directory as `cwd`. Command, HTTP and
MCP-tool handlers are supported, with a 600-second default timeout.

DirectoryAdded cannot block the grant, change watches or add arbitrary model
context. A successful command-path `systemMessage` is delivered once to the
main conversation on its next request; failures contribute a count, with
details in the debug log. A child cannot consume these results. Conversation
replacement cancels old setup processes and drops undelivered results;
removed hook sources also lose their pending output. SDK `register_repo_root`
fires the same event after registration and requested component reloads; its
`systemMessage` and failure output go only to the debug log. See the
[DirectoryAdded contract](https://code.claude.com/docs/en/hooks#directoryadded).

`SessionEnd` runs for a terminating CLI session, `/clear`, interactive `/resume`,
logout of the active provider, and provider/login conversation resets. Matchers
filter the reason: `clear`, `resume`, `logout`, `prompt_input_exit`, or `other`.
The input retains the ending session's identity. A failed replacement does not
fire it. Command, HTTP and MCP-tool handlers are supported; they cannot stop
termination or add model context, and their decision/JSON output is discarded.
Cleanup runs after foreground cancellation and before MCP teardown or release
of the source session's writer lease. Successful skill `once` outcomes remain
with the ending session, including one retained for later `/resume` after clear.
Shutdown skips cleanup if an unfinished writer still owns the conversation
when the acquisition deadline expires.

SessionEnd handlers share a 1.5-second budget by default. An explicit timeout
on a matching settings handler raises that budget up to 60 seconds; plugin and
skill declarations cannot raise it. Other handlers without an explicit timeout
keep their 1.5-second limit. `CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS` overrides
the shared budget and the limit for handlers without an explicit timeout
(positive milliseconds, capped at 60000). Cancellation terminates command
process groups. Async handlers are owned by this cleanup and cannot wake a
later conversation. Hook failures are reported without changing the command's
exit status. Legacy native `on_exit` hooks remain a separate event.

Agent hooks use `type: agent`, `prompt`, and optional `model`. They run an
isolated verification conversation with the invoking loop's available tools,
including file inspection, search, Bash and test tools. CLI capability ceilings,
workspace boundaries, permission denials, native hooks and current plugin/skill
authority still apply. Conversation controls, delegation and skill invocation
are excluded. Agent hooks do not recursively run inside the verifier; command,
HTTP, MCP and prompt checks on its tools remain active. Verifier tool events
carry their own `agent_id` and `agent_type: hook`.

The default model is the configured small fast model and the default timeout
is 60 seconds. Verification allows at most 50 model requests, within the
invocation's shared turn, time and cost limits. Every request, including tool
continuations and partial responses, contributes to session and goal usage.
The verifier keeps its own tool history and reasoning; foreground history and
model settings remain separate. Reads and searches bypass foreground result
caches, and read-before-edit/overwrite checks use the verifier's own reads.
It returns `{"ok": true}` or
`{"ok": false, "reason": "..."}`. A false pre-tool decision denies the action;
post-tool failures feed back to the model, and Stop denials use the existing
bounded continuation. Agent hooks do not accept `continueOnBlock` or
`impossible`. Invalid decisions and execution failures stop the turn and retain
reported usage. `once` is consumed only after a valid decision, including a
valid denial. Prompt and agent `$ARGUMENTS` substitution runs once; `\$` writes
a literal dollar sign, and inserted event or plugin option data is not expanded.

Command hooks support `async: true`. They start in the background and do not
delay tool execution. Only `systemMessage` and
`hookSpecificOutput.additionalContext` from their JSON stdout reach the next
model turn; decision fields, `continue`, exit code 2 and `onFailure: block` do
not control the foreground action. An idle session waits for the next user
prompt. Output may be a complete JSON object, or a single-line JSON object
among log lines. Invalid field types are discarded individually. These hooks
ignore their per-hook timeout, and `once` is consumed after successful process
completion rather than launch.

`asyncRewake: true` also runs a command in the background, but exit code 2
requests an automatic turn with stderr (or stdout) as context. Its timeout is
enforced. Automatic turns use the normal invocation limits and accounting,
wait for foreground and queued user work, and respect Esc cancellation.
Disabling or changing the originating hook revokes its pending result and
automatic request authority. Unrelated settings edits preserve an unchanged
handler. Both flags are command-only.

Background processes and pending results belong to their conversation.
Clear, rewind, session replacement and shutdown cancel the old scope; delegated
agents and hook verifiers own separate scopes. Headless mode cancels remaining
processes at teardown and does not start a later automatic turn. Limits are
32 running processes and 256 pending results (4 MiB total) per scope; when the
result queue is full, the oldest pending results are discarded.

Pre-tool JSON supports `allow`, `ask`, `deny`, `updatedInput`, and
`additionalContext`. Rewritten arguments are validated and authorized before
dispatch, with explicit denials and workspace boundaries still enforced.
`continue: false` stops the invocation, skips remaining sequential calls and
later dependency groups, and retains every tool-use/result pair. Already
started parallel reads may finish. Exit code 2 blocks pre-tool actions even
when stdout contains malformed JSON. A skill's own hooks also guard its dynamic
shell rendering; a failed render does not publish the skill's hooks or grants.

Delegated agents retain parent tool checks and own their local registrations.
Forked skill hooks stay in the child run and its `Stop` event is reported as
`SubagentStop`. A blocking Stop hook gets one additional pass in a sub-agent;
continued denial fails that run. Foreground Stop hooks use the existing bounded
continuation queue and report a repeated denial. Headless foreground Stop hooks
run one synchronous continuation within the original request, preserving its
skill permission rules, shared model-round limit and cost budget. Repeated
denial or `continue: false` returns a non-zero `hook_stopped` result with completed
history and usage retained. Events beyond those listed here remain
unsupported and are rejected in frontmatter.

With `hooks.enabled: true`, Gokin also loads command, prompt, agent, HTTP and MCP-tool hooks from
`~/.claude/settings.json` (or `$CLAUDE_CONFIG_DIR/settings.json`),
`<project>/.claude/settings.json` and `<project>/.claude/settings.local.json`.
Project and local files require the workspace in `hooks.trusted_workspaces`.
These sources merge and run without invoking a skill, including in delegated
agents. Matching handlers run in parallel; identical matching handlers across
settings files execute once per event. Skill handlers remain separate.
Settings support `UserPromptSubmit`, `SubagentStart`, `PreToolUse`, `PostToolUse`,
`PostToolUseFailure`, `Stop`, `SubagentStop`, `SessionStart`, `FileChanged`,
`CwdChanged`, `DirectoryAdded` and `SessionEnd`. Settings `Stop` applies to the main conversation;
`SubagentStop` applies to a child and matches its Claude-style agent type.
The `once` flag is ignored in settings, as in Claude Code.

Plain matchers such as `Bash`, `Edit|Write` and `Edit, Write` match exact names.
Patterns containing regex metacharacters, such as `^Notebook` and `mcp__.*`,
use unanchored Go regex matching; unsupported regex syntax is rejected.
Changes are sampled before requests and tool execution. A tool waiting for
permission cannot execute under settings that changed after its pre-hooks.
Invalid JSON, duplicate keys, unsupported events/controls and more than 256
settings handlers stop execution with an explicit diagnostic in `/hooks`.
Repairing the file takes effect at the next boundary without restarting.

Non-bare invocations also load OS-owned `managed-settings.json` and alphabetical
`managed-settings.d/*.json` drop-ins from `/Library/Application Support/ClaudeCode`
(macOS), `/etc/claude-code` (Linux), or `C:\Program Files\ClaudeCode` (Windows).
These command hooks run independently of `hooks.enabled` and workspace trust.
Managed drop-ins add handlers; later boolean values override earlier ones, and
`null` removes an earlier managed key/event. Empty managed files count as `{}`;
hidden and non-JSON drop-ins are ignored.

Ordinary `disableAllHooks` follows user → project → local precedence and disables
ordinary hooks. Managed hooks remain active. Managed `disableAllHooks: true`
disables all hooks; managed `false` overrides lower values while preserving the
native enable flag for ordinary hooks. Managed `allowManagedHooksOnly: true`
excludes native, user, project and skill hooks. This key has no authority in
ordinary settings. Existing required skill hooks stop the conversation rather
than silently lose their checks; activation and restoration are rejected until
the policy permits them. `/hooks` shows managed provenance and excluded hooks.

Invalid or unreadable managed sources stop execution even with ordinary hooks
disabled. This is stricter than Claude Code's diagnostic-only handling of some
read/type errors. Reads are bounded to 1 MiB/file, 16 MiB total and 256 managed
files; the combined active settings scope is limited to 256 handlers. Changes
and repairs take effect at request/tool boundaries without restarting.

Bare mode skips settings discovery. Remote organization settings, macOS MDM,
Windows policy registry, additional events/handler kinds, policy
helpers and other Claude settings remain unimplemented by this loader.
`allowManagedHooksOnly` governs imported command hooks, gokin hook scopes and
plugin command hooks, with an exemption for managed force-enabled plugins. It
does not implement the broader Claude helper policy. This does not claim full managed-settings compatibility.

### Claude plugin workflows, agents and command hooks

Non-bare runs import skills, commands, agents and command hooks from locally installed Claude plugins
(`~/.claude/plugins/installed_plugins.json`, version 2), plugins with a manifest
under user/project `.claude/skills/`, and explicit session directories:

```bash
gokin --plugin-dir ./my-plugin --plugin-dir './plugins with spaces' -p 'Review changes'
```

Each flag takes one literal directory; a container directory loads its children
with `.claude-plugin/plugin.json`. Explicit directories enable their hooks even
when automatic hooks are disabled with `hooks.enabled: false`. Ordinary native
skill hooks retain that enable flag; plugin skill hooks use their owning
plugin's current explicit/managed hook authority. Bare mode rejects explicit plugin loading.
Archives, URLs and `CLAUDE_CODE_PLUGIN_DIRS` discovery are not implemented.

The loader combines `hooks/hooks.json` with the manifest's `hooks` string path,
inline event map or mixed array. Hook files require the top-level `hooks` wrapper;
inline maps contain events directly. Declared JSON paths must start with `./`
and stay inside the canonical plugin root, including through symlinks. Manifest
symlinks outside the plugin are also rejected. The supported events/command
controls are the same as settings hooks. Identical matching handlers within a
plugin execute once; commands from different plugin environments stay separate.

`enabledPlugins` follows user → trusted project → local → managed precedence.
Managed `true` force-enables and managed `false` blocks. Managed entries lock
that plugin name against replacement by a sideloaded copy. Force-enabled plugin
hooks survive ordinary disable flags and `allowManagedHooksOnly`; managed
`disableAllHooks: true` disables them. `disableSideloadFlags: true` rejects
explicit directories. An enabled session copy overrides an installed copy;
disabling it allows the installed copy to load. Installed project/local records
require both matching canonical workspace paths and workspace trust; a user
record provides a fallback. Untrusted project plugin directories are not scanned.

Hook processes receive `CLAUDE_PLUGIN_ROOT`, `CLAUDE_PLUGIN_DATA` and declared
`CLAUDE_PLUGIN_OPTION_<KEY>` values. Data lives in the Claude user directory's
`plugins/data/<sanitized-id>/` and is created at the first hook execution. Exec
form substitutes path and `${user_config.KEY}` references as literal arguments.
Shell form uses process environment variables and rejects `${user_config.*}`
interpolation. Non-sensitive options come from defaults and user/managed
`pluginConfigs[id].options`; managed null removals restore lower values. String,
boolean, number, file/directory and string-array values are supported; arrays
are exported as JSON text. Unknown schema fields and invalid values stop loading.
Plugin environment variables are isolated to the owning hook process.

Plugin source/configuration changes are sampled at request and execution
boundaries, including existing delegated consumers. This differs from Claude's
explicit plugin reload lifecycle. Invalid or missing selected hooks stop before
requests/tools and appear in `/hooks`; repair works without restarting. Reads
share one per-boundary sample and the settings' total size/handler budgets.
Discovery is limited to 64 plugin candidates, 32 explicit directories and 4096
entries per scanned directory; manifests can reference at most 256 hook sources.

Plugin workflows load from the default `skills/` and `commands/` directories.
Manifest `skills` paths add directories or single skill folders, including `.`
for a root `SKILL.md`; manifest `commands` replaces its default scan and accepts
`.md` files, flat directories, arrays, or objects with exactly one `source` or
`content`. Object entries can override description, argument hint, model and
allowed tools. Plugin frontmatter is optional. A root `SKILL.md` is also loaded
when no skills directory or explicit skills declaration exists.

Invoke workflows with `/plugin-name:skill-name`, `/skill plugin-name:skill-name`,
or the model's `skill` tool. Plugin frontmatter `name` controls the last namespace
segment; an existing matching prefix is not added twice. Unique bare aliases
are available without shadowing native skills or built-in slash commands.
`disable-model-invocation` and `user-invocable` apply normally. Personal skills
honor `CLAUDE_CONFIG_DIR`; project plugins require workspace trust.

Skill discovery is independent of `hooks.enabled`, `disableAllHooks` and
`allowManagedHooksOnly`. A workflow with required frontmatter hooks still needs
its owning plugin's current hook authority. Its hooks receive the same plugin
process environment as global plugin hooks. Saved sessions retain only source,
contract hash and once state; restoration binds the current root, options and
policy. Revocation blocks required checks before provider or tool execution,
including consumed once handlers. Repair preserves consumed state.

Plugin root/data and non-sensitive option references expand once in workflow
text and allowed-tool paths; inserted options never become argument placeholders
or dynamic shell syntax. `${user_config.*}` inside dynamic shell commands is
explicitly unsupported. Dynamic context still uses the owning Bash tool's
permissions, capability ceiling and shell-disable policy. Discovery opens
workflow files beneath an `os.Root`, rejects symlink files/escapes, and shares
the normal skill byte/count limits. Invalid workflows are omitted with warnings.

Plugin agents load recursively from `agents/`; subfolders add namespace
segments, so `agents/review/security.md` appears as `plugin:review:security`.
Manifest `agents` accepts contained `.md` paths or arrays and replaces the
default scan. Frontmatter `name` replaces the final segment; files without
frontmatter use their filename and a fallback description. Select them through
the `task` tool's `subagent_type`, or a forked skill's `agent` field.

Supported agent controls are `tools`, `disallowedTools`, `model`, `effort` and
`maxTurns`. Tools accept comma-separated strings or YAML lists, including MCP
names; deny specifiers remove the entire tool. Explicit empty lists expose no
tools. Limits survive `request_tool`, caller ceilings and nested delegation.
GLM model aliases and effort use the same validated mapping as skills. Agent
clients receive their actual tool schema each round; parent clients stay
isolated. The agent's own prompt remains alongside supplied project context.
Catalog changes refresh on the next lookup; unchanged content keeps the schema
revision stable. Live plugin selection, trust and option changes are checked
before provider requests, auxiliary requests and tool execution, including after
permission waits and when resuming a saved agent. Revocation also applies to
descendants.

As in Claude Code, plugin agent `hooks`, `permissionMode`, `mcpServers` and
`initialPrompt` fields are ignored; global plugin hooks still apply. Agent
skill preloading, memory scopes, forced background execution, `omitClaudeMd`,
worktree isolation and experimental cache controls remain pending and are
rejected when requested. Malformed frontmatter is rejected with a warning
rather than loading an unrestricted fallback. Discovery is limited to 128
agents, 4 MiB in total, 256 KiB per file, 4096 entries per plugin and 16 directory
levels. Symlink escapes and symlink agent files are rejected.

Plugin MCP servers load `.mcp.json` first, then manifest `mcpServers` objects,
contained `.json` paths or mixed arrays. Later server entries replace earlier
entries. Plugin servers connect when `mcp.enabled` is true, or when the plugin
was explicitly selected with `--plugin-dir` or force-enabled by managed policy.
Installed plugins respect the disabled MCP default; bare mode imports none. Supported transports are
stdio and Streamable HTTP. Remote HTTP URLs require HTTPS; loopback HTTP is
available for local servers. Bundles, URLs to bundles, legacy SSE and OAuth
configuration remain pending and are rejected explicitly.

Servers appear as `plugin:<plugin>:<server>` and tools as
`mcp__plugin_<plugin>_<server>__<tool>`. Long or incompatible function names get
stable provider aliases; hooks and permissions retain the full source identity.
Session approvals are scoped to that identity too. Plugin root/data/project
variables and options expand once; arguments use exec form, and inserted option
values never receive another environment expansion. Stdio servers receive an
isolated plugin environment and start in the workspace directory. The data
directory is created when connecting, never during discovery.

`/reload-plugins` refreshes components, retains unchanged MCP connections,
reconnects changed servers and disconnects removed servers. Revocation, trust
changes and changed definitions block RPC calls immediately, including
resource/prompt access and stale agent tool references. A one-second policy
watch retires revoked/changed connections and their advertised tools; it never
starts new servers. Native servers remain separate. Discovery accepts at most
128 servers and 64 sources per plugin, with 256 KiB per source/expanded server
and a shared 4 MiB budget. Duplicate JSON keys, source escapes, unavailable
options and unsupported server controls fail before connection.

Plugin LSP servers load `.lsp.json` at the plugin root, followed by manifest
`lspServers` objects, contained `.json` paths or mixed arrays. The map has no
wrapper. Later entries replace earlier server names. Install the language-server
binary separately; discovery never starts a process or installs dependencies.
Each extension selects the first registered server; a longer extension suffix
takes precedence. Bare mode loads none. MCP's enable switch does not control LSP.

The read-only `lsp` tool supports `goToDefinition`, `findReferences`, `hover`,
`documentSymbol`, `workspaceSymbol`, `goToImplementation`, `prepareCallHierarchy`,
`incomingCalls` and `outgoingCalls`. Supply `filePath` to select the language
server, including workspace searches. Position operations take one-based `line`
and `character` values; columns count Unicode code points. Requests convert to
the server's negotiated UTF-8, UTF-16 or UTF-32 encoding. Returned ranges keep
standard zero-based LSP positions and include the encoding. Read path denies
also apply to LSP; returned locations outside permitted directories or denied
by Read rules are omitted. Servers cannot apply workspace edits through the
client. Plan and explore agents can use LSP too.

Successful source mutations synchronize bounded UTF-8 files and append current
errors/warnings to tool results in both foreground and delegated loops. Full and
incremental document synchronization are supported. Pull diagnostics are used
when advertised; otherwise the client waits for a current published batch.
Missing diagnostics remain an explicit unavailable check, never a clean result.
`diagnostics: false` disables the post-edit check for that server. Isolated
workspaces own separate lazy indexes; the parent closes their processes too.

`/lsp` inspects idle/running servers and startup errors without starting them.
`/reload-plugins` also updates LSP definitions, keeps unchanged processes and
retires changed/removed servers; replacements remain lazy. Live selection,
trust, option and definition changes block stale requests immediately. A
one-second watcher closes revoked processes without adopting new definitions.
Configuration supports `args`, `env`, `initializationOptions`, `settings`,
workspace-contained `workspaceFolder`, positive millisecond timeouts,
`restartOnCrash`, `maxRestarts` and `diagnostics`. As in Claude Code, `socket`
configurations use stdio. Gokin defaults to 10 seconds for startup, 60 seconds
per request, a bounded 2-second shutdown and at most three crash restarts.
Missing binaries can be retried after installation. Failed RPC operations are
reported to the caller; a later operation may restart a crashed server. An
explicit `/reload-plugins` resets stopped servers after an exhausted restart
limit while preserving healthy connections.

LSP discovery is bounded to 128 servers, 64 sources per plugin, 256 KiB per
source/expanded configuration and 4 MiB total. Protocol headers are limited to
64 KiB and bodies to 32 MiB. Active documents are bounded to 64 files per server,
2 MiB per source; automatic checks cover at most 32 changed files with an
8-second batch deadline. The supported configuration follows the
[LSP manifest reference](https://code.claude.com/docs/en/plugins-reference#lspservers)
and [code intelligence behavior](https://code.claude.com/docs/en/plugins/code-intelligence).

Manage plugins without starting a model:

```bash
gokin plugin marketplace add ./my-marketplace
gokin plugin marketplace add acme/team-plugins
gokin plugin marketplace list --json
gokin plugin marketplace auto-update team-plugins on
gokin plugin sync --json
gokin plugin list --available --json
gokin plugin install reviewer@team-plugins --scope user
gokin plugin install reviewer@team-plugins --scope project --config endpoint=https://example.org
gokin plugin update reviewer@team-plugins
gokin plugin disable reviewer@team-plugins
gokin plugin enable reviewer@team-plugins
gokin plugin configure reviewer@team-plugins --values-stdin < values.json
gokin plugin validate ./my-plugin
gokin plugin tag ./my-plugin --dry-run
gokin plugin tag ./my-plugin
gokin plugin uninstall reviewer@team-plugins --keep-data
gokin plugin marketplace remove team-plugins
```

The same operations are available through `/plugin` in an interactive session.
After installing, updating or configuring, use `/reload-plugins` to refresh the
current session. CLI installation defaults to user scope; project and local
scopes write their corresponding Claude settings files. Option values remain
in user-owned settings at every install scope. Bare mode rejects management.
Project-scoped runtime components still require workspace trust.

Normal CLI launches reconcile cached, explicitly declared `extraKnownMarketplaces`
(`additionalMarketplaces` is also accepted) and `enabledPlugins` before loading
runtime components. User and managed declarations apply in every workspace;
project/local declarations apply only in a trusted workspace. Relative project
marketplace paths resolve against the main checkout when launching from a Git
worktree. Registration preserves each declaration's original settings scope.
An enabled `name@claude-plugins-official` also declares Anthropic's official
catalog. Managed marketplace sources replace same-name lower-tier sources;
Gokin records install provenance; uncached or outdated required copies are
fetched in the background before a later launch/reload can load them.
Legacy installs without that provenance need reinstallation when a managed
marketplace requires them. Required userConfig values may come from managed
settings, including ordered drop-ins.

Set `autoUpdate` on a marketplace declaration, or use `marketplace auto-update
<name> on|off`, to refresh the catalog and its installed plugins automatically.
An explicit managed `autoUpdate` field prevents lower-tier changes. Otherwise
the toggle persists in the declaring user/project/local settings and the
registry; a managed declaration without the field permits a registry toggle.
Known official catalogs default to auto-update; third-party/local catalogs
default to off. Updates preserve disabled state and previous immutable versions.
Enabled plugins with missing caches are repaired even with auto-update off.
User/managed declarations may install external sources; a new external plugin
enabled by project/local settings still requires an explicit scoped install.

`DISABLE_UPDATES=1`, `DISABLE_AUTOUPDATER=1` and
`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` stop the update pass;
`FORCE_AUTOUPDATE_PLUGINS=1` permits it. Trusted settings `env` update controls
apply, with managed settings taking precedence. These controls do not erase
explicitly declared marketplaces or prevent provisioning required missing
plugins. Ordinary auto-update passes skip install/helper commands. Accepted command
sources have a separate session refresh, described below; changed commands and
download helpers still require explicit installation or update.

Startup uses cached plugin copies and local sources, with a 500ms reconciliation
budget and no synchronous marketplace network fetch or clone. Missing catalogs
and remote plugins are omitted for that launch. A background refresh starts
after construction, has a two-minute bound, is cancelled with the app context,
and never writes headless stdout. New copies are available on the next launch
or reload; current installed versions stay pinned. Marketplace failures persist
per source in `plugins/gokin/marketplace_failures.json`: retries back off for
6h, then 12h, capped at 24h; success resets the failure state. Explicit
`/plugin marketplace update`, `gokin plugin marketplace update`, and
`gokin plugin sync` bypass automatic retry backoff. Failed fetches retain cached
copies. Embedding hosts opt in using `BuildOptions.SyncDeclaredPlugins`.
`--no-plugins` (or `GOKIN_NO_PLUGINS=1`) disables plugin discovery, marketplaces
and plugin components while preserving native MCP servers, skills, hooks and
settings; it rejects `--plugin-dir`. `--bare` disables the broader discovery
surface. Passive runtime discovery remains offline. The declaration
and update keys follow the [organization settings](https://code.claude.com/docs/en/plugins/org)
and [loading reference](https://code.claude.com/docs/en/plugins/loading).

Marketplace sources support directories, JSON files, GitHub shorthand,
HTTPS/SSH/file git URLs, hosted JSON URLs and inline settings sources. Plugin
entries support relative paths (including `metadata.pluginRoot`), GitHub/git,
git subdirectories, HTTPS ZIP archives, npm packages and command sources in
copy/link modes. Git refs and commit pins are supported; archive SHA-256 pins are
verified. A hosted JSON-only marketplace cannot resolve relative plugin paths.
Git extraction uses archive data rather than checking out files through filters
or initializing submodules. npm package fetches use `--ignore-scripts` and an
isolated staging cache.

Command sources and download `headersHelper` commands are shown before execution
and require `--accept-command <sha256>` for that exact command. A refused JSON
operation returns `shownCommand` with its command and digest; changing the
command invalidates previous acceptance. Gokin remembers accepted plugin install
commands for that plugin/scope and reuses them on update only for the same digest
and copy/link mode.
Install, update and configure accept `--values-stdin` with a JSON object of
string values. A multiple option uses a JSON string array encoded in
its value. Values are validated before saving and never included in listings.
The `--config` flag on install/update sets plugin values, not the Gokin config file.
Sensitive options require stdin; `--config` refuses them without echoing the
value. Interactive `/plugin` does not consume the TUI's stdin; use the CLI.

Sensitive `userConfig` values are encrypted with AES-256-GCM in the user-owned
`plugins/gokin/options.enc.json`. The key lives in macOS Keychain, Linux Secret
Service or Windows Credential Manager, under `gokin.plugin-options.v1`, and is
isolated by configuration directory. An unavailable or locked credential store
fails closed, without a plaintext fallback. Settings, registries and listings
contain no secret values. Secret updates publish with settings and plugin graph
changes; failed validation retains previous ciphertext. Removing a plugin from
its last scope removes its encrypted options, including with `--keep-data`.
Hooks, MCP and LSP receive configured secrets; skills, commands and agent text
use placeholders for sensitive substitutions. User/managed settings cannot
provision a sensitive value in plaintext. Configure it through the CLI instead.

Copied plugin snapshots install Node.js dependencies when their root contains
`package.json` and a supported lockfile. Selection order is `bun.lock`,
`npm-shrinkwrap.json`, then `package-lock.json`; an invalid or unavailable first
choice never falls back to another manager. npm locks require version 2 or 3;
text Bun locks support versions 0–2, including comments and trailing commas.
The manager runs from PATH, in a separate directory containing only checked
dependency data, with frozen resolution, scripts disabled and a 60-second limit.
Plugin `.npmrc`, `.env`, `bunfig.toml`, scripts and trusted-dependency directives
never enter that directory. Registry packages must have exact locked versions;
git, folder, linked and workspace dependencies, overrides and patches are
refused. Downloads use HTTPS, with an HTTP exception for the user's own default
npm registry. Generated trees obey plugin file limits and preserve contained
CLI bin links. Failed or skipped installs retain plugin activation and add a
warning to install results and enabled plugin listings. `plugin update` retries
into a new snapshot. Startup/reload reconciliation uses the same installer when
creating a missing or updated snapshot; passive discovery and validation never
run it. Gokin copies local marketplace plugins too, installing into the copy;
`--plugin-dir` and skills-directory sources stay untouched. Binary Bun, Yarn and
pnpm locks and Bun catalogs/binDir are unsupported. These rules follow the
[Node.js dependency loading reference](https://code.claude.com/docs/en/plugins/loading#nodejs-package-dependencies)
and [user configuration reference](https://code.claude.com/docs/en/plugins-reference#user-configuration).

The real npm/Bun integration tests use a local registry, verify that scripts
stay disabled and run a published package's CLI bin link. Bun 1.4.3 has also
passed this check without npm on PATH, using a user-configured HTTP registry.
They run when the corresponding executable is on PATH:
`go test ./internal/plugins -run '^TestNodeReal' -count=1`.
`GOKIN_TEST_SYSTEM_KEYRING=1 go test ./internal/plugins -run '^TestSystemKeyringPluginLifecycle$' -count=1`
opts into a real credential-store check: it writes one temporary test key,
loads the saved option in a fresh process and deletes that key. This has passed
on macOS Keychain. Linux Secret Service and Windows Credential Manager have
compile and adapter-test coverage; native runtime acceptance on those systems
remains pending. The implementation uses Gokin's own encrypted format and does
not import Claude Code's saved credentials.

Management honors `CLAUDE_CONFIG_DIR` and preserves unrelated fields in
`settings.json`, `known_marketplaces.json` and the installed registry v2.
Copied Gokin installations use immutable content-addressed snapshots under
`plugins/gokin/cache/`; command link mode stores references to a live producer
directory there. Versions switch only after runtime validation of hooks,
skills, agents, MCP and LSP; validation never starts their processes. Failed
fetches or validation leave the previous installation active. State files use
private atomic writes, with rollback on ordinary multi-file failures, and an
OS-level lease serializes Gokin writers. Old snapshots remain cached for live
sessions. Storage/data names include the full ID's hash to prevent punctuation
collisions; earlier unlabelled data directories are retained.

Managed `strictKnownMarketplaces`/`allowedMarketplaces` and
`blockedMarketplaces` apply before downloads and during runtime discovery.
Allowlists compare git sources exactly; blocklists recognize equivalent GitHub
URL spellings. Host/path regex rules, owner wildcards and skills-directory
controls are supported. `disableSideloadFlags` controls explicit plugin roots
separately. `disableCommandPluginSources` prevents command-source installs,
updates and loading; when unset it follows `allowManagedHooksOnly`. Required
plugins cannot be disabled or uninstalled by a lower scope. A marketplace with
installed plugins must be emptied before removal.

Downloads, extraction and copying are bounded: catalogs/state are at most 4 MiB,
downloads 64 MiB, plugin trees 256 MiB/20,000 entries and individual files 32 MiB.
Archive traversal, duplicate members, symlinks and special files are rejected.
Archive DNS/redirect checks reject local/metadata endpoints; credentials are
removed across download hosts. Private Git sources use the user's Git/SSH
configuration without prompting. Source refresh runs only at the explicit
lifecycle boundaries above, never while resolving a model tool call.

Plugin manifests support transitive `dependencies`: names from the same
marketplace, `name@marketplace`, and objects with `name`, optional `marketplace`
and a semantic version range such as `^2.0` or `~2.1.0`. Installation resolves
the complete graph at the requested user/project/local scope and publishes its
registry and settings together after validation. A failed dependency fetch or
conflicting constraint preserves the previous installation. Git dependencies
select the highest `<name>--v<version>` tag satisfying all installed consumers;
relative sources without a matching tag use the marketplace tree and are
checked when loaded. External Git sources without a matching tag fail to install.
Tag versions govern compatibility even when a tagged manifest has an older
version; moving a tag fetches its new commit. Other sources use manifest versions.

Cross-marketplace automatic installation requires the root catalog's
`allowCrossMarketplaceDependenciesOn` list, unless the dependency is already
installed and enabled at the same scope. A disallowed dependency in a catalog
entry refuses installation; one in a manifest leaves the parent installed with
a loading diagnostic. Command sources and dependency header helpers require a
manual installation first. Missing dependencies are repaired by reinstalling
the parent, startup synchronization, `/reload-plugins` and marketplace addition.
Repair uses the installed parent and preserves its contents when auto-update
is off. Discovery itself does not fetch packages or run installers.

Missing, disabled or incompatible dependencies deactivate their consumers and
report diagnostics while unrelated plugins continue loading. Local
`--plugin-dir` copies satisfy dependencies by manifest name, including qualified
marketplace references, without requiring a version. Enabling a bundle enables
its installed dependencies; disabling or uninstalling a dependency required by
an enabled consumer is refused. Uninstalling a bundle retains its dependencies.
`gokin plugin prune` (alias `autoremove`) previews unused automatic dependencies;
add `--yes` to remove them or `--dry-run` to force a preview. Use `--scope` to
select a scope. Manually installed plugins and dependencies needed by remaining
bundles are retained. Dependency behavior follows the
[Claude Code dependency reference](https://code.claude.com/docs/en/plugins/dependencies).

Plugin authors can run `gokin plugin tag [path]` to create an annotated
`<name>--v<version>` release tag for dependency resolution. The path defaults to
the current directory. The command validates the plugin's components and finds
its marketplace entry in the same Git working tree. Versions must agree when
both manifests declare one; a missing plugin version can come from the catalog.
A release requires an exact semantic version, tracked release manifests and a
clean plugin subtree and matching marketplace file. Unrelated marketplace edits
do not block it. `--dry-run` prints the release sources and planned Git commands
without creating a tag. `--message 'Release %s'` customizes the annotation.
`--force` allows dirty files and replaces an existing local tag, while retaining
validation and version checks. Tags point to the committed `HEAD`; uncommitted
files are never included. `--push` sends only the release tag to `origin`, or
the name supplied with `--remote`. It does not force a remote overwrite. A
failed push retains the local tag and reports the partial result, including
`changed: true` in `--json` output. Git hooks and signing programs are disabled
for these operations. See the [release command reference](https://code.claude.com/docs/en/plugins/cli-reference#plugin-tag).

Interactive sessions reconcile declarations at startup, then schedule one
automatic update pass after the first real user message, with a random delay
of up to ten minutes. Only registered marketplaces with auto-update enabled
are refreshed; settings, managed policy and update-disable controls are read
again when the pass starts. The pass has a two-minute deadline, does not run
command sources or header helpers, and stops when the session closes. Cancelling
a foreground turn leaves this session task running. Bare/headless sessions and
embedding hosts that did not enable `SyncDeclaredPlugins` do not schedule it.
Noninteractive opted-in hosts use the bounded background startup refresh;
`gokin plugin sync` performs explicit reconciliation. Successful background
updates show a reload notice, while
failures remain independent diagnostics and preserve installed working copies.

Interactive opted-in sessions retain their installed-version paths until
`/reload-plugins` or restart. Skills, agents, hooks, MCP and LSP continue using
the old immutable snapshots during background updates, including when an update
adds new plugin dependencies. User disables and managed restrictions still
apply immediately. `/reload-plugins` repairs missing dependencies, captures the
current installed versions and refreshes the runtime components. Direct
low-level hook managers retain live registry discovery unless the host calls
`PinClaudePluginInstalls`. This scheduling follows the
[automatic update reference](https://code.claude.com/docs/en/plugins/loading#when-auto-update-runs).

Command sources support both `copy` and `link` modes. Copy versions include a
12-character hash of the copied output; link versions hash the producer's real
path and top-level entry names/types/links, without reading file contents. A
producer manifest version prefixes that hash; a catalog entry version is
ignored. Identical command output is up to date. Link mode publishes a cache
entry containing top-level links, reads components from the producer's real
directory and leaves its files untouched, including its manifest. Marketplace
manifest fields are merged in memory. Schema validation remains bounded, while
unrelated large files are neither copied nor hashed. Node.js installation is
skipped: the producer must supply `node_modules` when needed.

The linked directory must remain available. Top-level symlinks may resolve only
inside it; changed cache targets fail closed. Sessions opened in that directory
or a descendant do not load its plugin. Files in a linked directory remain live,
including edits that keep its version unchanged. Link mode is refused on
Windows; use copy mode there. Changing between `copy` and `link` requires a new
explicit `--accept-command` review, even when the command string is unchanged.
The approval error includes the requested mode. These rules follow the
[command-source reference](https://code.claude.com/docs/en/plugins/marketplace-reference#copy-mode-and-link-mode).

Enabled command sources perform a separate refresh once per session, shortly
after interactive startup, without waiting for the first user message. It uses
the exact accepted command, copy/link mode and marketplace provenance at the
effective installed scope; lower-priority copies are not run. Catalog changes
to the command or mode, missing acceptance, managed restrictions and live user
disables prevent execution and report the explicit update to run. No catalog
or header helper is executed by this pass. `DISABLE_UPDATES` and
`DISABLE_AUTOUPDATER` do not stop it. `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`
does, even with `FORCE_AUTOUPDATE_PLUGINS=1`; trusted settings and managed
overrides apply. Bare and non-opted-in embedding hosts do not run it.
Noninteractive opted-in hosts refresh before component discovery.

Startup and `/reload-plugins` can restore missing enabled command-source caches
using the same saved acceptance. A successful startup restore counts toward the
session refresh. A later explicit reload can retry a missing cache. Each source
uses its configured timeout and session cancellation stops active commands.
Failures retain working copies and do not stop independent sources. Changed
output reloads automatically in an interactive session after the foreground
turn is idle. Unchanged output does not reload or display an update notice.

`/reload-plugins [--force]` previews pending versions without changing the live
session. If an existing conversation would gain/lose a plugin MCP server or the
LSP tool, it shows the cache cost and asks to repeat with `--force` (`force` is
also accepted). The refused reload leaves installed-version pins, tool wrappers
and server connections in place. Other component refreshes apply normally.
The command-source background task uses the same decision and notification.
These lifecycle boundaries follow the [command refresh reference](https://code.claude.com/docs/en/plugins/loading#when-a-command-source-re-runs)
and [reload reference](https://code.claude.com/docs/en/plugins/cli-reference#reloads-that-change-mcp-tools).

A catalog's `forceRemoveDeletedPlugins` removes withdrawn entries during
startup, marketplace refresh and `/reload-plugins`, even when auto-update is
off. Listed entries and names in a validated `renames` map are exempt. Managed
requirements and other workspaces' records remain; trusted workspaces reconcile
their own project/local scopes. Registry, settings, options and encrypted secrets
commit together. Plugin data is cleaned up after the last installation is
removed; immutable snapshots remain for running sessions. Consumers of a
withdrawn dependency receive normal dependency diagnostics, while independent
plugins continue loading. `plugin list` retains bounded removal notices marked
`Flagged: Removed from marketplace`; reinstalling clears the corresponding
notice. This follows the [marketplace withdrawal reference](https://code.claude.com/docs/en/plugins/host-marketplace#uninstall-removed-plugins-from-users-machines).

Marketplace `renames` migrate former names through chained mappings to the
current name, or drop their settings keys for a `null` target. Startup,
marketplace refresh and `/reload-plugins` move `enabledPlugins` and
`pluginConfigs` in the user and trusted project/local scopes. Explicit values
under the current name win conflicts; other old options carry over. Managed
settings are resolved in memory and are never rewritten. Their enable/disable
and option values retain precedence, including across managed documents, and
an old managed name produces a recurring migration notice until the admin
updates it. Other workspaces migrate their own files when they next start.

Sensitive values are re-encrypted with the new ID as authenticated data.
Current-name values take precedence over retired aliases. A locked store or an
unverifiable marketplace source preserves the previous registry, settings and
encrypted options. Retired installations keep their options and files for live
sessions until uninstall. An open session keeps its old namespace/version until
reload; migrated values remain available and live disables still revoke it.
`plugin list` marks retained old rows as renamed or removed.

Relative plugins from local-directory/file marketplaces can be provisioned from
their new entry at startup/reload, retaining automatic-dependency status. A
renamed Git/URL plugin remains uncached until an explicit
`gokin plugin install new-name@marketplace`; discovery reports this without
loading the retired identity or stopping independent plugins. Dependency
consumers follow canonical names and retain normal version/removal checks.
These rules follow the [marketplace rename reference](https://code.claude.com/docs/en/plugins/host-marketplace#migrate-users-with-a-renames-map).

Default official-catalog registration without a declaration and cache pruning
remain pending. Missing enabled plugins outside dependency diagnostics and malformed plugin configurations fail
closed, more strictly than some Claude diagnostic-only cases. No plugin package
install or network fetch runs during discovery. The supported manifest forms
and namespace behavior follow the [plugin manifest reference](https://code.claude.com/docs/en/plugins-reference)
and [skills reference](https://code.claude.com/docs/en/skills).

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

### Goal-driven work

```bash
/goal all acceptance tests pass and the build exits zero
/goal                      # current condition, elapsed time, turns and spend
/goal clear                # also: stop, off, reset, none, cancel
gokin --headless --prompt '/goal notes.txt contains the verified result' --output-format json
```

One goal belongs to the current session. Setting a new condition replaces it
and starts work immediately. After a successful foreground turn, an isolated
model evaluates the condition against the conversation and tool evidence. It
has no tools or worker instructions. GLM-5.3 sessions use GLM-5.3 Flash for this
evaluation by default. A `not_met` reason guides the next normal turn; `met` records the
achievement and `impossible` records failure. Permission mode is preserved.
Evaluation requests share the invocation's turn, time and cost limits.

Goals and their most recent achievement are saved with the session. Resuming
an active goal resets its measured time, turn count and spend. `/clear` removes
the active goal; returning to the pre-clear conversation does not rearm it.
Plan context replacement keeps the condition. Explicit queued user input takes
priority over automatic continuation. The status bar shows an active/paused
indicator. JSON output includes the goal state; text headless output prints the
last worker response when the loop ends.

The evaluator requires enabled ordinary hooks and a trusted workspace. Effective
`disableAllHooks` and managed `allowManagedHooksOnly` block it. Invalid evaluator
output pauses the goal without marking success. Three evaluated turns without
tool use also pause it while preserving the condition; a new user prompt resumes
evaluation. Unfinished or impossible headless goals return a nonzero exit.

Authentication failures, exhausted credit balances, unavailable models and
unresolved context overflows clear the goal with an actionable warning. Usage
window limits, hook stops and other errors preserve the condition on pause.
Failed headless goal turns return a nonzero exit even after the goal is cleared.

Running background agents and shell tasks defer evaluation. In interactive
sessions, their completion, failure or cancellation starts a turn that receives
the task result and continues toward the condition. User input takes priority;
late callbacks cannot restart work after cancellation, goal replacement or a
conversation change. Deferred foreground spend remains included in goal totals.
Esc at an empty idle prompt pauses a waiting goal without removing its condition.
Goal directives stay in the foreground tool executor; workers can explicitly
delegate through the task tool.
Headless invocations keep detached task tools disabled and never schedule a
background continuation after returning their terminal result.

Interactive goals retry temporary network/provider failures up to three times,
waiting 15, 30 and 60 seconds. Retries retain the exact completed-tool journal;
an evaluator failure retries only the independent evaluation. Unsafe or
unserializable replay state stays paused for user review. Usage/rate limits and
hook stops pause the condition. New user input takes over immediately; replacing
or clearing the goal, changing conversations, Esc and shutdown invalidate timers.

Waiting goals check background progress after 30 minutes, then 60 minutes, then
every 120 minutes. Due checks are delivered when a turn ends; interactive sessions
also start them while idle, up to three idle checks between user prompts. The
third check announces the pause. A check lists actual task IDs and asks the worker
to read their output, wait for progress or address stuck tasks. A lost completion
notification does not leave a waiting goal permanently idle.

Set `GOKIN_GOAL_CHECKIN_MINUTES` (or `CLAUDE_CODE_GOAL_CHECKIN_MINUTES`) to change
the initial interval; later intervals scale by two and four. `0` disables both
check-ins and automatic goal retries. Resuming an active session resets retry
and check-in baselines along with its turn, elapsed-time and token counters.
Headless runs never start idle timers or delayed retries after returning.

Approved plan execution now returns to the same goal after its intentional
context replacement. Direct steps, delegated steps and nested tasks contribute
their owned model usage; unrelated background completions are excluded. Native
verification output and step evidence reach the independent evaluator. A
completed plan can be a milestone: a `not_met` verdict continues the larger
condition. Failed or paused plans do not automatically replay their steps.
Approval refreshes the read-only planning request's tool schema for execution;
explicit invocation tool ceilings remain binding, including native verification.

With `--json-schema`, validation and tool-free format correction run before the
goal verdict is published. The invocation has one two-correction allowance,
shared across all goal turns, plus its common model-turn/time/cost limits.
Correction preserves work evidence and cannot resume a paused goal or execute
tools. Status and clear commands never request model formatting. Reported model
rounds include nested agent and auxiliary requests even when the cap is disabled.

Goal spend includes foreground responses and nested or detached agent responses
from the same measurement period. In-flight usage is included when a turn ends;
late agent usage is saved before its completion notification. Repeated completion
checkpoints cannot charge it twice. Goal replacement, session changes and resume
baselines exclude late usage from the previous measurement period. A failed save
retains pending usage for the next successful checkpoint.

Auxiliary requests for compaction, tool-output distillation, session memory,
message scoring, error reflection and task planning share that accounting and
the invocation's model-turn, time and cost limits. Usage remains charged when
an auxiliary response is partial or its output fails validation. Hierarchical
summaries account for every stage. Headless automatic compaction and memory
extraction join the invocation; session restoration cannot launch an unscoped
automatic compaction request before its limits are installed.

Set `GOKIN_SMALL_FAST_MODEL` to select the small fast model used by the goal
evaluator and auxiliary work. `ANTHROPIC_DEFAULT_HAIKU_MODEL` is the compatible
alternative, with `ANTHROPIC_SMALL_FAST_MODEL` accepted as a legacy fallback.
The native variable takes priority, then the compatible variable, then the
legacy one. Values are full model IDs on the current provider, including custom
gateway IDs. Invalid aliases or recognizable foreign-provider IDs produce an
actionable error before the auxiliary request. The main session's model stays
as selected; auxiliary clients keep isolated prompts, tools and mutable state.

The `haiku` alias uses the same selection in `--model`, `/model`, task agents
and skill/plugin-agent model settings. An explicit full model ID retains its
own selection. Local Ollama model names preserve the local transport even when
they resemble a configured cloud fallback. Shared model-turn and cost limits
still apply; a hard dollar limit requires known pricing for the selected model.
Environment preferences are runtime-only. An explicit `/model haiku` change
saves the resolved session model through the normal model-switch workflow.

Substantial live-model goal acceptance remains unverified against the
[goal reference](https://code.claude.com/docs/en/goal).

### Plan Mode
Physical tool restriction — plan mode limits the model to read-only tools (read, grep, glob, diff, git status/log). Auto-exits when you approve the plan, restoring full tool access.

### Cost Tracking
- Per-model pricing for all cloud providers (Ollama is free)
- Real-time cost in status bar
- `/cost` and `/stats` commands

### Prompt Caching
- Stable prefixes for GLM and DeepSeek implicit prompt caching; provider-reported cache reads reduce cost estimates
- System prompt, tools, and conversation prefix cached

### Claude Code replacement readiness

Gokin already includes coding tools, parallel agents, MCP, skills, session
resume/fork, detached background sessions, permission controls and structured
headless output. Complete parity with the [current Claude Code interface](https://code.claude.com/docs/en/cli-reference)
is still under validation. In particular, additional hook lifecycle events,
rewind UX parity, plugin compatibility and general IDE/browser workflows need further implementation
or end-to-end acceptance checks.

For GLM releases, the opt-in `TestLiveGLM53ToolContinuation` checks real tool
calls and reasoning replay at all three effort levels on GLM-5.3 and Flash.
It also requires input/cache usage to survive the terminal stream event.
Set `GOKIN_LIVE_GLM_CONFIG` to an existing config file to run it; this uses
provider quota and sends synthetic prompts only. Local protocol tests passing
without that live gate do not establish coding quality on your account.

Headless GLM-5.3 requests use the same adaptive thinking policy as interactive
requests when reasoning effort is `auto` and no numeric thinking budget is
configured. A simple question can use `low`, while coding work receives a
larger budget. Explicit `--effort low|high|max` and configured budgets retain
their priority. Reasoning selection does not delegate work or make an extra
model request.

After a provider failure, Gokin keeps completed tool calls and their results
in the conversation. Exact retained outcomes are acknowledged so the model
can continue or correct a failed edit. Effects without an exact retained
result remain protected by the recovery ledger; their execution is not
silently repeated.

A temporary compiler failure is recoverable and does not become a permanent
permission refusal. If the compiler barrier refused an edit, Gokin requires a
fresh successful check before reporting successful completion. A still-broken
build or an unavailable compiler keeps the invocation unsuccessful.

`TestLiveGLMHeadlessSessionLifecycle` additionally checks separate CLI processes
creating, resuming and forking a conversation, then confirms the parent retains
its original marker. Set both `GOKIN_LIVE_GLM_CONFIG` and
`GOKIN_LIVE_GOKIN_BIN` (the exact candidate binary) to opt in. It isolates session
storage and checks both the implicit Flash default and an explicit GLM-5.3
selection, sending four synthetic prompts per model. It disables tools and
provider fallback and caps each invocation at 90 seconds and an estimated $0.10.

### Extended Thinking
- Full multi-turn support for GLM / DeepSeek reasoning models
- Signed thinking blocks preserved for providers that emit signatures; GLM unsigned reasoning is replayed byte-for-byte across tool calls
- GLM-5.3 reasoning stays enabled at every effort level. `/thinking off` selects `low`; `/effort low|high|max` overrides adaptive budgets, and `/effort auto` restores adaptation
- The CLI defaults to GLM-5.3-Flash, including the `coding`, `fast`, and `glm` presets. Select `gokin --model glm-5.3`, `/model glm-5.3`, or the `balanced` preset for the flagship model. Explicit saved model selections are preserved.

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
│  (local) │          │  (Z.AI / DeepSeek)   │
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
| **DeepSeek** | `deepseek-v4-pro`, `deepseek-flash` (`deepseek-v4-flash` alias) | 1M context, 384K max output | Peak: Pro $1.32/$3.96; Flash $0.30/$1.20 (off-peak half) |
| **GLM** | `glm-5.3`, `glm-5.3-flash`, `glm-5.3-flashx`; older 5.2/5.1/5/4.7/4.5 | 5.3: 1M input, 128K output | 5.3: $1.40/$4.40; Flash: $0.15/$0.50; FlashX: $0.37/$1.25 |
| **Ollama** | Any local model | Varies | Free |

DeepSeek model IDs, limits and peak/off-peak rates follow the [official DeepSeek pricing page](https://api-docs.deepseek.com/quick_start/pricing/) (checked 2026-10-22). Cost estimates use peak rates as a conservative ceiling.

All cloud providers use Anthropic-compatible APIs and share the same client — fewer moving parts, consistent behavior. GLM and DeepSeek use server-managed implicit prefix caching; DeepSeek accepts and ignores explicit cache markers. Ollama uses its own native client and makes zero network calls.

GLM model parameters and pay-as-you-go estimates follow [Z.ai model documentation](https://docs.z.ai/guides/llm/glm-5.3), [Flash documentation](https://docs.z.ai/guides/vlm/glm-5.3-flash), and [pricing](https://docs.z.ai/guides/overview/pricing) (checked 2026-10-08). Coding Plan subscription quotas are separate from token-cost estimates. FlashX is listed for pay-as-you-go access and is not included in Coding Plan.

GLM API access also depends on the provider's account and client entitlements.
Z.AI limits Coding Plan use to its [listed tools](https://docs.z.ai/devpack/tool/others);
Gokin is not named in that list (checked 2026-10-20). Protocol compatibility
alone does not establish subscription access. For error `1113`, verify that
the key comes from the correct Personal or Team Coding Plan account, then check
the subscription, endpoint, model and supported-client requirements in the
[Z.AI FAQ](https://docs.z.ai/devpack/faq). The error alone does not establish that
adding prepaid balance will resolve it.

For deep coding work in CI, use `gokin -p --provider glm --model glm-5.3 --effort max "fix the failing tests"`. `--effort` changes only the current process configuration.

GLM Coding Plan users can enable the first-party `web_search_prime` tool with `/set glmsearch on`. It reuses `GOKIN_GLM_KEY`, `GLM_API_KEY`, or `api.glm_key`; no second credential is required. This boot-wired setting takes effect after restart.

Switch anytime:
```
/provider deepseek    →  /model deepseek-v4-pro
/provider glm         →  /model glm-5.3-flash
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
| `/settings` | Open interactive settings |
| `/set <key> <on\|off>` | Change a setting directly |
| `/shortcuts` | Keyboard shortcuts reference |
| `/help` | Show all 65 commands |

**Aliases:** `p`=plan, `c`=commit, `m`=model, `s`=status, `u`=undo, `r`=redo, `h`=help, `q`=clear, `st`=stats, `pr`=pr

### Keyboard Shortcuts

The default `ui.keybindings: claude` profile keeps familiar terminal editing
keys available while composing, including while a response streams. Set
`ui.keybindings: gokin` to retain the original Gokin bindings. The welcome
screen, command palette, `/help`, `/shortcuts`, and `?` follow the selected profile.
The editing controls follow the [Claude Code interactive reference](https://code.claude.com/docs/en/interactive-mode);
Gokin-specific panels remain available through separate shortcuts and the palette.

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Esc` / `Ctrl+C` | Interrupt a response; idle `Ctrl+C` clears the draft, which Up restores |
| `Ctrl+C` / `Ctrl+D` twice | Quit when idle; `Ctrl+D` deletes forward when input contains text |
| `Ctrl+A` / `Ctrl+E` | Start / end of line |
| `Ctrl+B` / `Ctrl+F` | Move backward / forward one character |
| `Ctrl+K` / `Ctrl+U` / `Ctrl+W` | Cut to end / start of line / previous word |
| `Ctrl+Y` / `Ctrl+_` | Paste the last cut text / undo input editing |
| `Ctrl+S` | Stash or restore a draft; swap if both drafts contain text |
| `Ctrl+P` / `Ctrl+N` | Previous / next visual line, then history at the boundary |
| `Ctrl+G` / `Ctrl+X Ctrl+E` | Edit the prompt in `$VISUAL` / `$EDITOR`; Enter still confirms sending |
| `Alt+P` | Model selector |
| `F1` / `Alt+X` | Command palette |
| `F2` | Settings |
| `Alt+E` | Reveal the last tool output |
| `Ctrl+O` | Searchable transcript: Tab switches conversation / full tool results |
| `Alt+O` | Live activity detail |
| `Ctrl+L` | Redraw the screen, preserving conversation and input |
| `PgUp` / `PgDn` | Scroll conversation output |
| `Shift+Tab` | Cycle Normal → Plan → YOLO |
| `Ctrl+T` / `Alt+A` / `Alt+H` | Task list / agent tree / context dashboard |
| `Alt+C` | Copy last response |
| `?` | Searchable shortcuts overlay |
| `Tab` | Autocomplete |
| `↑/↓` | Visual lines, then history |
| `y/n` | Accept/reject diff |

In the transcript, `/` searches, `n`/`N` move between matches, and `r` refreshes
the snapshot without appending duplicate tool output to the conversation.
`Esc` or `Ctrl+O` returns to the draft and current request state. Large pasted
content retains its identity across stashing, cutting, yanking and undo.
The external editor opens while idle; errors preserve the draft, and changes
remain undoable. Configure graphical editors with their wait option, such as
`EDITOR="code --wait"`.

---

## Configuration <a id="configuration"></a>

`~/.config/gokin/config.yaml`

Use `--config /path/to/config.yaml` to load an explicit file. An explicit
file must already exist; runtime configuration saves are written back to that
same file rather than the default location.

### Minimal

```yaml
# GLM Coding Plan (default: Flash; model.name can be omitted)
api: { glm_key: "...", active_provider: "glm" }
model: { name: "glm-5.3-flash" }

# Or DeepSeek
api: { deepseek_key: "sk-...", active_provider: "deepseek" }
model: { name: "deepseek-v4-pro" }


```

### Full Reference

```yaml
api:
  deepseek_key: ""
  glm_key: ""
  ollama_key: ""                  # optional, for remote Ollama with auth
  active_provider: "glm"          # glm | deepseek | ollama
  ollama_base_url: "http://localhost:11434"
  retry:
    max_retries: 10
    http_timeout: 0s               # 0 = provider-specific first-header default
    stream_idle_timeout: 0s        # 0 = provider-specific SSE pause default

model:
  name: "glm-5.3-flash"
  temperature: 1.0
  max_output_tokens: 65536         # headroom below GLM-5.3's 131K API cap
  custom_base_url: ""             # override endpoint
  thinking_mode: "auto"           # auto | on | off; GLM-5.3 off means low effort
  reasoning_effort: "auto"        # GLM-5.3: auto | low | high | max; explicit effort wins
  enable_thinking: false          # legacy static switch
  thinking_budget: 0              # 0 = adaptive/provider default

engine:
  # auto stays process-free until the model actually calls repl_exec for a
  # collection-scale request; simple turns pay no Python startup cost.
  # If isolation fails, auto transparently uses normal tools for every request.
  # tools disables the REPL; hybrid fails startup if a secure runtime is absent.
  mode: "auto"                    # auto | tools | hybrid
  repl:
    cell_timeout: 30s             # Python compute inactivity; pauses for callbacks
    max_code_bytes: 65536
    max_response_bytes: 1048576
    max_memory_bytes: 268435456   # 256 MiB peak RSS; breach resets the kernel

ui:
  keybindings: "claude"             # claude | gokin (original bindings)
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
  enabled: false                  # Normal on startup; Shift+Tab enters Plan
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

In the default `engine.mode: auto`, ordinary questions, edits, and targeted
lookups neither start Python nor carry the REPL or harness declarations. A
prompt that combines repository/dataset scope with aggregation, ranking,
comparison, deduplication, or cross-file joins receives a lazy
workspace-read-only `repl_exec` declaration. Python is still not started if the
model answers directly or chooses a structured tool. The first actual
`repl_exec` execution performs a one-time fail-closed sandbox probe and retains
that verified worker for the session. Auto-policy signals are
boundary-aware and cover common English and Russian analytics phrasing;
single-file work, explicitly named file pairs, and cross-file mutations remain
on structured tools even when the prompt also mentions the repository.
Classification examines at most 64 KiB from the prompt edges. This retains the
usual instruction-before/after-data layouts while keeping routing CPU and
memory bounded even when a request includes a large pasted log or dataset.
Mutation-like identifiers, analytic nouns, relative clauses, and negative edit
instructions are distinguished from imperative changes. File-target detection
uses the main analysis clause, ignores verification commands, and does not
mistake directories or dotted code symbols for individual files.
`engine.mode: hybrid`
keeps both declarations visible for every request; `tools` removes them.
The engine mode and `engine.repl` limits are boot-wired: changing them in
configuration while Gokin is running is saved for the next launch and requires
`/restart` to take effect. This prevents the tool schema, router policy, Python
worker lifecycle, and its enforced limits from entering different partially
switched states.
Invocation capability is applied before optional runtime startup: if `--tools`
or `--disallowedTools` excludes `repl_exec`, Gokin does not probe or launch
Python even when the configured mode is `hybrid`. A separately allowed
`harness` remains usable without a Python worker.

The REPL's `context` object keeps large repository analysis out of the model
transcript. Prefer one cell that scans, filters, aggregates, and prints final
evidence: every additional cell requires another model round even when Python
itself is fast. `rlm()` delegates bounded work through the existing permission
and audit pipeline. In auto mode the administrative harness is not advertised
as a separate model tool; it remains available inside eligible REPL work as
`rlm.harness` and its project memory is loaded only on the first such call. A
bad optional harness state disables those callbacks without discarding a
verified analytical worker; explicit `hybrid` mode remains strict at startup.
Harness mutations require approval by default; proposals remain
under `.gokin/harness/proposals/` and are never auto-loaded from
`.gokin/skills/`. Python cannot change permissions, sandbox policy, built-in
tools, or immutable system instructions. Direct Python file writes,
subprocesses, sockets, native-library loading, and Git execution are denied;
resource limits and the OS sandbox remain the hard boundary. Use the ordinary
structured `git_status` and `git_diff` tools for repository state or changes.
Direct directory enumeration (`os.walk`, `listdir`/`scandir`, globbing, and
`Path.iterdir`) is also denied: repository paths must come from the bounded,
ignore-aware `context` inventory/search APIs. This prevents raw Python walks
from silently including ignored trees or bypassing parent-observed scan
telemetry; targeted reads of already known workspace files remain read-only.
Worker/runtime reflection and dynamic-code entry points are rejected before a
cell runs, preventing Python introspection from rewriting telemetry or protocol
state. Cell imports use an analytical standard-library allowlist (JSON/regex,
math/statistics, collections/iterators, CSV/date, hashing/encoding); direct
`open`, OS/runtime modules, threads, and dynamic module loading are rejected.
Known workspace files remain available through bounded `context.read_slice`.
Ordinary computation remains available;
the OS sandbox, rather than this syntax guard, remains the security boundary.
The production worker is single-process: macOS Seatbelt denies fork and every
executable outside the validated Python launcher/runtime pair, while Linux
bubblewrap applies an architecture-checked seccomp filter that denies process
creation. Its workspace, ephemeral runtime, and synthetic filesystems are
read-only; only the Go parent publishes bounded inventory snapshots into the
runtime.

For count/ranking questions, `context.count_code(..., regex=True,
group_by="file|top_dir|extension")` scans the bounded workspace without
materializing every hit. `context.search_code` also accepts `regex=True` when a
small evidence sample is needed; literal substring matching remains its default.
Comparisons across several patterns should use `context.count_code_many(...)`:
it produces separate counts and groups from one consistent inventory/read pass
instead of rescanning the repository for every pattern.
For inventory-only counts or byte totals, `context.file_stats(...)` groups by
extension or top-level directory without materializing thousands of path
dictionaries or retaining the inventory after its streaming pass. It can reuse
an earlier same-scope snapshot. Group cardinality is capped and fails explicitly
instead of returning a partial distribution; use `list_files` when the paths
themselves are needed for a join.
When an exact count also needs example matches, pass `sample_limit` to either
count API. Samples are collected during that same complete scan and report
`samples_truncated` independently from overall scan truncation.
Both calls distinguish candidate files from files actually searched and report
binary, oversized, or unreadable skips. Known binaries and NUL-bearing files
are not decoded as source text, avoiding false matches and wasted I/O.
Directory scans share one bounded repository-visible snapshot per scope within
a Python cell, avoiding repeated Git processes and repeated path metadata work
for a compound analysis. The first traversal remains streaming; a later exact
count can safely resume an evidence search that stopped early. The
snapshot is always refreshed between cells and is invalidated before any
`rlm`/harness callback that could change the workspace. Root and nested
`.gitignore` rules, negations, `.git/info/exclude`, global excludes, and tracked
files use Git's native semantics without walking ignored build trees in Python.
Explicitly naming an ignored directory still reads it; ignore rules filter
implicit project inventories rather than access rights.
Eval journals separately record whether a request was REPL-eligible, whether
`repl_exec` was actually present in the final model-visible schema, and whether
the model used it. Successful cells also contribute bounded operation counters
without retaining Python code, arguments, or repository content, so evals can
prove that one-pass primitives such as `count_code_many` or count-with-samples
actually ran. Scenario contracts can also cap collection-scan operations and
REPL calls, rejecting a nominally correct primitive if it follows redundant
repository passes or avoidable cells. Repository-index refresh counts come from
callbacks observed by the Go parent rather than the Python result payload. This
is paired with a worker-owned lowest-layer `file_inventory` count, so replaying
a cached inventory or reaching an internal helper through Python introspection
cannot make multiple logical scans look like one; legacy journals fall back to
their public operation counters. This
allows evals to require positive parent-observed proof of a repository scan, not
merely place an upper bound on claimed scan work. This
prevents an unavailable sandbox or invocation tool
restriction from being misreported as successful hybrid exposure, and prevents
an arbitrary `repl_exec` call from masquerading as efficient adoption. Eval
runner journals live in a private sibling runtime directory rather than the
model-writable fixture workspace; bash, REPL, and verification environments do
not receive that path, and reports ignore untrusted workspace journal data. Eval
reports also retain per-row engine-mode provenance, availability gaps, and
unexpected exposures, so mixed-mode or opposite policy errors cannot disappear
behind aggregate totals.
Paired efficiency reports include mean and median deltas plus a one-sided exact
sign test over trial-clustered scenario/provider/model/fault evidence units.
They also split total usage into input, uncached input, cache-read input, and
output tokens. This distinguishes actual context compression from provider-cache
effects or output expansion; component gates reject rows without a complete,
internally consistent token ledger instead of treating missing fields as zero.
Repeated trials are averaged inside one unit before its direction is counted,
preventing `--repeat` from turning correlated reruns into fictitious evidence;
a dedicated sign-test gate can fail on an insufficient lower-effect p-value,
and the lower-ratio gate uses the same clustered directions rather than raw
trial counts. Repeated run order is counterbalanced too: comparable
provider/model/fault rows stay
adjacent, while engine position within each cohort and cohort position within
the trial rotate deterministically. With three modes and three trials, every
engine occupies every local position once; six trials append a reversed Latin
block and balance directed adjacent carry-over too. As with any sign test,
inference assumes those units are independent and applies to the eval suite
rather than proving universal workload performance.
An optional median-relative gate enforces practical effect size without letting
one high-volume scenario dominate the aggregate ratio or correlated repeated
trials occupy several central ranks. It averages trials inside each evidence
unit before taking the across-unit median, requires non-zero baselines for every
pair, and otherwise fails closed.
Persisted eval JSONL is parsed strictly and with an explicit per-record bound;
ambiguous keys, unknown schema fields, trailing documents, and impossible
status/score/trial or negative telemetry values fail before report gates run.
Non-empty scenario/run provenance must be a real SHA-256 digest. Paired reports
retain positive verified-provenance counts, and every engine gate requires both
hashes plus candidate/control classification on every pair; hashless or
unclassified legacy results remain diagnostic rather than being promoted to
evidence for a possibly different fixture or executable.
Long eval matrices preserve the previous published report while atomically
checkpointing each completed row to `OUTPUT.partial`; an interrupted run can be
continued with `--resume`. Resume is fail-closed on changed matrix, scenario or
fixture contracts, command/timeout/fault settings, and an explicitly selected
`GOKIN_BIN`, preventing incompatible rows from being mixed merely to save calls.
For eligible requests with actual exposure, a short request-scoped hint favors
one aggregate REPL cell over materializing many grep/read results; the hint is
removed before session history is persisted and is absent on fallback paths.
Use the dedicated [`evals/hybrid`](evals/hybrid/README.md) suite to compare
`tools`, adaptive `auto`, and explicit `hybrid` on identical analytics tasks.

Use `repl_exec` with `action: status` to inspect bounded kernel health
(generation, restarts, executions, transport failures, and timeouts), or
`action: reset` to discard Python globals/artifacts deliberately. Protocol
failures and inactive cells discard the affected kernel automatically; the next
cell starts a clean generation. Value, stdout, and stderr are byte-bounded
independently so all inline channels and their recovery handles fit the outer
tool-result budget; every overflowing channel remains available as its own
named artifact. Artifact sizes and offsets are UTF-8 bytes, and paginated
readers should continue from `next_offset` returned by `context.artifact_get`.
Chunk and storage limits are reported by `context.runtime_limits()`.
The same runtime report includes the peak-RSS budget. A memory breach returns a
typed error, discards the complete Python generation, and starts the next cell
cleanly; this watchdog also covers macOS, where address-space rlimits cannot be
relied upon.
Episodic-memory writes use an advisory lock plus
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
│   ├── client/         # AnthropicClient (GLM/DeepSeek) + OllamaClient
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
