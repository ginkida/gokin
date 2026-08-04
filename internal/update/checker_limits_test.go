package update

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type failingJSONReader struct {
	err error
}

func (r failingJSONReader) Read([]byte) (int, error) { return 0, r.err }

func TestDecodeBoundedJSONLimitsAndStrictness(t *testing.T) {
	readErr := errors.New("reader must not be touched")
	var value map[string]any
	if err := decodeBoundedJSON(failingJSONReader{err: readErr}, 33, 32, &value); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("declared oversize error = %v", err)
	} else if errors.Is(err, readErr) {
		t.Fatalf("declared oversized body was read: %v", err)
	}

	exact := `{"ok":true}`
	if err := decodeBoundedJSON(strings.NewReader(exact), -1, int64(len(exact)), &value); err != nil {
		t.Fatalf("exact-limit response: %v", err)
	}
	if value["ok"] != true {
		t.Fatalf("decoded response = %v", value)
	}

	if err := decodeBoundedJSON(strings.NewReader(exact+" "), -1, int64(len(exact)), &value); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("streaming oversize error = %v", err)
	}
	if err := decodeBoundedJSON(strings.NewReader(exact+exact), -1, 64, &value); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
	if err := decodeBoundedJSON(nil, -1, 32, &value); err == nil {
		t.Fatal("nil response body was accepted")
	}
	if err := decodeBoundedJSON(strings.NewReader(exact), -1, 0, &value); err == nil {
		t.Fatal("non-positive response limit was accepted")
	}
	if err := decodeBoundedJSON(failingJSONReader{err: readErr}, -1, 32, &value); !errors.Is(err, readErr) {
		t.Fatalf("read error = %v, want wrapped source error", err)
	}
}

func TestCheckerRejectsOversizedReleaseResponses(t *testing.T) {
	checker, _ := newAPITestChecker(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.FormatInt(maxReleaseResponseBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	}))

	if _, err := checker.GetLatestRelease(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("latest release error = %v, want size rejection", err)
	}
	if _, err := checker.GetReleases(context.Background(), 20); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("release list error = %v, want size rejection", err)
	}
}

func TestCheckerRejectsInvalidReleaseListLimits(t *testing.T) {
	checker := NewChecker(nil, t.TempDir())
	for _, limit := range []int{-1, 0, 101} {
		if _, err := checker.GetReleases(context.Background(), limit); err == nil {
			t.Errorf("GetReleases limit %d was accepted", limit)
		}
	}
}

func TestCheckerCacheIsBoundedAndFailedSavePreservesPrevious(t *testing.T) {
	dir := t.TempDir()
	checker := NewChecker(DefaultConfig(), dir)
	previous := &UpdateCache{LastCheck: time.Now(), LatestVersion: "v1.2.3"}
	if err := checker.SaveCache(previous); err != nil {
		t.Fatalf("save previous cache: %v", err)
	}

	tooLarge := &UpdateCache{ReleaseNotes: strings.Repeat("x", int(maxUpdateCacheBytes)+1)}
	if err := checker.SaveCache(tooLarge); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized SaveCache error = %v", err)
	}
	loaded, err := checker.LoadCache()
	if err != nil {
		t.Fatalf("load preserved cache: %v", err)
	}
	if loaded.LatestVersion != previous.LatestVersion {
		t.Fatalf("failed save replaced previous cache: %+v", loaded)
	}

	cachePath := filepath.Join(dir, "update_cache.json")
	if err := os.WriteFile(cachePath, []byte(strings.Repeat("x", int(maxUpdateCacheBytes)+1)), 0o600); err != nil {
		t.Fatalf("write oversized cache fixture: %v", err)
	}
	if _, err := checker.LoadCache(); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized LoadCache error = %v", err)
	}
}

func TestCheckerRejectsNilCacheAndEmptyDirectory(t *testing.T) {
	checker := NewChecker(DefaultConfig(), t.TempDir())
	if err := checker.SaveCache(nil); err == nil {
		t.Fatal("nil cache was accepted")
	}

	emptyDirChecker := NewChecker(DefaultConfig(), "")
	if err := emptyDirChecker.SaveCache(&UpdateCache{}); err == nil || !strings.Contains(err.Error(), "directory is empty") {
		t.Fatalf("empty-directory SaveCache error = %v", err)
	}
	if _, err := emptyDirChecker.LoadCache(); err == nil || !strings.Contains(err.Error(), "directory is empty") {
		t.Fatalf("empty-directory LoadCache error = %v", err)
	}
}

func TestCheckerCacheRejectsZeroAndFutureTimestamps(t *testing.T) {
	checker := NewChecker(DefaultConfig(), t.TempDir())
	if checker.IsCacheValid(&UpdateCache{}) {
		t.Fatal("zero cache timestamp was treated as fresh")
	}
	if checker.IsCacheValid(&UpdateCache{LastCheck: time.Now().Add(time.Hour)}) {
		t.Fatal("future cache timestamp was treated as fresh")
	}
}

var _ io.Reader = failingJSONReader{}
