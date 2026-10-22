package tools

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Keep shell state out of command stdout. In particular, printed filenames
// and marker-like output must not become authority to run directory hooks.
type bashCwdProbe struct{ path string }

func newBashCwdProbe() (*bashCwdProbe, error) {
	file, err := os.CreateTemp("", "gokin-bash-cwd-*")
	if err != nil {
		return nil, err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return &bashCwdProbe{path: path}, nil
}

func (p *bashCwdProbe) wrap(command string) string {
	quoted := "'" + strings.ReplaceAll(filepath.ToSlash(p.path), "'", "'\\''") + "'"
	write := `__gokin_cwd_value="$(builtin pwd -P; builtin printf .)"; __gokin_cwd_value="${__gokin_cwd_value%?}"; __gokin_cwd_value="${__gokin_cwd_value%$'\n'}"; builtin printf '%s\0' "$__gokin_cwd_value" > ` + quoted + ` || :`
	// The trap covers explicit exit and errexit; the trailing write also works
	// when user code replaces EXIT with its own cleanup handler.
	trap := `__gokin_cwd_rc=$?; if [[ "$BASH_SUBSHELL" == 0 ]]; then ` + write + `; fi; exit "$__gokin_cwd_rc"`
	// DEBUG records a changed directory before exec replaces the shell. The
	// BASH_SUBSHELL also works in macOS's Bash 3.2, which lacks BASHPID.
	debug := `if [[ "$BASH_SUBSHELL" == 0 && "${PWD-}" != "$__gokin_cwd_last" ]]; then ` + write + `; __gokin_cwd_last="${PWD-}"; fi`
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	return "__gokin_cwd_last=''\ntrap " + quote(trap) + " EXIT\ntrap " + quote(debug) + " DEBUG\n" + command + "\n__gokin_cwd_rc=$?\n" + write + "\nexit \"$__gokin_cwd_rc\""
}

func (p *bashCwdProbe) directory() string {
	file, err := os.Open(p.path)
	if err != nil {
		return ""
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 32769))
	if err != nil || len(data) > 32768 || len(data) == 0 || data[len(data)-1] != 0 {
		return ""
	}
	value := string(data[:len(data)-1])
	if strings.ContainsRune(value, 0) || !filepath.IsAbs(value) {
		return ""
	}
	return value
}

func (p *bashCwdProbe) close() { _ = os.Remove(p.path) }
