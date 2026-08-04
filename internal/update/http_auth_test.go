package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func allowTestUpdateURL(string) error { return nil }

func TestAddGitHubTokenHeaderOnlyTrustsGitHubHTTPS(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "top-secret-token")
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "api", url: "https://api.github.com/repos/ginkida/gokin/releases", want: "token top-secret-token"},
		{name: "download", url: "https://github.com/ginkida/gokin/releases/download/v1/gokin", want: "token top-secret-token"},
		{name: "insecure", url: "http://github.com/ginkida/gokin", want: ""},
		{name: "suffix spoof", url: "https://github.com.attacker.example/file", want: ""},
		{name: "prefix spoof", url: "https://api.github.com.attacker.example/file", want: ""},
		{name: "local", url: "http://127.0.0.1/file", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tc.url, nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			addGitHubTokenHeader(req)
			if got := req.Header.Get("Authorization"); got != tc.want {
				t.Fatalf("Authorization = %q, want %q", got, tc.want)
			}
		})
	}

	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/test", nil)
	req.Header.Set("Authorization", "Bearer explicit")
	addGitHubTokenHeader(req)
	if got := req.Header.Get("Authorization"); got != "Bearer explicit" {
		t.Fatalf("explicit Authorization was overwritten: %q", got)
	}
}

func TestUpdateHTTPPathsDoNotLeakGitHubTokenToCustomHost(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "must-not-leak")
	seenAuth := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth <- r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/repos/test/repo/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"tag_name":"v1.2.3"}`)
		case "/checksums.txt":
			fmt.Fprint(w, strings.Repeat("a", 64)+"  gokin-linux-amd64.tar.gz\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	checker := NewChecker(DefaultConfig(), t.TempDir())
	checker.repo = "test/repo"
	checker.baseURL = server.URL
	if _, err := checker.GetLatestRelease(context.Background()); err != nil {
		t.Fatalf("GetLatestRelease: %v", err)
	}

	downloader := NewDownloader(DefaultConfig(), t.TempDir())
	downloader.validateURL = allowTestUpdateURL
	if _, err := downloader.DownloadChecksum(context.Background(), server.URL+"/checksums.txt"); err != nil {
		t.Fatalf("DownloadChecksum: %v", err)
	}

	if first, second := <-seenAuth, <-seenAuth; first != "" || second != "" {
		t.Fatalf("custom host received Authorization headers %q and %q", first, second)
	}
}

func TestDownloaderRejectsOversizedChecksumResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(maxChecksumFileBytes+1))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	downloader := NewDownloader(DefaultConfig(), t.TempDir())
	downloader.validateURL = allowTestUpdateURL
	_, err := downloader.DownloadChecksum(context.Background(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("DownloadChecksum error = %v, want size rejection", err)
	}
}
