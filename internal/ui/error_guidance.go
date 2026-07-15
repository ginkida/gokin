package ui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/rivo/uniseg"
)

// ErrorGuidance provides actionable suggestions for common errors.
type ErrorGuidance struct {
	Pattern     *regexp.Regexp // Compiled regex to match error
	Title       string         // User-friendly title
	Suggestions []string       // What user can try
	Command     string         // Relevant command hint (optional)
}

// errorGuidancePatterns contains known error patterns with guidance.
//
// Order matters: patterns are checked in order, first match wins. MCP-specific
// patterns live at the top because their error strings (e.g. "permission
// denied", "no such file or directory") also match the later generic
// filesystem/permission patterns — without priority ordering users would see
// "File Not Found" instead of the more actionable "MCP Server Command Not Found".
var errorGuidancePatterns = []ErrorGuidance{
	// ─── MCP-specific patterns (first so they win over generic file/permission errors) ───
	{
		Pattern:     regexp.MustCompile(`(?i)failed to start MCP server.*(executable file not found|no such file or directory)`),
		Title:       "MCP Server Command Not Found",
		Suggestions: []string{"The command you gave /mcp add isn't on $PATH", "Install the server binary (e.g. `npx -y @mcp/server-...`), or use an absolute path", "Run /mcp list to see servers currently configured"},
		Command:     "/mcp list",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)failed to start MCP server.*(permission denied|fork/exec.*not permitted)`),
		Title:       "MCP Server Not Executable",
		Suggestions: []string{"The command path exists but isn't executable", "Check `chmod +x` on the binary, or verify your user has permission", "On macOS, verify the binary isn't quarantined"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(initialize failed|initialization failed).*(timeout|deadline)`),
		Title:       "MCP Server Did Not Initialize",
		Suggestions: []string{"The server process started but never responded to the MCP handshake", "Check the server's own logs — it may be crashing on startup", "Try running the command in a shell to see stderr directly"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(protocol version|unsupported protocol|protocolVersion).*(mismatch|not supported|incompatible)`),
		Title:       "MCP Protocol Mismatch",
		Suggestions: []string{"This server speaks an MCP protocol version gokin doesn't support yet", "Check if there's a newer gokin release", "Some servers accept a protocol version flag — consult the server's docs"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(MCP server.*connect|mcp.*transport.*(failed|closed)|mcp.*not connected)`),
		Title:       "MCP Connection Failed",
		Suggestions: []string{"The MCP server isn't reachable right now", "Run /mcp status to see which servers are healthy", "Try /mcp refresh NAME, or /mcp remove + /mcp add to rebuild the connection"},
		Command:     "/mcp status",
	},
	// ─── Specific network/auth patterns (must come before the generic
	// "Connection Failed" / "Access Denied" patterns below) ───
	//
	// TLS / certificate errors — common with corporate proxies that intercept
	// HTTPS using a private CA, or with stale system trust stores.
	{
		Pattern:     regexp.MustCompile(`(?i)(x509:.*(certificate|signed by unknown|expired|not.*valid|not.trusted)|tls:.*(handshake|bad certificate)|certificate verify failed|self.signed certificate)`),
		Title:       "TLS / Certificate Error",
		Suggestions: []string{"The provider's TLS certificate isn't trusted by your system", "If behind a corporate proxy: ask IT for the proxy CA bundle and add it to your system trust store", "On macOS check Keychain; on Linux check /etc/ssl/certs/ca-certificates.crt"},
		Command:     "",
	},
	// Git index/ref locks — typical after a SIGKILL or crash interrupted a git
	// operation. The cure is just to remove the lock file. Pattern matches
	// any *.lock under .git/ so it catches refs/heads/<branch>.lock too,
	// not just index.lock and HEAD.lock (an earlier draft had alternation
	// over (index|HEAD|.*ref) which silently missed branch ref locks).
	{
		Pattern:     regexp.MustCompile(`(?i)(unable to create|could not lock|fatal:.*).*\.git/[^'"\s]*\.lock`),
		Title:       "Git Lock File Stuck",
		Suggestions: []string{"A previous git command was killed mid-operation and left a stale lock", "Check no other git is running (ps aux | grep git), then remove the lock file shown in the error", "Common ones: rm .git/index.lock or rm .git/refs/heads/*.lock"},
		Command:     "",
	},
	// DNS / hostname resolution — distinct from the generic "network error" so
	// the user knows it's their resolver, not the remote service. Comes
	// BEFORE the "Connection Failed" pattern (which also matches "no such
	// host") so users get the more actionable hint.
	{
		Pattern:     regexp.MustCompile(`(?i)(no such host|name resolution|dns lookup failed|getaddrinfo|EAI_NONAME|ENOTFOUND)`),
		Title:       "DNS Resolution Failed",
		Suggestions: []string{"The hostname couldn't be resolved", "Check your network connection and DNS (try `ping 1.1.1.1` then `ping example.com`)", "If on a VPN, the DNS may need to route through it — verify VPN is up"},
		Command:     "",
	},
	// SSH-specific connection errors. Comes before the generic
	// "Connection Failed" / "Access Denied" patterns since "Permission
	// denied (publickey)" and ":22: connection refused" both match
	// generic ones with less actionable hints.
	{
		Pattern:     regexp.MustCompile(`(?i)(ssh:.*(handshake|authentication)|denied.*\(publickey\)|publickey.*(denied|rejected)|host key.*(verification|mismatch|unknown)|:22.*connection refused|connection refused.*:22)`),
		Title:       "SSH Connection Failed",
		Suggestions: []string{"Verify host/port/user are correct in your SSH config", "If publickey denied: confirm your key is added to the remote ~/.ssh/authorized_keys (and ssh-add for the local agent)", "If host key mismatch: the remote was reinstalled — remove the stale entry from ~/.ssh/known_hosts"},
		Command:     "",
	},
	// ─── Generic patterns ───
	{
		Pattern: regexp.MustCompile(`model_round_timeout`),
		Title:   "Model Round Timeout",
		Suggestions: []string{
			"A single model round hit the hard safety cap",
			"Increase tools.model_round_timeout for heavy reasoning/tool chains",
			"Try a narrower prompt to reduce round duration",
		},
	},
	{
		Pattern: regexp.MustCompile(`http_timeout`),
		Title:   "Provider HTTP Timeout",
		Suggestions: []string{
			"The provider did not return headers/data in time",
			"Increase api.retry.http_timeout or provider-specific override",
			"Check network stability and provider status",
		},
	},
	{
		Pattern: regexp.MustCompile(`stream idle timeout`),
		Title:   "Stream Stalled",
		Suggestions: []string{
			"The model stopped sending data mid-response",
			"This is usually a temporary server issue — try again",
			"If persistent, increase stream_idle_timeout in config",
		},
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(deadline exceeded|timeout|context deadline)`),
		Title:       "Request Timed Out",
		Suggestions: []string{"Check your network connection", "Try a simpler request", "The API server may be overloaded - wait and retry"},
		Command:     "",
	},
	{
		// \b429\b — same convention as \b401\b/\b500\b: bare "429" matched
		// "429ms latency" and "sent 1429 requests" too, both common in
		// streaming logs. Keep the rate-limit phrases unbounded since they
		// don't have the false-positive risk.
		Pattern:     regexp.MustCompile(`(?i)(rate limit|\b429\b|too many requests)`),
		Title:       "Rate Limit Reached",
		Suggestions: []string{"Wait a moment before trying again", "Reduce request frequency", "Consider upgrading your API plan"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(connection refused|no such host|network unreachable|dial tcp)`),
		Title:       "Connection Failed",
		Suggestions: []string{"Check your internet connection", "Verify API endpoint in config", "Check if firewall is blocking the connection"},
		Command:     "/status",
	},
	{
		// \b401\b prevents "401ms" (latency text) from matching. Aligns
		// with the `\b500\b` rule enforced in the 5xx pattern below.
		Pattern:     regexp.MustCompile(`(?i)(unauthorized|\b401\b|invalid.*key|api.*key.*invalid|authentication_error)`),
		Title:       "Authentication Failed",
		Suggestions: []string{"Your API key is missing, invalid, or expired", "Run `/login <provider>` to replace it in the masked key-entry prompt", "Verify the key at the provider's dashboard"},
		Command:     "/login",
	},
	{
		// \b403\b — bare "403" matched "403ms" / "5403 retries". Same
		// rationale as \b401\b above.
		Pattern:     regexp.MustCompile(`(?i)(forbidden|\b403\b|permission denied)`),
		Title:       "Access Denied",
		Suggestions: []string{"Your API key doesn't have access to this model or endpoint", "Verify the model is available on your provider plan", "Check the provider dashboard for model access settings"},
		Command:     "",
	},
	{
		// Covers both raw quota errors and HTTP 402 Payment Required, which
		// some providers return for billing issues. The polished version
		// previously sat at the bottom of the file as dead code (first-
		// match wins); merged in here.
		// "limit (exceeded|exhausted|reached)" + "usage limit" cover the GLM
		// coding-plan cap wordings ("weekly/monthly limit exhausted", "Usage
		// limit reached for 5 hour") — a field report showed the GLM weekly
		// cap rendering as a bare truncated line because none of the old
		// alternatives matched it.
		// NOTE: no bare "limit reached" alternative — "max tokens limit
		// reached" must fall through to the Context Window pattern below
		// (first-match wins); usage-cap wordings are qualified instead.
		Pattern:     regexp.MustCompile(`(?i)(quota|limit (exceeded|exhausted)|(usage|weekly|monthly|daily|hourly) limit|resource exhausted|insufficient.*credit|billing|payment.*required|\b402\b)`),
		Title:       "Provider Limit Reached",
		Suggestions: []string{"Your account has hit a usage cap or billing issue", "Wait for the limit to reset, or check the provider dashboard", "Switch to a different provider with /provider or /model"},
		Command:     "/provider",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)\b400\b.*model.*not.*found`),
		Title:       "Provider API Temporarily Unavailable",
		Suggestions: []string{"The provider returned a transient error — try sending your message again", "If persistent, switch provider with /provider or check /health"},
		Command:     "/health",
	},
	{
		// Covers vendor variations: "model not found", "unknown model",
		// "model_not_found", "model does not exist". Polished message
		// previously sat at the bottom of the file as dead code.
		Pattern:     regexp.MustCompile(`(?i)(invalid.*model|unknown.*model|model.*not found|model.*does not exist|model_not_found)`),
		Title:       "Model Not Available",
		Suggestions: []string{"The selected model is unavailable or not supported for your account", "Run /model to pick a supported one", "Check the provider's model list"},
		Command:     "/model",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(context.*(length|window|limit|too long|exceed)|maximum context|prompt is too long|token.*limit|max.*tokens)`),
		Title:       "Context Window Exceeded",
		Suggestions: []string{"The conversation is too long for this model", "Run /clear to start fresh, or /compact to summarize older messages", "Switch to a model with a larger context window via /model"},
		Command:     "/compact",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(file.*not.*found|no such file|ENOENT)`),
		Title:       "File Not Found",
		Suggestions: []string{"Check the file path (typo?)", "Use /pwd to verify current directory", "Open /browse to locate the file"},
		Command:     "/browse",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(permission.*denied|EACCES|cannot.*write)`),
		Title:       "File Permission Error",
		Suggestions: []string{"Check permissions: ls -la <path>", "Fix with: chmod u+rw <path>", "If system file: check ownership or use sudo"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(command.*not.*found|executable.*not.*found)`),
		Title:       "Command Not Found",
		Suggestions: []string{"Check the command is installed", "Verify it's in your PATH", "Install the required tool"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(git.*not.*found|not.*git.*repository)`),
		Title:       "Git Error",
		Suggestions: []string{"Initialize a git repository with 'git init'", "Check you're in the right directory", "Install git if not available"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(json.*parse|invalid.*json|unexpected.*token)`),
		Title:       "Invalid Response Format",
		Suggestions: []string{"The API returned an unexpected response", "Try the request again", "Report this issue if it persists"},
		Command:     "",
	},
	{
		// Internal "done-gate": the agent refused to finalize because its own
		// verification checks (build/vet/tests) still fail. This is NOT a
		// content-policy issue — it used to mis-match the broad "blocked"
		// pattern below and render a scary "flagged by content filters" card.
		// Sits before Content Policy and the Go patterns so it wins.
		Pattern:     regexp.MustCompile(`(?i)(done[ _-]?gate|required checks are still failing|no verification checks are available)`),
		Title:       "Quality Checks Didn't Pass",
		Suggestions: []string{"The agent stopped before finalizing because build/vet/test checks still fail", "Review the failing checks above and fix the underlying errors", "Re-run once fixed, or adjust the done-gate in config if a check doesn't apply"},
		Command:     "",
	},
	{
		// Genuine content-policy / safety-filter rejections only. Must NOT match
		// the bare word "blocked"/"safety"/"harmful" — a build that is "blocked",
		// a "safety check", "harmful input sanitised", etc. are not policy hits.
		// Require real content-policy context adjacent to the keyword.
		Pattern:     regexp.MustCompile(`(?i)(content[ _-]?(policy|filter|moderation)|safety[ _-]?(filter|policy|system|setting)|flagged[^\n]{0,30}(content|safety|filter)|(content|response|completion|output|generation)[^\n]{0,20}blocked|blocked[^\n]{0,20}(content|safety|policy|filter|guideline)|harmful[ _-]?content)`),
		Title:       "Content Policy",
		Suggestions: []string{"Your request was flagged by content filters", "Rephrase your request", "Review content guidelines"},
		Command:     "",
	},
	// Go-specific errors
	{
		Pattern:     regexp.MustCompile(`(?i)(undefined:|cannot refer to unexported|imported and not used|declared and not used)`),
		Title:       "Go Compilation Error",
		Suggestions: []string{"Check for typos in variable/function names", "Ensure all imports are used", "Remove unused variables or use _ placeholder"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(cannot use .* as .* in|incompatible type|type mismatch)`),
		Title:       "Go Type Error",
		Suggestions: []string{"Check type assertions and conversions", "Verify function signatures match expected types", "Use explicit type conversion if needed"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(go\.mod.*requires|module .* found .* but does not contain|no required module provides)`),
		Title:       "Go Module Error",
		Suggestions: []string{"Run 'go mod tidy' to fix dependencies", "Check go.mod for correct module path", "Run 'go get <package>' to add missing dependency"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(FAIL.*Test|--- FAIL:|panic.*test|test.*timed out)`),
		Title:       "Go Test Failure",
		Suggestions: []string{"Run the failing test in isolation: go test -run TestName -v ./pkg/", "Check for race conditions: go test -race ./...", "Look for test fixtures or env dependencies"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(go build.*:.*cannot|build constraints exclude|cgo.*not supported)`),
		Title:       "Go Build Constraint Error",
		Suggestions: []string{"Check build tags and GOOS/GOARCH settings", "If cgo: install C compiler or set CGO_ENABLED=0", "Verify the target platform supports the dependency"},
		Command:     "",
	},
	// Rust/Cargo errors
	{
		Pattern:     regexp.MustCompile(`(?i)(cargo.*error|error\[E\d+\]|cannot find.*crate)`),
		Title:       "Rust Compilation Error",
		Suggestions: []string{"Check the error code for detailed explanation: rustc --explain EXXXX", "Run 'cargo check' for faster feedback", "Ensure dependencies are correct in Cargo.toml"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(failed to resolve.*dependencies|perhaps a crate was updated|version.*yanked)`),
		Title:       "Cargo Dependency Error",
		Suggestions: []string{"Run 'cargo update' to refresh dependencies", "Check Cargo.toml version constraints", "Clear cache: cargo clean && cargo build"},
		Command:     "",
	},
	// Python-specific errors
	{
		Pattern:     regexp.MustCompile(`(?i)(ModuleNotFoundError|ImportError|No module named)`),
		Title:       "Python Import Error",
		Suggestions: []string{"Install the missing package with pip", "Check virtual environment is activated", "Verify the module name is spelled correctly"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(IndentationError|TabError|unexpected indent)`),
		Title:       "Python Indentation Error",
		Suggestions: []string{"Check for mixed tabs and spaces", "Use consistent indentation (4 spaces recommended)", "Run your editor's auto-format"},
		Command:     "",
	},
	// Node.js-specific errors
	{
		Pattern:     regexp.MustCompile(`(?i)(Cannot find module|MODULE_NOT_FOUND|ERR_MODULE_NOT_FOUND)`),
		Title:       "Node Module Not Found",
		Suggestions: []string{"Run 'npm install' to install dependencies", "Check package.json for the module", "Verify the import path is correct"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(ENOSPC|no space left|disk quota exceeded)`),
		Title:       "Disk Space Error",
		Suggestions: []string{"Free up disk space", "Clean build caches and node_modules", "Check disk usage with 'df -h'"},
		Command:     "",
	},
	// Docker errors
	{
		Pattern:     regexp.MustCompile(`(?i)(Cannot connect to the Docker daemon|docker.*not running)`),
		Title:       "Docker Not Running",
		Suggestions: []string{"Start Docker Desktop or Docker daemon", "Check Docker service status: systemctl status docker", "Verify Docker installation: docker --version"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(docker:.*command not found|compose.*command not found)`),
		Title:       "Docker Not Installed",
		Suggestions: []string{"Install Docker: https://docs.docker.com/get-docker/", "If using docker compose plugin, update Docker to latest version", "Check PATH includes Docker binary location"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(Error response from daemon.*pull|manifest.*not found|no matching manifest|image.*not found)`),
		Title:       "Docker Image Not Found",
		Suggestions: []string{"Check the image name and tag for typos", "Verify the image exists in the registry: docker search <name>", "Check your registry authentication: docker login"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(Error response from daemon.*Conflict|container name.*already in use)`),
		Title:       "Docker Container Name Conflict",
		Suggestions: []string{"Remove the existing container: docker rm <name>", "Use 'docker compose down' to clean up", "Use a different container name"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(Bind for 0\.0\.0\.0:\d+.*failed|port is already allocated)`),
		Title:       "Docker Port Conflict",
		Suggestions: []string{"Another container or process is using this port", "Check what's using the port: lsof -i :<port>", "Change the port mapping in docker-compose.yml", "Stop conflicting containers: docker compose down"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(mount.*denied|volume.*permission denied|error while creating mount)`),
		Title:       "Docker Volume/Mount Error",
		Suggestions: []string{"Check that the host path exists and is readable", "Verify file permissions on the mounted directory", "On macOS/Windows, ensure the path is shared in Docker settings"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(service.*failed to build|failed to solve|executor failed running)`),
		Title:       "Docker Build Failed",
		Suggestions: []string{"Check Dockerfile syntax and base image availability", "Review the build output for the specific error", "Try building with --no-cache: docker compose build --no-cache"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(network.*not found|could not find.*network)`),
		Title:       "Docker Network Error",
		Suggestions: []string{"Create the network: docker network create <name>", "Use 'docker compose up' to auto-create networks", "List existing networks: docker network ls"},
		Command:     "",
	},
	// Memory errors
	{
		Pattern:     regexp.MustCompile(`(?i)(out of memory|OOM|heap.*exhausted|allocation failed)`),
		Title:       "Out of Memory",
		Suggestions: []string{"Reduce batch size or data volume", "Increase memory limits", "Check for memory leaks in your code"},
		Command:     "",
	},
	// Port/address errors
	{
		Pattern:     regexp.MustCompile(`(?i)(address already in use|EADDRINUSE|port.*already|bind.*failed)`),
		Title:       "Port Already In Use",
		Suggestions: []string{"Another process is using this port", "Find the process: lsof -i :<port>", "Use a different port or kill the blocking process"},
		Command:     "",
	},
	// Server errors (5xx) — word boundaries so "500ms" doesn't trigger.
	// The 4xx auth/quota/model patterns above already cover their specific
	// status codes; this catches everything else in the 5xx range plus the
	// vendor-specific "overloaded" / "temporarily unavailable" phrasings.
	{
		Pattern:     regexp.MustCompile(`(?i)(\b5\d\d\b.*(error|server|gateway|unavailable|timeout)|internal server error|bad gateway|service unavailable|gateway timeout|overloaded|temporarily unavailable)`),
		Title:       "Provider Server Error",
		Suggestions: []string{"The provider is having issues — not your fault", "Retry logic already tried automatically", "Switch providers with /model, or try again in a few minutes"},
		Command:     "",
	},
}

// GetErrorGuidance returns guidance for an error message, or nil if no match.
func GetErrorGuidance(errMsg string) *ErrorGuidance {
	for _, g := range errorGuidancePatterns {
		if g.Pattern.MatchString(errMsg) {
			return &g
		}
	}
	return nil
}

// FormatErrorWithGuidance formats an error with helpful guidance. Errors
// are wrapped in styles.ErrorBox (a rounded red border) so they stand out
// from regular streaming output — users kept missing actionable suggestions
// when errors blended into plain text.
func FormatErrorWithGuidance(styles *Styles, errMsg string) string {
	return FormatErrorWithGuidanceWidth(styles, errMsg, 0)
}

// FormatErrorWithGuidanceWidth is FormatErrorWithGuidance with the terminal
// width threaded in so the message wraps to the card instead of being
// amputated. termWidth <= 0 falls back to a sane default.
func FormatErrorWithGuidanceWidth(styles *Styles, errMsg string, termWidth int) string {
	// Guidance patterns match the RAW error (they reference provider
	// wording); only the DISPLAYED text is humanized below.
	guidance := GetErrorGuidance(errMsg)

	errorStyle := lipgloss.NewStyle().Foreground(ColorError).Bold(true)
	msgStyle := lipgloss.NewStyle().Foreground(ColorText)
	titleStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
	suggestionStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	cmdStyle := lipgloss.NewStyle().Foreground(ColorSecondary)
	markerStyle := lipgloss.NewStyle().Foreground(ColorDim)

	if termWidth > 0 && termWidth < 12 {
		// At one-digit widths a bordered guidance card degenerates into hundreds
		// of mostly-indent rows. Keep the persistent transcript record compact:
		// identity, cause, and the best recovery action all remain available.
		var rows []string
		for _, line := range splitDisplayToken("Error", termWidth) {
			rows = append(rows, errorStyle.Render(line))
		}
		for _, line := range displayErrorLines(errMsg, termWidth) {
			rows = append(rows, msgStyle.Render(line))
		}
		if guidance != nil {
			recovery := guidance.Command
			if recovery != "" {
				recovery = "Try: " + recovery
			} else if len(guidance.Suggestions) > 0 {
				recovery = guidance.Suggestions[0]
			}
			for _, line := range displayErrorLines(recovery, termWidth) {
				rows = append(rows, cmdStyle.Render(line))
			}
		}
		return strings.Join(rows, "\n")
	}

	contentWidth := 76
	bordered := styles != nil
	if termWidth > 0 {
		// Border + horizontal padding cost four cells. At very small widths the
		// chrome would consume most of the row, so keep the error readable and
		// render it borderless until the terminal grows again.
		if styles != nil && termWidth >= 16 {
			contentWidth = max(min(termWidth-8, 96), 1)
		} else {
			contentWidth = max(min(termWidth, 96), 1)
			bordered = false
		}
	}

	var body strings.Builder

	// Error message: machine wrapper prefixes stripped, WRAPPED to the card
	// width — never tail-truncated. The tail is where the actionable half of
	// a provider error lives ("… switch provider with /provider"); the old
	// 100-rune cut amputated exactly that (field report: the GLM weekly-cap
	// message ended mid-word at "switch p...").
	msgWidth := contentWidth - 9 // 9 = lipgloss.Width("✗ Error: ")
	if msgWidth < 8 {
		// The prefix will wrap onto its own row. Give the actual error the full
		// row instead of reducing it to one-character fragments.
		msgWidth = contentWidth
	}
	msgLines := displayErrorLines(errMsg, msgWidth)
	body.WriteString(errorStyle.Render(MessageIcons["error"]+" Error: ") + msgStyle.Render(msgLines[0]))
	for _, line := range msgLines[1:] {
		body.WriteString("\n")
		body.WriteString(msgStyle.Render("         " + line))
	}

	if guidance != nil {
		// Title
		body.WriteString("\n")
		body.WriteString(markerStyle.Render(MessageIcons["hint"]+" ") + titleStyle.Render(guidance.Title))

		// Suggestions
		for _, suggestion := range guidance.Suggestions {
			body.WriteString("\n")
			body.WriteString(markerStyle.Render("  "+MessageIcons["info"]+" ") + suggestionStyle.Render(suggestion))
		}

		// Command hint
		if guidance.Command != "" {
			body.WriteString("\n")
			body.WriteString(markerStyle.Render("  ") + cmdStyle.Render("Try: "+guidance.Command))
		}
	}

	// Guidance strings and provider errors are external text. Wrap the whole
	// styled body as a final safety net so titles, suggestions and commands all
	// obey the same terminal-cell budget as the error itself.
	renderedBody := wrapText(body.String(), contentWidth)

	// Fall back to plain/borderless rendering when styles aren't provided or
	// the terminal is too narrow for useful box chrome.
	if styles == nil || !bordered {
		return renderedBody
	}
	return styles.ErrorBox.Render(renderedBody)
}

// machineErrorPrefixRe matches internal wrapper prefixes ("model response
// error (other): ", "function response error (stream_idle_timeout): ") that
// classify the error for RETRY LOGIC but mean nothing to a user — "(other)"
// is literally the taxonomy's miss bucket. Stripped for display only; the
// raw error still drives guidance matching and logging.
var machineErrorPrefixRe = regexp.MustCompile(`^(?i)(model response error|function response error|request failed|agent error)(\s*\([^)]{0,40}\))?:\s*`)

// machineErrorAnywhereRe is the non-anchored sibling of machineErrorPrefixRe
// for surfaces that COMPOSE their own prefixes around an error ("Loop x #3:
// Iteration error: model response error (other): …") — the wrapper can sit
// mid-string there. The phrases are distinctive retry-taxonomy strings, so a
// global replace is safe for display text.
var machineErrorAnywhereRe = regexp.MustCompile(`(?i)\b(model response error|function response error)(\s*\([^)]{0,40}\))?:\s*`)

// stripMachineErrorWrappers removes machine wrapper taxonomy from a display
// string wherever it appears. Display-only — raw errors keep driving
// guidance matching and logs.
func stripMachineErrorWrappers(s string) string {
	return machineErrorAnywhereRe.ReplaceAllString(s, "")
}

// displayErrorLines prepares an error for the user-facing card: strips
// machine wrapper prefixes, collapses newlines, and word-wraps to the given
// width. Always returns at least one line. Capped at 5 lines with the
// overflow elided in the MIDDLE — the head carries the cause, the tail the
// action, and both must survive.
func displayErrorLines(errMsg string, width int) []string {
	msg := strings.Join(strings.Fields(safeTerminalDisplayText(errMsg)), " ")
	for range 3 { // wrappers nest ("model response error: request failed: …")
		stripped := machineErrorPrefixRe.ReplaceAllString(msg, "")
		if stripped == msg {
			break
		}
		msg = stripped
	}
	if msg == "" {
		msg = "unknown error"
	}
	width = max(width, 1)

	var lines []string
	words := strings.Fields(msg)
	current := ""
	for _, w := range words {
		if current != "" {
			candidate := current + " " + w
			if lipgloss.Width(candidate) <= width {
				current = candidate
				continue
			}
			lines = append(lines, current)
			current = ""
		}

		chunks := splitDisplayToken(w, width)
		if len(chunks) == 0 {
			continue
		}
		if len(chunks) > 1 {
			lines = append(lines, chunks[:len(chunks)-1]...)
		}
		current = chunks[len(chunks)-1]
	}
	if current != "" {
		lines = append(lines, current)
	}
	if len(lines) == 0 {
		lines = []string{msg}
	}

	const maxLines = 5
	if len(lines) > maxLines {
		head := lines[:2]
		tail := lines[len(lines)-2:]
		lines = append(append(append([]string{}, head...), "…"), tail...)
	}
	return lines
}

// splitDisplayToken hard-wraps a whitespace-free token by grapheme cluster.
// This is the fallback for URLs, paths and provider identifiers that have no
// natural word boundary. Every returned row fits width terminal cells.
func splitDisplayToken(token string, width int) []string {
	if token == "" {
		return nil
	}
	width = max(width, 1)
	graphemes := uniseg.NewGraphemes(token)
	var chunks []string
	var current strings.Builder
	used := 0
	for graphemes.Next() {
		cluster := graphemes.Str()
		clusterWidth := lipgloss.Width(cluster)
		if used > 0 && used+clusterWidth > width {
			chunks = append(chunks, current.String())
			current.Reset()
			used = 0
		}
		if clusterWidth > width {
			// A two-cell emoji cannot be represented in a one-cell terminal.
			// Consume the intact cluster but render a bounded omission marker.
			chunks = append(chunks, truncateForWidth(cluster, width))
			continue
		}
		current.WriteString(cluster)
		used += clusterWidth
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}
	return chunks
}

// GetCompactHint returns a short actionable hint for an error.
// Returns empty string if no matching guidance is found.
func GetCompactHint(errMsg string) string {
	guidance := GetErrorGuidance(errMsg)
	if guidance == nil {
		return ""
	}
	// Prefer the explicit recovery command over the first explanatory sentence.
	// Compact surfaces only have room for one hint, so repeating the diagnosis
	// ("your key is invalid") while hiding the fix ("/login") is not useful.
	if guidance.Command != "" {
		return truncateForWidth("Try: "+guidance.Command, 40)
	}
	if len(guidance.Suggestions) == 0 {
		return ""
	}
	// Fall back to the first suggestion, shortened by rendered cells so CJK
	// and emoji cannot overflow a compact status surface.
	return truncateForWidth(guidance.Suggestions[0], 40)
}
