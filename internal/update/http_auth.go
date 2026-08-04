package update

import (
	"net/http"
	"os"
	"strings"
)

func addGitHubTokenHeader(req *http.Request) {
	if req == nil || req.URL == nil || req.Header.Get("Authorization") != "" {
		return
	}
	if !strings.EqualFold(req.URL.Scheme, "https") || !isTrustedGitHubAuthHost(req.URL.Hostname()) {
		return
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
}

func isTrustedGitHubAuthHost(host string) bool {
	switch strings.ToLower(strings.TrimSuffix(host, ".")) {
	case "github.com", "api.github.com":
		return true
	default:
		return false
	}
}
