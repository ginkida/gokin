package permission

import "strings"

// BashDanger is the ACTION-semantics danger tier for a bash command, used to
// complement the name-based RiskLevel. "bash" is uniformly RiskHigh, but `ls`
// and `git push --force` are worlds apart — the name-based level can't tell them
// apart, so a force-push got the same one-line "Execute command:" prompt as a
// directory listing, and ran silently whenever bash was configured to allow.
//
// gokin already KNEW these commands were dangerous (the verify-policy and
// isolated-workspace blocklists both flag git push / reset / clean), but that
// knowledge never reached the foreground permission decision. This classifier
// carries it there.
//
// Scope note: the truly-catastrophic class (rm -rf /, fork bombs, reverse shells,
// mkfs, dd-to-disk) is HARD-BLOCKED upstream in security.ValidateCommand and the
// executor's safety pre-flight, regardless of mode — those never reach here. This
// tier is about LEGITIMATE-but-irreversible / outward-facing / privilege-escalating
// commands the user should consciously confirm, not refuse outright.
type BashDanger int

const (
	// BashDangerNone — no irreversible/outward-facing action recognized.
	BashDangerNone BashDanger = iota
	// BashDangerElevated — irreversible, outward-facing, or privilege-escalating.
	// Must be consciously confirmed even when bash is otherwise set to allow.
	BashDangerElevated
)

// ClassifyBashCommand inspects a bash command string and returns its danger tier
// plus a human-readable reason (empty when BashDangerNone). Pattern-based and
// deliberately conservative — it errs toward NOT flagging (a missed flag costs a
// silent run, but a false flag trains the user to click through prompts, which is
// worse). The command is normalized (lowercased, whitespace collapsed) so spacing
// variants don't escape detection.
func ClassifyBashCommand(cmd string) (BashDanger, string) {
	n := strings.Join(strings.Fields(strings.ToLower(cmd)), " ")
	if n == "" {
		return BashDangerNone, ""
	}

	switch {
	case isGitPush(n) && hasShortOrLongForceFlag(n):
		return BashDangerElevated, "force-pushes to a remote — rewrites published history, very hard to undo"
	case isGitPush(n):
		return BashDangerElevated, "pushes commits to a remote — an external, hard-to-revert side effect"
	case strings.Contains(n, "git reset --hard"):
		return BashDangerElevated, "git reset --hard discards uncommitted work — irreversible"
	case strings.Contains(n, "git clean") && hasShortOrLongForceFlag(n):
		return BashDangerElevated, "git clean -f permanently deletes untracked files — irreversible"
	case pipesRemoteToShell(n):
		return BashDangerElevated, "pipes remote content straight into a shell — remote code execution risk"
	case usesSudo(n):
		return BashDangerElevated, "runs with elevated (root) privileges via sudo"
	case strings.Contains(n, "rm -rf") || strings.Contains(n, "rm -fr"):
		return BashDangerElevated, "recursively force-deletes files — irreversible"
	}
	return BashDangerNone, ""
}

// ClassifyBashArgs extracts the command from tool args and classifies it.
func ClassifyBashArgs(args map[string]any) (BashDanger, string) {
	cmd, _ := args["command"].(string)
	return ClassifyBashCommand(cmd)
}

// hasShortOrLongForceFlag reports whether any token is a force flag: --force,
// --force-with-lease, or a short bundle containing 'f' (-f, -fd, -fdx, -rf). In a
// `git push`/`git clean` context a short flag with 'f' means force.
func hasShortOrLongForceFlag(n string) bool {
	for _, f := range strings.Fields(n) {
		if f == "--force" || f == "--force-with-lease" {
			return true
		}
		if strings.HasPrefix(f, "-") && !strings.HasPrefix(f, "--") && strings.Contains(f, "f") {
			return true
		}
	}
	return false
}

// pipesRemoteToShell catches `curl ... | sh`, `wget ...|bash` and spacing/shell
// variants the exact-match hard-block in safety.go misses. Requires BOTH a remote
// fetch and a pipe into a shell so a plain `cat x | sh` of a local file isn't flagged.
func pipesRemoteToShell(n string) bool {
	if !strings.Contains(n, "curl") && !strings.Contains(n, "wget") {
		return false
	}
	for _, sh := range []string{"| sh", "|sh", "| bash", "|bash", "| zsh", "|zsh", "| dash", "|dash"} {
		if strings.Contains(n, sh) {
			return true
		}
	}
	return false
}

// isGitPush reports whether n invokes `git push`, tolerating git's global
// options between `git` and the subcommand (`git -C /repo push`, `git -c k=v
// push`, `git --no-pager push`) which a literal "git push" substring misses.
// It walks tokens after each `git`, skipping option flags (and the value of
// `-C`/`-c`/`--git-dir`/`--work-tree`/`--namespace`), and checks the first
// non-option token is `push` — so `git log --grep push` is NOT flagged.
func isGitPush(n string) bool {
	fields := strings.Fields(n)
	for i, f := range fields {
		if f != "git" {
			continue
		}
		j := i + 1
		for j < len(fields) {
			t := fields[j]
			switch {
			case t == "-c" || t == "-C" || t == "--git-dir" || t == "--work-tree" || t == "--namespace":
				j += 2 // option consumes its value
			case strings.HasPrefix(t, "-"):
				j++ // a valueless flag (e.g. --no-pager)
			default:
				goto check
			}
		}
	check:
		if j < len(fields) && fields[j] == "push" {
			return true
		}
	}
	return false
}

// usesSudo reports whether sudo runs as a command — at the start of a shell
// segment (after stripping VAR=VAL env-prefixes) or carried inside a shell `-c`
// wrapper. Avoids flagging "echo sudo" / "grep sudo file"; catches `sudo x`,
// `a && sudo x`, `FOO=1 sudo x`, and `bash -c '... sudo ...'`. Over-approximates
// the wrapper case (errs toward prompting), which is the safe direction.
func usesSudo(n string) bool {
	for _, seg := range splitOnShellSeparators(n) {
		fields := strings.Fields(seg)
		k := 0
		for k < len(fields) && isEnvAssignment(fields[k]) {
			k++
		}
		if k < len(fields) && fields[k] == "sudo" {
			return true
		}
	}
	if strings.Contains(n, "sudo") {
		for _, w := range []string{"bash -c", "sh -c", "zsh -c", "dash -c", "bash -lc", "sh -lc"} {
			if strings.Contains(n, w) {
				return true
			}
		}
	}
	return false
}

// splitOnShellSeparators splits an already-normalized command on top-level shell
// separators (&& || | ;). Longer operators are listed first so `||`/`&&` win
// over `|`.
func splitOnShellSeparators(n string) []string {
	repl := strings.NewReplacer("&&", "\x00", "||", "\x00", "|", "\x00", ";", "\x00")
	return strings.Split(repl.Replace(n), "\x00")
}

// isEnvAssignment reports whether tok is a NAME=VALUE shell env-assignment
// (so `FOO=1 sudo …` treats sudo as the command, not FOO).
func isEnvAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for i, r := range tok[:eq] {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z'):
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
