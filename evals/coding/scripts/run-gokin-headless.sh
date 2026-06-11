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

if [ -z "$prompt" ]; then
  echo "GOKIN_EVAL_PROMPT is required" >&2
  exit 64
fi

set -- "$bin" --headless --prompt "$prompt"
if [ -n "$provider" ]; then
  set -- "$@" --provider "$provider"
fi
if [ -n "$model" ]; then
  set -- "$@" --model "$model"
fi

exec "$@"
