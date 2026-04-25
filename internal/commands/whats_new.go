package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gokin/internal/config"
)

// WhatsNewCommand fetches the GitHub release notes for the running
// version and shows them in the TUI. Closes the upgrade-feedback loop:
// users who run /update install + /restart no longer have to context-
// switch to the browser to see what changed.
//
// Defaults to the version baked into the binary, but accepts an
// explicit tag arg (`/whats-new v0.72.0`) so users can review past
// releases without restarting on a different binary.
type WhatsNewCommand struct{}

func (c *WhatsNewCommand) Name() string        { return "whats-new" }
func (c *WhatsNewCommand) Description() string { return "Show release notes for the current (or specified) version" }
func (c *WhatsNewCommand) Usage() string {
	return `/whats-new           - Show notes for the running version
/whats-new v0.74.0   - Show notes for a specific tag`
}

func (c *WhatsNewCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "info",
		Priority: 7, // groups with /update + /restart
		HasArgs:  true,
		ArgHint:  "[tag]",
	}
}

// whatsNewHTTPClient is overridable from tests so we don't actually
// hit GitHub during CI runs. Production binds it to a sane default
// with a 10s timeout — release-note fetching shouldn't hang the TUI.
var whatsNewHTTPClient = &http.Client{Timeout: 10 * time.Second}

// whatsNewBaseURL is the GitHub API endpoint stem. Tests can swap it
// for an httptest.Server.
var whatsNewBaseURL = "https://api.github.com"

func (c *WhatsNewCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Configuration not available.", nil
	}

	repo := strings.TrimSpace(cfg.Update.GitHubRepo)
	if repo == "" {
		return "GitHub repo not configured. Set `update.github_repo: owner/name` in config.yaml.", nil
	}

	// Default to the running binary's version when no arg is given.
	tag := strings.TrimSpace(app.GetVersion())
	if len(args) > 0 {
		tag = strings.TrimSpace(args[0])
	}
	if tag == "" {
		return "Could not determine current version. Pass a tag explicitly: /whats-new v0.74.0", nil
	}
	// GitHub release tags are conventionally `vX.Y.Z`, but the binary's
	// version field stores the bare `X.Y.Z`. Normalize so both forms
	// work whether the user typed it in or we picked it up automatically.
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}

	notes, err := fetchReleaseNotes(ctx, repo, tag)
	if err != nil {
		return fmt.Sprintf("Could not fetch release notes for %s: %v\n\nFallback: https://github.com/%s/releases/tag/%s",
			tag, err, repo, tag), nil
	}

	return formatReleaseNotes(notes, repo, tag), nil
}

// releaseNotes is the subset of the GitHub Releases API response we use.
// JSON shape: https://docs.github.com/en/rest/releases/releases#get-a-release-by-tag-name
type releaseNotes struct {
	Name        string `json:"name"`
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
}

// fetchReleaseNotes hits the GitHub Releases API for the given tag.
// Surfaces 404 explicitly because that's the most common error
// (typo in the tag, or release wasn't published yet) and the generic
// "API error" message would leave the user wondering.
func fetchReleaseNotes(ctx context.Context, repo, tag string) (*releaseNotes, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", whatsNewBaseURL, repo, tag)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// User-Agent is required by the GitHub API; without it the server
	// 403s with a vague error.
	req.Header.Set("User-Agent", "gokin-whatsnew")

	resp, err := whatsNewHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no release published with this tag")
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("github API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var notes releaseNotes
	if err := json.NewDecoder(resp.Body).Decode(&notes); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &notes, nil
}

// formatReleaseNotes turns the API payload into a TUI-friendly block.
// We keep the original markdown body untouched (the streaming output
// already does basic markdown rendering) and just add a header with
// the tag, name, and a link back to the release page.
func formatReleaseNotes(n *releaseNotes, repo, tag string) string {
	var sb strings.Builder

	title := strings.TrimSpace(n.Name)
	if title == "" {
		title = n.TagName
	}
	sb.WriteString(fmt.Sprintf("# %s\n\n", title))

	if n.PublishedAt != "" {
		// Format YYYY-MM-DD from the ISO-8601 the API returns.
		date := n.PublishedAt
		if t, err := time.Parse(time.RFC3339, n.PublishedAt); err == nil {
			date = t.Format("2006-01-02")
		}
		sb.WriteString(fmt.Sprintf("Published %s · https://github.com/%s/releases/tag/%s\n\n", date, repo, tag))
	}

	body := strings.TrimSpace(n.Body)
	if body == "" {
		sb.WriteString("_No release notes provided._\n")
	} else {
		sb.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// Compile-time check.
var _ Command = (*WhatsNewCommand)(nil)

// silence-the-import: linter-friendly use to keep `config` referenced
// even if the Execute path's reference is moved around in refactors.
var _ = config.UpdateConfig{}
