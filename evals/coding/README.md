# Coding Evals

This directory contains a small, machine-readable evaluation set for Gokin's coding-agent behavior.

The manifest is intentionally provider-neutral. Each scenario describes:

- the user-facing task prompt
- the project fixture or target shape
- expected agent behavior
- verification commands
- success criteria and failure signals

Use these cases when comparing model/provider changes, prompt changes, routing changes, or tool-policy changes.

Run a dry smoke pass:

```sh
go run ./cmd/gokin eval run --dry-run --scenario go_bugfix_targeted_test
```

Run the same manifest across providers:

```sh
go run ./cmd/gokin eval run --provider kimi --provider glm --provider minimax --agent-command "$(pwd)/evals/coding/scripts/run-gokin-headless.sh"
```

The headless script uses `gokin` from PATH by default; point `GOKIN_BIN` at a
freshly built binary when iterating locally:

```sh
go build -o /tmp/gokin ./cmd/gokin
GOKIN_BIN=/tmp/gokin go run ./cmd/gokin eval run --provider kimi --agent-command "$(pwd)/evals/coding/scripts/run-gokin-headless.sh"
```

Run a provider/model matrix:

```sh
go run ./cmd/gokin eval run --provider kimi --model kimi-for-coding --agent-command "$(pwd)/evals/coding/scripts/run-gokin-headless.sh"
```

For the dedicated repository-analytics A/B suite, including positive and
negative auto-policy controls, see [`../hybrid/README.md`](../hybrid/README.md).
Any manifest can still be expanded across identical `tools`, adaptive `auto`,
and explicit `hybrid` cohorts in one run:

```sh
go build -o /tmp/gokin ./cmd/gokin
GOKIN_BIN=/tmp/gokin go run ./cmd/gokin eval run \
  --provider glm --model glm-5.2 \
  --engine-mode tools --engine-mode auto --engine-mode hybrid \
  --repeat 3 \
  --agent-command "$(pwd)/evals/coding/scripts/run-gokin-headless.sh" \
  --output .gokin/evals/engine-ab.jsonl
go run ./cmd/gokin eval report --input .gokin/evals/engine-ab.jsonl
```

Each engine cohort is isolated in its own workspace and result identity. The
report pairs identical scenario/provider/model/fault cohorts before comparing
quality, total tokens, model rounds, agent duration, tracked cost, and actual
`repl_exec` calls. Missing, duplicate, non-executed, or changed-spec rows are
reported as exclusions rather than mixed into the A/B averages. With no flag,
the runner explicitly injects `engine.mode=auto` so a user's global config
cannot silently change a baseline.
Repeated matrices isolate every trial in its own workspace, preserve
`trial`/`trial_count` in JSONL, and deterministically rotate execution order to
reduce fixed-order bias. Provider usage scales linearly with `--repeat`.
Every completed row is atomically and durably checkpointed to
`OUTPUT.partial` with private `0600` permissions. The previous `OUTPUT` remains
untouched until the whole matrix finishes, when the checkpoint is atomically
published and removed. If the process is interrupted, the checkpoint remains a
strictly readable JSONL prefix; repeat the exact command with `--resume` to skip
those completed rows. A checkpoint is never overwritten implicitly, and resume
fails before another provider call if the matrix order, scenario/fixture,
agent-command, timeout, fault upstream, or explicitly selected `GOKIN_BIN`
binary changed. This avoids both destroying the last complete report and paying
again for rows whose results are already durable.
For CI, `--require-complete-engine-pairs`,
`--max-engine-score-regression 0`, and
`--max-engine-quality-regressions 0` turn the paired evidence into fail-closed
gates. Every engine gate also requires matching, valid SHA-256
`scenario_spec_hash` and `run_spec_hash` values on both sides of every pair, so
results from different fixtures, binaries, matrices, or runner settings cannot
be silently joined. Candidate/control classification must also be present on
both paired rows, preventing unclassified cases from disappearing into `all`.
Legacy rows without hashes stay visible for diagnosis but are not valid gate
evidence. Gates target `auto` unless
`--engine-gate-mode hybrid` is selected too.
Optional repeatable gates such as
`--max-engine-relative-delta candidates.total_tokens=0%`,
`--max-engine-relative-delta candidates.input_tokens=0%`, and
`--max-engine-median-relative-delta candidates.total_tokens=-5%` enforce
aggregate and outlier-resistant practical magnitude. The median gate fails if
any paired baseline is zero and averages repeated trials inside each
scenario/provider/model/fault unit before taking the across-unit median. It
therefore cannot count correlated reruns as several central observations.
`--min-engine-lower-ratio candidates.total_tokens=50%` separately
enforces consistency across scenario/provider/model/fault units after averaging
repeated trials inside each unit. For hybrid suites,
`--max-engine-lower-p-value candidates.total_tokens=5%` adds a one-sided exact
sign-test gate. It clusters repeated trials by scenario/provider/model/fault,
so `--repeat` reduces within-unit noise without inflating the evidence-unit
count; ties are excluded and missing/non-tied evidence fails closed. Choose a
single primary efficiency metric unless you apply a multiple-testing correction.
Token-component thresholds also accept `uncached_input_tokens`,
`cache_read_input_tokens`, and `output_tokens`. The report requires explicit,
consistent component provenance before any of those gates can pass; a legacy
row with only `total_tokens` is never interpreted as four measured zeroes.
Additionally,
`--min-engine-repl-use-ratio candidates=50%` proves candidate adoption and
`--max-engine-repl-use-ratio controls=0%` limits control misuse; both use exact
paired denominators and fail closed on missing or exposure-inconsistent policy
evidence, including policy events attributed to the wrong engine mode.

Summarize the last run:

```sh
go run ./cmd/gokin eval report --input .gokin/evals/results.jsonl
```

Diagnose what to improve next:

```sh
go run ./cmd/gokin eval diagnose --input .gokin/evals/results.jsonl
```

Compare a prompt/tool change against a previous run:

```sh
cp .gokin/evals/results.jsonl .gokin/evals/baseline.jsonl
# change prompts/tools/model routing, then run evals again
go run ./cmd/gokin eval report --input .gokin/evals/results.jsonl --baseline .gokin/evals/baseline.jsonl
go run ./cmd/gokin eval diagnose --input .gokin/evals/results.jsonl --baseline .gokin/evals/baseline.jsonl
```

Fail a local or CI loop when quality drops:

```sh
go run ./cmd/gokin eval report \
  --input .gokin/evals/results.jsonl \
  --baseline .gokin/evals/baseline.jsonl \
  --require-pass \
  --fail-under 90% \
  --max-regression 2% \
  --fail-metric verification_passed=100% \
  --fail-metric no_false_file_claims=100%
```

The runner writes JSONL results and scores agent evidence from a private
sibling runtime directory, including tool calls and per-tool counts, files
read, files edited, verification commands, false file claims, invocation
tokens, model rounds, duration, and tracked cost. The outer agent-command gets
the reserved `GOKIN_EVAL_RUNTIME_DIR` only so the headless Gokin process can
write there; model-visible bash/REPL environments and subsequent verification
commands do not receive it. Workspace `.gokin/execution_journal.jsonl` files
are ignored by scoring, and reports aggregate efficiency only from results
marked `trusted_runtime`.
Result JSONL ingestion is also fail-closed: each record is bounded to 16 MiB,
unknown fields, duplicate keys at any depth, trailing JSON values, invalid
status/engine/trial provenance, impossible scores, and negative telemetry
counters are rejected with the exact line number. Records above the old
`bufio.Scanner` 64 KiB default remain supported within that explicit bound.

## Reliability and fault injection

`eval run` can place a loopback-only reverse proxy between gokin and an
Anthropic-compatible provider. Each profile injects one deterministic failure
and then becomes transparent. This exercises the real client retry, app
recovery, tool checkpoint, and verification paths instead of a mocked agent.

For GLM 5.2, build the exact binary under test and point the proxy at Z.AI's
Anthropic endpoint:

```sh
go build -o /tmp/gokin ./cmd/gokin

GOKIN_BIN=/tmp/gokin go run ./cmd/gokin eval run \
  --provider glm --model glm-5.2 \
  --fault-upstream https://api.z.ai/api/anthropic \
  --fault-profile after-tool-429-once \
  --fault-profile after-tool-connection-drop-once \
  --scenario go_bugfix_targeted_test \
  --agent-command "$(pwd)/evals/coding/scripts/run-gokin-headless.sh" \
  --output .gokin/evals/glm-reliability.jsonl

go run ./cmd/gokin eval report \
  --input .gokin/evals/glm-reliability.jsonl \
  --require-pass \
  --fail-metric reliability_fault_injected=100% \
  --fail-metric reliability_retry_observed=100% \
  --fail-metric reliability_no_duplicate_side_effects=100% \
  --fail-metric reliability_fault_recovered=100%
```

Available profiles are shown by shell completion for `--fault-profile` and
cover HTTP 408/429/500, connection drops, truncated streams, and empty streams.
The `after-tool-*` forms wait until a request contains a tool result, making
them the strongest exactly-once check. Such a run deliberately fails closed if
the model never invokes a tool: the requested fault was not exercised, so there
is no recovery evidence to score.

Reliability results include non-sensitive proxy counters and journal-derived
recovery evidence. Request bodies, authorization headers, and API keys are not
written to the result file.

## Fixture contracts

Every scenario declares (implicitly or via `delivered_state`) what its
verification commands do in the delivered, pre-agent state:

- `red` (default) — verification FAILS as delivered; the agent's job is to
  make it pass. A red fixture that already passes measures nothing.
- `green` — trap scenarios (e.g. `go_investigation_used_symbol`,
  `go_refactor_preserve_contract`): verification PASSES as delivered; the
  agent's job is to act without breaking it.

CI enforces this on every push:

```sh
go run ./cmd/gokin eval validate
```

Run it locally after adding or editing any fixture.

## Baseline runbook

Snapshot a baseline per provider (uses your configured API keys — this
spends real tokens; the full set is ~34 agent runs per provider). The
committed baselines currently predate 11 scenarios
(`go_wrong_layer_paging`, `go_refactor_order_contract`, `go_cross_pkg_classify`,
`go_wrong_layer_normalize`, `go_two_bugs_stats`, `go_iface_drift_contract`,
`go_preserve_render_guard`, `go_int_overflow_average`, `py_mutable_default_tally`,
`node_promise_order`, `go_enum_threading`) — regenerate them to capture those
rows. Check baseline coverage without spending provider tokens:

```sh
go run ./cmd/gokin eval baseline-audit \
  --input evals/coding/baselines/glm.jsonl \
  --input evals/coding/baselines/deepseek.jsonl \
  --input evals/coding/baselines/kimi.jsonl
```

The command fails closed on missing, unknown, or duplicate rows in any
provider/model cohort. To validate just the new set without a full run, scope
the provider run with repeated `--scenario` flags:

```sh
go build -o /tmp/gokin ./cmd/gokin

# deepseek and glm are the primary day-to-day providers — keep both green.
GOKIN_BIN=/tmp/gokin go run ./cmd/gokin eval run \
  --provider deepseek \
  --agent-command "$(pwd)/evals/coding/scripts/run-gokin-headless.sh" \
  --output evals/coding/baselines/deepseek.jsonl

# glm defaults to glm-5.2 (the whole glm-5.x line aliases to it on Z.AI);
# pin --model so the baseline stays reproducible if the default later moves.
GOKIN_BIN=/tmp/gokin go run ./cmd/gokin eval run \
  --provider glm --model glm-5.2 \
  --agent-command "$(pwd)/evals/coding/scripts/run-gokin-headless.sh" \
  --output evals/coding/baselines/glm.jsonl

GOKIN_BIN=/tmp/gokin go run ./cmd/gokin eval run \
  --provider kimi \
  --agent-command "$(pwd)/evals/coding/scripts/run-gokin-headless.sh" \
  --output evals/coding/baselines/kimi.jsonl
```

Each provider needs its key in the environment for the run (the headless
adapter inherits it): `GOKIN_DEEPSEEK_KEY`, `GOKIN_GLM_KEY`, `GOKIN_KIMI_KEY`.

Commit `evals/coding/baselines/*.jsonl`. After any prompt/tool/routing
change, re-run for the affected provider into `.gokin/evals/results.jsonl`
and compare:

```sh
go run ./cmd/gokin eval report \
  --input .gokin/evals/results.jsonl \
  --baseline evals/coding/baselines/deepseek.jsonl \
  --max-regression 2%
go run ./cmd/gokin eval diagnose \
  --input .gokin/evals/results.jsonl \
  --baseline evals/coding/baselines/deepseek.jsonl
```

## Nightly improvement loop

Inside gokin, let the loop iterate while you sleep (self-paced):

```text
/loop run the coding evals for deepseek and glm, diagnose the weakest shared
metric, make the smallest prompt or tool-output change that addresses it,
re-run the affected scenarios for BOTH providers, and report the delta against
evals/coding/baselines/deepseek.jsonl and evals/coding/baselines/glm.jsonl
```

deepseek and glm are the two providers to keep healthy — prefer changes that
help both, and never regress one to lift the other. Each iteration should land
at most ONE change so the delta stays attributable.
