package permission

import (
	"strings"
	"unicode/utf8"
)

// IsSideEffectFreeBashCommand classifies commands eligible to bypass mutation
// safeguards. Unlike the advisory read-only classifier, execution is forbidden
// unless the complete command and its options are understood.
// This classifies shell syntax and arguments, not ambient program configuration
// or executable provenance; callers still own those execution boundaries.
func IsSideEffectFreeBashCommand(command string) bool {
	// No expansion, continuation lines, comments, compound shell syntax, or
	// control characters. Quoting below resolves literal words, never code.
	if len(command) > 64<<10 || !utf8.ValidString(command) || strings.ContainsAny(command, "\x00\r\n$`") {
		return false
	}
	p := inspectionParser{text: command}
	var words []string
	redirected := false
	for {
		p.space()
		if p.pos == len(p.text) {
			return inspectionProgram(words, redirected)
		}
		switch p.text[p.pos] {
		case '&', '|':
			if !inspectionProgram(words, redirected) {
				return false
			}
			if strings.HasPrefix(p.text[p.pos:], "&&") {
				p.pos += 2
			} else if p.text[p.pos] == '|' && !strings.HasPrefix(p.text[p.pos:], "||") && !strings.HasPrefix(p.text[p.pos:], "|&") {
				p.pos++
			} else {
				return false
			}
			words, redirected = nil, false
		case '>':
			if !p.redirect("") {
				return false
			}
			redirected = true
		default:
			word, plain, ok := p.word()
			if !ok {
				return false
			}
			if plain && decimalWord(word) && p.pos < len(p.text) && p.text[p.pos] == '>' {
				if !p.redirect(word) {
					return false
				}
				redirected = true
			} else {
				words = append(words, word)
			}
		}
	}
}

type inspectionParser struct {
	text string
	pos  int
}

func (p *inspectionParser) space() {
	for p.pos < len(p.text) && (p.text[p.pos] == ' ' || p.text[p.pos] == '\t') {
		p.pos++
	}
}

// word implements only literal POSIX shell words: adjacent quoted/unquoted
// fragments and backslash escapes. Unsupported syntax always fails closed.
func (p *inspectionParser) word() (string, bool, bool) {
	var word strings.Builder
	started, plain := false, true
	for p.pos < len(p.text) {
		c := p.text[p.pos]
		if strings.ContainsRune(" \t&|><", rune(c)) {
			break
		}
		if c < ' ' || strings.ContainsRune(";(){}*?[]~!#", rune(c)) {
			return "", false, false
		}
		started = true
		p.pos++
		switch c {
		case '\\':
			plain = false
			if p.pos == len(p.text) {
				return "", false, false
			}
			word.WriteByte(p.text[p.pos])
			p.pos++
		case '\'', '"':
			plain = false
			closed := false
			for p.pos < len(p.text) {
				q := p.text[p.pos]
				p.pos++
				if q == c {
					closed = true
					break
				}
				if c == '"' && q == '\\' && p.pos < len(p.text) && strings.ContainsRune("\\\"", rune(p.text[p.pos])) {
					q = p.text[p.pos]
					p.pos++
				}
				word.WriteByte(q)
			}
			if !closed {
				return "", false, false
			}
		default:
			word.WriteByte(c)
		}
	}
	return word.String(), plain, started
}

func (p *inspectionParser) redirect(fd string) bool {
	p.pos++ // >
	appendMode := p.pos < len(p.text) && p.text[p.pos] == '>'
	if appendMode {
		p.pos++
	}
	if p.pos < len(p.text) && p.text[p.pos] == '&' {
		p.pos++
		word, plain, ok := p.word()
		return !appendMode && fd == "2" && ok && plain && word == "1"
	}
	p.space()
	word, _, ok := p.word()
	return ok && word == "/dev/null"
}

func decimalWord(word string) bool {
	if word == "" {
		return false
	}
	for _, c := range word {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func inspectionProgram(words []string, redirected bool) bool {
	if len(words) == 0 {
		return false
	}
	args := words[1:]
	var switches, values string
	switch words[0] {
	case "ls":
		switches = "a A b B c C d f F g G h H i k l L m n N o p q Q r R s S t u U v w x 1 --all --almost-all --directory --human-readable --recursive --numeric-uid-gid --color --classify"
	case "cat":
		switches = "A b e E n s t T u v --show-all --number-nonblank --show-ends --number --squeeze-blank --show-tabs --show-nonprinting"
	case "head", "tail":
		switches, values = "q v --quiet --silent --verbose", "n c --lines --bytes"
	case "wc":
		switches = "c l m w L --bytes --lines --chars --words --max-line-length"
	case "pwd":
		switches = "L P --logical --physical"
	case "stat":
		switches, values = "f L n x --file-system --dereference --terse", "c t --format --printf"
	case "file":
		switches = "b h i k L N r s --brief --mime --mime-type --mime-encoding --dereference --no-dereference --keep-going --raw --special-files"
	case "echo":
		return !redirected
	case "printf":
		if redirected || len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return false
		}
		// Bash printf %n assigns variables (array subscripts can execute code).
		// Keep only literal output, %% and %s; no -v, numeric or time formats.
		for i := 0; i < len(args[0]); i++ {
			if args[0][i] == '%' {
				i++
				if i == len(args[0]) || args[0][i] != '%' && args[0][i] != 's' {
					return false
				}
			}
		}
		return true
	case "grep", "egrep", "fgrep":
		switches = "a b c E F h H i I l L n o q r R s v w x z --text --byte-offset --count --extended-regexp --fixed-strings --no-filename --with-filename --ignore-case --files-with-matches --files-without-match --line-number --only-matching --quiet --recursive --no-messages --invert-match --word-regexp --line-regexp --null-data"
		values = "A B C d D e f m --after-context --before-context --context --directories --devices --regexp --file --max-count --include --exclude --exclude-dir --exclude-from --label --binary-files"
	case "rg":
		switches = "a b c F h H i I l L n N o p P q s S u U v w x --text --byte-offset --count --fixed-strings --with-filename --no-filename --ignore-case --files-with-matches --files-without-match --line-number --no-line-number --only-matching --pcre2 --quiet --case-sensitive --smart-case --unrestricted --multiline --invert-match --word-regexp --line-regexp --null-data --files --hidden --no-ignore --no-config --follow --stats --heading --no-heading"
		values = "A B C e f g j m M r t T --after-context --before-context --context --regexp --file --glob --iglob --threads --max-count --max-columns --type --type-not --color --encoding --sort --sortr --max-depth --ignore-file --replace"
	case "find":
		switches = "-H -L -P -print -print0 -ls -empty -readable -writable -executable -true -false -a -and -o -or -not -depth -xdev -mount -prune -quit"
		values = "-name -iname -path -ipath -regex -iregex -type -xtype -maxdepth -mindepth -size -mtime -mmin -atime -amin -ctime -cmin -perm -user -group -uid -gid -links -inum -newer"
		// find expressions use multi-character single-dash operators, and do
		// not share the other programs' short bundles or -- operand convention.
		for i := 0; i < len(args); i++ {
			if strings.HasPrefix(args[i], "-") {
				if optionIn(values, args[i]) {
					i++
					if i == len(args) {
						return false
					}
				} else if !optionIn(switches, args[i]) {
					return false
				}
			}
		}
		return true
	case "git":
		return inspectionGit(args)
	default:
		return false
	}
	_, ok := inspectionOptions(args, switches, values)
	return ok
}

func optionIn(options, option string) bool {
	return strings.Contains(" "+options+" ", " "+option+" ")
}

// inspectionOptions rejects unknown flags, including abbreviations, and
// consumes option operands before checking further options. Return only the
// positional words, for commands whose operands can themselves mutate state.
func inspectionOptions(args []string, switches, values string) ([]string, bool) {
	var operands []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return append(operands, args[i+1:]...), true
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			operands = append(operands, arg)
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, value, inline := strings.Cut(arg, "=")
			if inline {
				if !optionIn(values, name) || !inspectionOptionValue(name, value) {
					return nil, false
				}
			} else if !optionIn(switches, name) {
				if !optionIn(values, name) || i+1 == len(args) {
					return nil, false
				}
				i++
				if strings.HasPrefix(args[i], "-") || !inspectionOptionValue(name, args[i]) {
					return nil, false
				}
			}
			continue
		}
		for j := 1; j < len(arg); j++ {
			flag := arg[j : j+1]
			if optionIn(values, flag) {
				value := arg[j+1:]
				if j+1 == len(arg) {
					i++
					if i == len(args) {
						return nil, false
					}
					value = args[i]
				}
				if strings.HasPrefix(value, "-") || !inspectionOptionValue(flag, value) {
					return nil, false
				}
				break
			}
			if !optionIn(switches, flag) {
				return nil, false
			}
		}
	}
	return operands, true
}

func inspectionOptionValue(option, value string) bool {
	switch option {
	case "n", "U", "--max-count", "--unified":
		return decimalWord(value)
	default:
		return true
	}
}

func inspectionGit(args []string) bool {
	if len(args) > 0 && args[0] == "--no-pager" {
		args = args[1:]
	}
	if len(args) == 0 {
		return false
	}
	var switches, values string
	switch args[0] {
	case "status":
		switches = "s b z --short --branch --porcelain --untracked-files --ignored"
		values = "u --porcelain --untracked-files --ignored --ignore-submodules"
	case "diff", "log", "show":
		switches = "p s w b z --patch --no-patch --stat --numstat --shortstat --name-only --name-status --summary --raw --check --no-ext-diff --no-textconv --no-color --oneline --decorate --no-decorate --all --reverse --first-parent --no-merges --merges --no-renames --ignore-space-at-eol --ignore-all-space --exit-code --quiet"
		values = "n U --max-count --unified --color --decorate --since --until --author --grep --date --diff-filter"
		// The conventional -1/-10 history limit is a single numeric option.
		filtered := make([]string, 0, len(args))
		for _, arg := range args[1:] {
			if len(arg) > 1 && arg[0] == '-' && decimalWord(arg[1:]) {
				continue
			}
			filtered = append(filtered, arg)
		}
		_, ok := inspectionOptions(filtered, switches, values)
		return ok
	case "blame":
		switches, values = "w p c s --porcelain --line-porcelain --no-textconv", "L --date"
	case "rev-parse":
		switches = "q --quiet --verify --show-toplevel --show-prefix --is-inside-work-tree --abbrev-ref --short --symbolic-full-name --end-of-options"
		values = "--short"
	case "ls-files":
		switches = "c d m o s t u v z --cached --deleted --modified --others --stage --unmerged --exclude-standard --eol"
		values = "x --exclude --exclude-from"
	case "branch":
		switches = "a r v --all --remotes --verbose --list --show-current"
		values = "--sort"
		operands, ok := inspectionOptions(args[1:], switches, values)
		if !ok || len(operands) > 0 && !containsInspectionOption(args[1:], "--list") {
			return false
		}
		return true
	default:
		return false
	}
	_, ok := inspectionOptions(args[1:], switches, values)
	return ok
}

func containsInspectionOption(args []string, option string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return false
		}
		if arg == option {
			return true
		}
		// The branch options with separate values must not turn a literal
		// --list operand (or a format string) into authority to list branches.
		if arg == "--format" || arg == "--sort" {
			i++
		}
	}
	return false
}
