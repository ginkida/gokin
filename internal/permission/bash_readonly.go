package permission

import (
	"path/filepath"
	"regexp"
	"strings"
)

// IsReadOnlyBashCommand reports whether every shell segment is a conservative,
// allowlisted inspection command. Unknown programs, command substitution, and
// redirects to user files fail closed.
//
// This classifier is shared by dontAsk permission mode and the executor's
// stagnation recovery. Keeping one implementation prevents a command from
// being treated as harmless for loop recovery but mutating for permissions, or
// vice versa.
func IsReadOnlyBashCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	command = harmlessBashRedirectRE.ReplaceAllString(command, " ")
	if strings.Contains(command, ">") ||
		strings.Contains(command, "$(") ||
		strings.Contains(command, "`") {
		return false
	}

	for _, segment := range splitBashSegments(command) {
		fields := strings.Fields(segment)
		if len(fields) == 0 || !isReadOnlyBashProgram(fields) {
			return false
		}
	}
	return true
}

// IsReadOnlyBashArgs extracts and classifies a Bash tool invocation.
func IsReadOnlyBashArgs(args map[string]any) bool {
	command, _ := args["command"].(string)
	return IsReadOnlyBashCommand(command)
}

// harmlessBashRedirectRE matches redirections that cannot write a user file:
// any fd redirected to /dev/null and the stderr-to-stdout merge.
var harmlessBashRedirectRE = regexp.MustCompile(`(?:[012]?&?>>?\s*/dev/null|2>&1)`)

// splitBashSegments deliberately does not interpret quotes. A separator inside
// a quoted string therefore produces a safe false negative.
func splitBashSegments(command string) []string {
	for _, separator := range []string{"&&", "||", ";", "|"} {
		command = strings.ReplaceAll(command, separator, "\x00")
	}
	var segments []string
	for segment := range strings.SplitSeq(command, "\x00") {
		if segment = strings.TrimSpace(segment); segment != "" {
			segments = append(segments, segment)
		}
	}
	return segments
}

var readOnlyBashPrograms = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "wc": true,
	"grep": true, "rg": true, "ag": true, "find": true, "fd": true,
	"echo": true, "printf": true, "pwd": true, "which": true,
	"whoami": true, "stat": true, "du": true, "df": true, "ps": true,
	"date": true, "uname": true, "file": true, "diff": true, "cmp": true,
	"sort": true, "uniq": true, "cut": true, "tr": true, "true": true,
	"test": true, "[": true, "cd": true,
}

var readOnlyGitSubcommands = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true, "blame": true,
	"ls-files": true, "rev-parse": true, "describe": true, "grep": true,
	"shortlog": true,
}

// mutatingProgramFlags lists, per otherwise-read-only program, the options that
// turn it into a file mutator or an arbitrary-command launcher. Without this the
// allowlist would make `find . -delete` and `fd -x rm` "conservative read-only
// Bash" — which dontAsk mode auto-executes without a prompt.
var mutatingProgramFlags = map[string][]string{
	"find": {"-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprintf", "-fls"},
	"fd":   {"-x", "-X", "--exec", "--exec-batch"},
	"sort": {"-o", "--output"},
	"rg":   {"--pre", "--hostname-bin"},
}

// hasMutatingProgramFlag reports whether an allowlisted program was invoked in
// one of its mutating/executing forms. Both `--flag value` and `--flag=value`
// spellings count; short flags are matched exactly so `-output` style operands
// are not confused with them.
func hasMutatingProgramFlag(program string, args []string) bool {
	flags, ok := mutatingProgramFlags[program]
	if !ok {
		return false
	}
	for _, arg := range args {
		name := arg
		if index := strings.IndexByte(arg, '='); index > 0 {
			name = arg[:index]
		}
		for _, flag := range flags {
			if name == flag {
				return true
			}
		}
	}
	return false
}

func isReadOnlyBashProgram(fields []string) bool {
	program := filepath.Base(fields[0])
	if hasMutatingProgramFlag(program, fields[1:]) {
		return false
	}
	if readOnlyBashPrograms[program] {
		return true
	}
	switch program {
	case "git":
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
	case "gofmt":
		for _, field := range fields[1:] {
			if field == "-w" {
				return false
			}
		}
		return true
	case "go":
		if len(fields) < 2 {
			return false
		}
		switch fields[1] {
		case "version", "list", "vet", "build", "test":
			return true
		case "env":
			for _, field := range fields[2:] {
				if strings.HasPrefix(field, "-") {
					return false
				}
			}
			return true
		}
	}
	return false
}
