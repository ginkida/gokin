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
go run ./cmd/gokin eval run --provider kimi --provider glm --provider minimax --agent-command 'gokin --prompt {{prompt}}'
```

The runner writes JSONL results and scores agent evidence from `.gokin/execution_journal.jsonl`, including tool calls, files read, files edited, verification commands, and false file claims.
