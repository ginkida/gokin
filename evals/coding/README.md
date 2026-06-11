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
go run ./cmd/gokin eval run --provider kimi --provider glm --provider minimax --agent-command './evals/coding/scripts/run-gokin-headless.sh'
```

The headless script uses `gokin` from PATH by default; point `GOKIN_BIN` at a
freshly built binary when iterating locally:

```sh
go build -o /tmp/gokin ./cmd/gokin
GOKIN_BIN=/tmp/gokin go run ./cmd/gokin eval run --provider kimi --agent-command './evals/coding/scripts/run-gokin-headless.sh'
```

Run a provider/model matrix:

```sh
go run ./cmd/gokin eval run --provider kimi --model kimi-for-coding --agent-command './evals/coding/scripts/run-gokin-headless.sh'
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
