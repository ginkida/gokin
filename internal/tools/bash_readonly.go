package tools

import (
	"path/filepath"
	"strings"
)

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
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	// Output redirection writes files; command substitution / backticks can
	// hide arbitrary programs. Reject outright — a quoted '>' also rejects,
	// which only costs a hint, never correctness.
	if strings.Contains(cmd, ">") || strings.Contains(cmd, "$(") || strings.Contains(cmd, "`") {
		return false
	}

	for _, segment := range splitShellSegments(cmd) {
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			return false
		}
		if !readOnlyProgram(fields) {
			return false
		}
	}
	return true
}

// splitShellSegments naively splits on &&, ||, ; and | — quotes are NOT
// honored, which is fine here: a separator inside quotes produces a garbage
// segment that fails the allowlist (a safe false negative).
func splitShellSegments(cmd string) []string {
	for _, sep := range []string{"&&", "||", ";", "|"} {
		cmd = strings.ReplaceAll(cmd, sep, "\x00")
	}
	var out []string
	for seg := range strings.SplitSeq(cmd, "\x00") {
		if s := strings.TrimSpace(seg); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// readOnlyPrograms are unconditionally-safe leading programs.
var readOnlyPrograms = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "wc": true,
	"grep": true, "rg": true, "ag": true, "find": true, "fd": true,
	"echo": true, "printf": true, "pwd": true, "which": true,
	"whoami": true, "stat": true, "du": true, "df": true, "ps": true,
	"date": true, "uname": true, "file": true, "diff": true, "cmp": true,
	"sort": true, "uniq": true, "cut": true, "tr": true, "true": true,
	"test": true, "[": true, "cd": true, "gofmt": true,
}

// readOnlyGitSubcommands are git verbs that never mutate the repository.
// Deliberately excludes `branch` (creates on extra arg), `remote` (add/set),
// `stash` — unknown verbs classify as mutating.
var readOnlyGitSubcommands = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true, "blame": true,
	"ls-files": true, "rev-parse": true, "describe": true, "grep": true,
	"shortlog": true,
}

func readOnlyProgram(fields []string) bool {
	prog := filepath.Base(fields[0])
	if readOnlyPrograms[prog] {
		return true
	}
	switch prog {
	case "git":
		// Skip -c/-C option pairs and bare flags to find the subcommand.
		rest := fields[1:]
		for len(rest) > 0 {
			switch {
			case rest[0] == "-c" || rest[0] == "-C":
				if len(rest) < 2 {
					return false
				}
				rest = rest[2:]
			case strings.HasPrefix(rest[0], "-"):
				rest = rest[1:]
			default:
				return readOnlyGitSubcommands[rest[0]]
			}
		}
		return false
	case "go":
		if len(fields) < 2 {
			return false
		}
		switch fields[1] {
		case "version", "list", "vet", "build", "test":
			// build/test write only the build cache — idempotent in effect,
			// and they are THE validation-loop commands worth hinting on.
			return true
		case "env":
			// `go env -w` persists configuration — reject any flagged form.
			for _, f := range fields[2:] {
				if strings.HasPrefix(f, "-") {
					return false
				}
			}
			return true
		}
		return false
	}
	return false
}
