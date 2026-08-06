#!/usr/bin/env sh
set -eu

# Runs one headless gokin turn inside an eval workspace.
# The eval runner provides GOKIN_EVAL_* environment variables.
# Set GOKIN_BIN to a binary path when gokin is not on PATH
# (for example: GOKIN_BIN="$PWD/gokin" after `go build ./cmd/gokin`).

bin="${GOKIN_BIN:-gokin}"
provider="${GOKIN_EVAL_PROVIDER:-}"
model="${GOKIN_EVAL_MODEL:-}"
prompt="${GOKIN_EVAL_PROMPT:-}"
base_url="${GOKIN_EVAL_BASE_URL:-}"

if [ -z "$prompt" ]; then
  echo "GOKIN_EVAL_PROMPT is required" >&2
  exit 64
fi

# Evals run unattended in disposable temp workspaces, and headless permission
# prompts are refused rather than auto-approved (there is no operator to ask).
# Without this every scenario that edits a file scores zero, which is what
# silently happened to the whole suite once that refusal landed.
set -- "$bin" --headless --permission-mode bypassPermissions --prompt "$prompt"
if [ -n "$provider" ]; then
  set -- "$@" --provider "$provider"
fi
if [ -n "$model" ]; then
  set -- "$@" --model "$model"
fi
if [ -n "$base_url" ]; then
  set -- "$@" --base-url "$base_url"
fi

exec "$@"
