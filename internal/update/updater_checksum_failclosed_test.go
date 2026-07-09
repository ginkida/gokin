package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestUpdaterDownload_FailsClosedOnMissingChecksumEntry pins the v0.100.73 #7
// fix: when the checksum file is fetched but has no entry for the downloaded
// asset (a misnamed/partial checksums.txt, a CDN error page), verification used
// to silently fall OPEN — the binary installed unverified even with
// VerifyChecksum:true. It must now refuse to install.
func TestUpdaterDownload_FailsClosedOnMissingChecksumEntry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".tar.gz"):
			w.Write([]byte("fake archive bytes"))
		case strings.HasSuffix(r.URL.Path, "checksums.txt"):
			// A valid checksum file — but for a DIFFERENT asset only.
			w.Write([]byte("deadbeef00  gokin-some-other-platform.tar.gz\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.VerifyChecksum = true
	u := &Updater{
		config:     cfg,
		currentVer: "0.0.1",
		tempDir:    t.TempDir(),
		downloader: &Downloader{
			httpClient: &http.Client{Timeout: 10 * time.Second},
			config:     cfg,
			tempDir:    t.TempDir(),
		},
	}

	info := &UpdateInfo{
		AssetURL:    srv.URL + "/gokin-linux-amd64.tar.gz",
		AssetName:   "gokin-linux-amd64.tar.gz",
		ChecksumURL: srv.URL + "/checksums.txt",
	}

	_, err := u.Download(context.Background(), info, nil)
	if err == nil {
		t.Fatal("Download must FAIL when the checksum file has no entry for the asset (fail closed)")
	}
	if !strings.Contains(err.Error(), "no entry for asset") {
		t.Fatalf("error = %v, want a 'no entry for asset' fail-closed error", err)
	}
}
