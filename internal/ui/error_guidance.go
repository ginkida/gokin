package ui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
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
		Pattern:     regexp.MustCompile(`(?i)(unauthorized|\b401\b|invalid.*key|api.*key.*invalid)`),
		Title:       "Authentication Failed",
		Suggestions: []string{"Check your API key is correct", "Regenerate your key at the provider's dashboard", "Run `/login <provider> <key>` to set a new key, or `gokin --setup` to re-run the onboarding wizard"},
		Command:     "/login",
	},
	{
		// \b403\b — bare "403" matched "403ms" / "5403 retries". Same
		// rationale as \b401\b above.
		Pattern:     regexp.MustCompile(`(?i)(forbidden|\b403\b|permission denied)`),
		Title:       "Access Denied",
		Suggestions: []string{"Check API key permissions", "Verify you have access to this model", "Contact API provider for access"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(quota|limit exceeded|resource exhausted)`),
		Title:       "Quota Exceeded",
		Suggestions: []string{"You've reached your usage limit", "Wait for quota reset or upgrade plan", "Use a more efficient model"},
		Command:     "",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)\b400\b.*model.*not.*found`),
		Title:       "Provider API Temporarily Unavailable",
		Suggestions: []string{"The provider returned a transient error — try sending your message again", "If persistent, switch provider with /provider or check /health"},
		Command:     "/health",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(invalid.*model|unknown.*model)`),
		Title:       "Model Not Found",
		Suggestions: []string{"Check the model name is correct", "List available models with /model", "The model may have been deprecated"},
		Command:     "/model",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(context.*too.*long|token.*limit|max.*tokens)`),
		Title:       "Context Too Long",
		Suggestions: []string{"Clear conversation history with /clear", "Use /compact to summarize context", "Break your request into smaller parts"},
		Command:     "/clear",
	},
	{
		Pattern:     regexp.MustCompile(`(?i)(file.*not.*found|no such file|ENOENT)`),
		Title:       "File Not Found",
		Suggestions: []string{"Check the file path (typo?)", "Use /pwd to verify current directory", "Search with /glob or /grep to locate it"},
		Command:     "",
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
		Pattern:     regexp.MustCompile(`(?i)(content.*policy|safety|blocked|harmful)`),
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
	// Context length / token overflow
	{
		Pattern:     regexp.MustCompile(`(?i)(context.*(length|window|limit|too long|exceed)|maximum context|prompt is too long|token.*limit)`),
		Title:       "Context Window Exceeded",
		Suggestions: []string{"The conversation is too long for this model", "Run /clear to start fresh, or /compact to summarize older messages", "Switch to a model with a larger context window via /model"},
		Command:     "/compact",
	},
	// Authentication / API key — use word boundaries on status codes so we
	// don't match "401ms latency" etc.
	{
		Pattern:     regexp.MustCompile(`(?i)(invalid api key|authentication failed|unauthorized|\b401\b|\b403\b.*forbidden|api key.*invalid|authentication_error)`),
		Title:       "Authentication Failed",
		Suggestions: []string{"Your API key is missing, invalid, or expired", "Run `/login <provider> <key>` with a fresh key, or check your config file", "Verify the key at the provider's dashboard"},
		Command:     "/login",
	},
	// Quota / billing
	{
		Pattern:     regexp.MustCompile(`(?i)(quota.*exceed|insufficient.*credit|billing|payment.*required|\b402\b)`),
		Title:       "Quota or Billing Issue",
		Suggestions: []string{"Your account has hit a usage cap or billing issue", "Check your provider dashboard for billing status", "Try a different provider via /model"},
		Command:     "",
	},
	// Server errors (5xx) — word boundaries so "500ms" doesn't trigger
	{
		Pattern:     regexp.MustCompile(`(?i)(\b5\d\d\b.*(error|server|gateway|unavailable|timeout)|internal server error|bad gateway|service unavailable|gateway timeout|overloaded|temporarily unavailable)`),
		Title:       "Provider Server Error",
		Suggestions: []string{"The provider is experiencing issues — this is not your fault", "The retry logic already tried automatically", "Switch providers with /model, or try again in a few minutes"},
		Command:     "",
	},
	// Model not found
	{
		Pattern:     regexp.MustCompile(`(?i)(model.*not found|unknown model|model.*does not exist|model_not_found)`),
		Title:       "Model Not Available",
		Suggestions: []string{"The selected model is unavailable or not supported for your account", "Run /model to pick a supported one", "Check the provider's supported model list"},
		Command:     "/model",
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
	guidance := GetErrorGuidance(errMsg)

	errorStyle := lipgloss.NewStyle().Foreground(ColorError).Bold(true)
	msgStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FECACA"))
	titleStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
	suggestionStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	cmdStyle := lipgloss.NewStyle().Foreground(ColorSecondary)
	markerStyle := lipgloss.NewStyle().Foreground(ColorDim)

	var body strings.Builder

	// Error message (first line inside the box)
	body.WriteString(errorStyle.Render(MessageIcons["error"]+" Error: ") + msgStyle.Render(truncateError(errMsg, 100)))

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

	// Fall back to plain rendering if styles isn't provided (tests,
	// telemetry, etc.) so callers without a Styles value still work.
	if styles == nil {
		return body.String()
	}
	return styles.ErrorBox.Render(body.String())
}

// truncateError truncates an error message to a maximum length.
func truncateError(msg string, maxLen int) string {
	// Remove newlines for single-line display
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.TrimSpace(msg)

	if runes := []rune(msg); len(runes) > maxLen {
		return string(runes[:maxLen-3]) + "..."
	}
	return msg
}

// GetCompactHint returns a short actionable hint for an error.
// Returns empty string if no matching guidance is found.
func GetCompactHint(errMsg string) string {
	guidance := GetErrorGuidance(errMsg)
	if guidance == nil || len(guidance.Suggestions) == 0 {
		return ""
	}
	// Return first suggestion, shortened if needed
	hint := guidance.Suggestions[0]
	if runes := []rune(hint); len(runes) > 40 {
		hint = string(runes[:37]) + "..."
	}
	return hint
}
