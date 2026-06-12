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

The runner writes JSONL results and scores agent evidence from `.gokin/execution_journal.jsonl`, including tool calls, files read, files edited, verification commands, and false file claims.

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
spends real tokens; the full set is ~23 agent runs per provider):

```sh
go build -o /tmp/gokin ./cmd/gokin

GOKIN_BIN=/tmp/gokin go run ./cmd/gokin eval run \
  --provider deepseek \
  --agent-command "$(pwd)/evals/coding/scripts/run-gokin-headless.sh" \
  --output evals/coding/baselines/deepseek.jsonl

GOKIN_BIN=/tmp/gokin go run ./cmd/gokin eval run \
  --provider kimi \
  --agent-command "$(pwd)/evals/coding/scripts/run-gokin-headless.sh" \
  --output evals/coding/baselines/kimi.jsonl
```

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
/loop run the coding evals for deepseek, diagnose the weakest metric, make
the smallest prompt or tool-output change that addresses it, re-run the
affected scenarios, and report the delta against evals/coding/baselines/deepseek.jsonl
```

Each iteration should land at most ONE change so the delta stays
attributable.
