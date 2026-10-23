package plugins

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"gokin/internal/plugindeps"
	"gokin/internal/pluginpolicy"
)

func satisfiesRanges(version string, ranges []string) bool {
	for _, constraint := range ranges {
		if !plugindeps.Satisfies(version, constraint) {
			return false
		}
	}
	return true
}

func marketplaceRoot(market Marketplace) string {
	switch market.Source.Kind {
	case "directory":
		return market.Source.Path
	case "file":
		return filepath.Dir(filepath.Dir(market.Source.Path))
	default:
		return market.InstallLocation
	}
}

// Tag resolution pins the advertised object and archives its resolved commit;
// moving a tag is observed on the next resolve, never hidden by a version cache.
func (m *Manager) fetchConstrainedPlugin(ctx context.Context, id string, source Source, market Marketplace, entry Entry, stage string, opts InstallOptions) (rootResult string, commitResult string, versionResult string, fetchErr error) {
	constrained := false
	for _, value := range opts.versionRanges {
		constrained = constrained || value != ""
	}
	fetchCurrent := func() (string, string, string, error) {
		root, commit, err := m.fetchPlugin(ctx, source, market, entry, stage, opts)
		return root, commit, "", err
	}
	if !constrained {
		return fetchCurrent()
	}
	gitSource := source
	relative := source.Kind == "relative"
	if relative {
		switch market.Source.Kind {
		case "git", "github":
			gitSource = Source{Kind: "git-subdir", URL: pluginpolicy.GitURL(market.Source), Path: source.Path}
		case "directory", "file":
			root := marketplaceRoot(market)
			data, err := runCommand(ctx, root, "git", gitArgs("rev-parse", "--show-toplevel")...)
			if err != nil {
				return fetchCurrent()
			}
			repo := strings.TrimSpace(string(data))
			path, err := filepath.Rel(repo, filepath.Join(root, source.Path))
			if err != nil {
				return "", "", "", err
			}
			if path, err = safeRelative(filepath.ToSlash(path), true); err != nil {
				return "", "", "", err
			}
			gitSource = Source{Kind: "git-subdir", URL: (&url.URL{Scheme: "file", Path: repo}).String(), Path: filepath.ToSlash(path)}
		default:
			return fetchCurrent()
		}
		if gitSource.Path == "." {
			gitSource.Kind, gitSource.Path = "url", ""
		}
	}
	if gitSource.Kind != "github" && gitSource.Kind != "url" && gitSource.Kind != "git-subdir" {
		return fetchCurrent() // Non-git constraints are checked against plugin.json.
	}
	rawURL := pluginpolicy.GitURL(gitSource)
	// Tagged local repositories remain available offline.
	if !strings.HasPrefix(rawURL, "file://") {
		if err := requireCachedSource(ctx, gitSource); err != nil {
			return "", "", "", err
		}
	}
	_, marketName, _ := strings.Cut(id, "@")
	if opts.Background && !m.marketplaceRetryAllowed(ctx, marketName, market.Source) {
		return "", "", "", fmt.Errorf("marketplace %s retry deferred by backoff", marketName)
	}
	// Install owns the registry lock. Remember dependency network failures too,
	// including those reached through a cached marketplace with updates disabled.
	defer func() { _ = m.recordMarketplaceAttemptLocked(marketName, market.Source, fetchErr != nil) }()
	if err := validateGitURL(rawURL); err != nil {
		return "", "", "", err
	}
	name, _, _ := strings.Cut(id, "@")
	prefix := name + "--v"
	output, err := runCommand(ctx, stage, "git", gitArgs("ls-remote", "--tags", "--refs", rawURL, prefix+"*")...)
	if err != nil {
		return "", "", "", err
	}
	type release struct {
		tag, sha string
		version  *semver.Version
	}
	var releases []release
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !hexSHA.MatchString(fields[0]) || !strings.HasPrefix(fields[1], "refs/tags/"+prefix) {
			continue
		}
		tag := strings.TrimPrefix(fields[1], "refs/tags/")
		version, err := semver.StrictNewVersion(strings.TrimPrefix(tag, prefix))
		if err == nil && satisfiesRanges(version.String(), opts.versionRanges) {
			releases = append(releases, release{tag, fields[0], version})
		}
		if len(releases) > 10000 {
			return "", "", "", fmt.Errorf("dependency release tags exceed limit")
		}
	}
	if len(releases) == 0 {
		if relative {
			return fetchCurrent()
		}
		return "", "", "", fmt.Errorf("dependency %q has no git tag satisfying %s (or has conflicting version requirements)", id, strings.Join(opts.versionRanges, " AND "))
	}
	sort.Slice(releases, func(i, j int) bool {
		if comparison := releases[i].version.Compare(releases[j].version); comparison != 0 {
			return comparison > 0
		}
		return releases[i].tag < releases[j].tag
	})
	chosen := releases[0]
	gitSource.Ref, gitSource.SHA = chosen.tag, chosen.sha
	root, commit, err := fetchGit(ctx, gitSource, stage)
	return root, commit, chosen.version.String(), err
}
