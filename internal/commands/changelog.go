package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ChangelogCommand shows a compact list of the most recent gokin
// releases. Pairs with /whats-new (which shows a single release in
// full): users skim /changelog to find a tag of interest, then run
// /whats-new <tag> for the body.
//
// `/changelog` defaults to the last 5 releases. `/changelog 10` shows
// the last 10. The cap (50) avoids accidental floods if a user types
// something silly like `/changelog 9999`.
type ChangelogCommand struct{}

func (c *ChangelogCommand) Name() string        { return "changelog" }
func (c *ChangelogCommand) Description() string { return "List recent releases (compact)" }
func (c *ChangelogCommand) Usage() string {
	return `/changelog        - Last 5 releases
/changelog 10     - Last 10 releases (max 50)`
}

func (c *ChangelogCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "list",
		Priority: 7, // sits with /whats-new + /update + /restart
		HasArgs:  true,
		ArgHint:  "[count]",
	}
}

const (
	changelogDefaultCount = 5
	changelogMaxCount     = 50
)

func (c *ChangelogCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Configuration not available.", nil
	}

	repo := strings.TrimSpace(cfg.Update.GitHubRepo)
	if repo == "" {
		return "GitHub repo not configured. Set `update.github_repo: owner/name` in config.yaml.", nil
	}

	count := changelogDefaultCount
	if len(args) > 0 {
		n, err := strconv.Atoi(strings.TrimSpace(args[0]))
		if err != nil || n < 1 {
			return fmt.Sprintf("Invalid count %q — pass a positive integer (e.g. /changelog 10).", args[0]), nil
		}
		if n > changelogMaxCount {
			n = changelogMaxCount
		}
		count = n
	}

	releases, err := fetchRecentReleases(ctx, repo, count)
	if err != nil {
		return fmt.Sprintf("Could not fetch releases: %v\n\nFallback: https://github.com/%s/releases",
			err, repo), nil
	}
	if len(releases) == 0 {
		return fmt.Sprintf("No releases published yet at https://github.com/%s/releases", repo), nil
	}

	// Read version from cfg directly — App.GetVersion() returns the
	// same field but the fake AppInterface used in tests hardcodes a
	// stub there. Using cfg keeps test setup natural ("set the version
	// you want the running binary to be" via cfg.Version).
	return formatChangelog(releases, repo, cfg.Version), nil
}

// fetchRecentReleases hits GitHub's "list releases" endpoint with a
// per_page param so we don't pull megabytes of unwanted history. The
// endpoint returns newest-first, exactly what we want for the
// compact view.
func fetchRecentReleases(ctx context.Context, repo string, count int) ([]releaseNotes, error) {
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", whatsNewBaseURL, repo, count)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "gokin-changelog")

	resp, err := whatsNewHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("github API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var releases []releaseNotes
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return releases, nil
}

// formatChangelog renders the compact list. One row per release with:
// tag, optional "← you" marker on the running version, date, and the
// title (or first body line, if title is empty). Bullet-style so the
// list reads as a scannable timeline.
func formatChangelog(releases []releaseNotes, repo, currentVersion string) string {
	currentTag := normalizeChangelogTag(currentVersion)

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Recent releases · %s\n\n", repo)

	for _, r := range releases {
		date := ""
		if t, err := time.Parse(time.RFC3339, r.PublishedAt); err == nil {
			date = t.Format("2006-01-02")
		}

		title := strings.TrimSpace(r.Name)
		if title == "" {
			// Fall back to first non-empty body line.
			for line := range strings.SplitSeq(r.Body, "\n") {
				if strings.TrimSpace(line) != "" {
					title = strings.TrimSpace(line)
					break
				}
			}
		}
		if title == "" {
			title = "(no description)"
		}

		marker := ""
		if r.TagName == currentTag {
			marker = "  ← you"
		}

		fmt.Fprintf(&sb, "- **%s**", r.TagName)
		if date != "" {
			fmt.Fprintf(&sb, " · %s", date)
		}
		sb.WriteString(marker)
		sb.WriteString("\n  ")
		sb.WriteString(truncateChangelogTitle(title, 80))
		sb.WriteString("\n")
	}

	sb.WriteString("\n_Use `/whats-new <tag>` for the full notes of any release._\n")
	return sb.String()
}

// normalizeChangelogTag mirrors WhatsNewCommand's normalization so the
// "← you" marker matches whether the binary's version field is bare
// semver or already prefixed.
func normalizeChangelogTag(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		return "v" + v
	}
	return v
}

// truncateChangelogTitle keeps each row to one terminal line on
// typical 100-col TUIs. Cuts at a word boundary when possible so we
// don't slice mid-word.
func truncateChangelogTitle(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	cut := string(runes[:max])
	if idx := strings.LastIndexByte(cut, ' '); idx > len(cut)-15 {
		cut = cut[:idx]
	}
	return cut + "…"
}

// Compile-time check.
var _ Command = (*ChangelogCommand)(nil)
