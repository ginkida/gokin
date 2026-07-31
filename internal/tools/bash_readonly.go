package tools

import "gokin/internal/permission"

// readOnlyBashCommand reports whether a bash command is PURE INSPECTION —
// every pipeline/sequence segment starts with an allowlisted read-only
// program and nothing redirects output. Used by the stagnation guard to give
// an inspection loop (`git status && git diff --stat` repeated while the
// model "thinks out loud") graceful recovery hints instead of killing the
// whole turn — the same courtesy read/grep/glob earned in v0.86.7, which a
// field report showed bash inspection commands still lacked.
//
// CONSERVATIVE BY DESIGN: a false negative merely keeps the old hard-abort
// behavior; a false positive would grant loop hints to a mutating command.
// Unknown programs, redirections, and command substitution all classify as
// NOT read-only.
func readOnlyBashCommand(cmd string) bool {
	return permission.IsReadOnlyBashCommand(cmd)
}
