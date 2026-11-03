package security

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"gokin/internal/logging"
)

// MinSecretLiteralLength is the shortest value the literal registry accepts.
// Shorter values (ports, booleans, short words) would redact ordinary output.
const MinSecretLiteralLength = 12

// maxSecretLiterals bounds the registry so a hostile environment with
// thousands of token-shaped variables cannot make every redaction pass slow.
const maxSecretLiterals = 256

// Process-wide literal registry. Pattern redaction only catches secrets with a
// recognizable shape (AKIA…, ghp_…, key=value). A provider key loaded from the
// config file, or an opaque token inherited from the parent process (for
// example a Claude Code session token), has no shape the patterns know — so the
// exact values gokin can SEE are registered here and every SecretRedactor
// scrubs them, no matter which tool or path surfaced them (env, bash output,
// a test that prints its environment, a file that happens to contain one).
var (
	secretLiteralsMu sync.RWMutex
	secretLiterals   = map[string]struct{}{}
	// sortedLiterals is rebuilt on registration, longest first, so a literal
	// that contains another is replaced whole.
	sortedLiterals []string
)

// RegisterSecretLiteral adds an exact value that every SecretRedactor must
// replace with [REDACTED]. Values shorter than MinSecretLiteralLength, values
// containing whitespace, and whitelisted placeholder words are ignored.
// It reports whether the value was registered.
func RegisterSecretLiteral(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < MinSecretLiteralLength || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	// A path (SECRETS_DIR=/run/secrets, SSH_AUTH_SOCK=/tmp/…) names where a
	// secret lives, not the secret; scrubbing it would mangle ordinary output.
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~") {
		return false
	}
	if safeValues[strings.ToLower(value)] {
		return false
	}
	secretLiteralsMu.Lock()
	defer secretLiteralsMu.Unlock()
	if _, ok := secretLiterals[value]; ok {
		return true
	}
	if len(secretLiterals) >= maxSecretLiterals {
		logging.Warn("secret literal registry full; value not registered for redaction", "cap", maxSecretLiterals)
		return false
	}
	secretLiterals[value] = struct{}{}
	sortedLiterals = make([]string, 0, len(secretLiterals))
	for literal := range secretLiterals {
		sortedLiterals = append(sortedLiterals, literal)
	}
	sort.Slice(sortedLiterals, func(i, j int) bool {
		if len(sortedLiterals[i]) != len(sortedLiterals[j]) {
			return len(sortedLiterals[i]) > len(sortedLiterals[j])
		}
		return sortedLiterals[i] < sortedLiterals[j]
	})
	return true
}

// UnregisterSecretLiteral removes a value registered by a test.
func UnregisterSecretLiteral(value string) {
	secretLiteralsMu.Lock()
	defer secretLiteralsMu.Unlock()
	if _, ok := secretLiterals[strings.TrimSpace(value)]; !ok {
		return
	}
	delete(secretLiterals, strings.TrimSpace(value))
	next := make([]string, 0, len(sortedLiterals))
	for _, literal := range sortedLiterals {
		if literal != strings.TrimSpace(value) {
			next = append(next, literal)
		}
	}
	sortedLiterals = next
}

// resetSecretLiteralsForTest clears the registry. Tests only.
func resetSecretLiteralsForTest() {
	secretLiteralsMu.Lock()
	defer secretLiteralsMu.Unlock()
	secretLiterals = map[string]struct{}{}
	sortedLiterals = nil
}

func redactSecretLiterals(text string) string {
	secretLiteralsMu.RLock()
	literals := sortedLiterals
	secretLiteralsMu.RUnlock()
	for _, literal := range literals {
		if strings.Contains(text, literal) {
			text = strings.ReplaceAll(text, literal, "[REDACTED]")
		}
	}
	return text
}

// IsSensitiveEnvName reports whether an environment variable NAME suggests its
// value is a credential. It is used to decide which inherited values to
// register as literals and which variables the env tool must never reveal.
func IsSensitiveEnvName(name string) bool {
	upper := strings.ToUpper(name)
	// GIT_AUTHOR_NAME/EMAIL carry identity, not credentials; redacting them
	// would scrub every git log line.
	upper = strings.ReplaceAll(upper, "AUTHOR", "")
	// Toolchain configuration (GOPRIVATE=github.com/acme, GOFLAGS, …) is not a
	// credential; registering it would scrub every import path in output.
	if strings.HasPrefix(upper, "GO") && !strings.Contains(upper, "TOKEN") && !strings.Contains(upper, "PASSWORD") {
		return false
	}
	for _, marker := range []string{
		"PASSWORD", "PASSWD", "SECRET", "TOKEN", "API_KEY", "APIKEY", "API-KEY",
		"PRIVATE_KEY", "PRIVATEKEY", "CREDENTIAL", "AUTH", "SESSION", "COOKIE", "DSN", "_KEY",
	} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return strings.HasSuffix(upper, "KEY")
}

// RegisterSensitiveEnvironment registers the values of every variable in env
// (KEY=value form, as from os.Environ) whose name suggests a credential.
// It returns how many values were registered.
func RegisterSensitiveEnvironment(env []string) int {
	registered := 0
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !IsSensitiveEnvName(name) {
			continue
		}
		if RegisterSecretLiteral(value) {
			registered++
		}
	}
	return registered
}

// RegisterProcessSecrets registers the sensitive part of this process's own
// environment. Call once at startup, after configuration is loaded.
func RegisterProcessSecrets() int {
	return RegisterSensitiveEnvironment(os.Environ())
}

// highConfidenceSecretPatterns are formats that are credentials wherever they
// appear. Outbound tools REFUSE on a match, so this list is deliberately
// narrower than the output redactor's: key=value heuristics and loose prefixes
// (a bare `sk-` matches "task-management") stay output-only.
var highConfidenceSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}`),
	regexp.MustCompile(`(?:^|[^A-Za-z0-9])sk-(?:ant-|proj-)?[A-Za-z0-9_-]{32,}`),
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bsk_(?:live|test)_[0-9a-zA-Z]{24,}`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]{8,}\.eyJ[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{20,}`),
	regexp.MustCompile(`\bxox[baprs]-[0-9]{10,}-[0-9]{10,}-[a-zA-Z0-9]{20,}`),
	regexp.MustCompile(`-----BEGIN (?:[A-Z]+ )?PRIVATE KEY-----`),
	envStyleCredentialPattern,
}

// ContainsHighConfidenceSecret reports whether text carries a registered
// secret literal or a credential in an unambiguous format. Used where a match
// REFUSES an action (outbound requests), so false positives must be rare.
func ContainsHighConfidenceSecret(text string) bool {
	if text == "" {
		return false
	}
	if redactSecretLiterals(text) != text {
		return true
	}
	for _, pattern := range highConfidenceSecretPatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}
