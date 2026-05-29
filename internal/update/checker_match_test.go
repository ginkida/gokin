package update

import (
	"runtime"
	"slices"
	"testing"
	"time"
)

func TestStripArchiveExtensions(t *testing.T) {
	cases := map[string]string{
		"gokin-linux-amd64.tar.gz": "gokin-linux-amd64",
		"gokin-linux-amd64.tgz":    "gokin-linux-amd64",
		"gokin-windows-amd64.zip":  "gokin-windows-amd64",
		"gokin-windows-amd64.exe":  "gokin-windows-amd64",
		"gokin-darwin-arm64":       "gokin-darwin-arm64", // no extension
		"GOKIN-LINUX-AMD64.TAR.GZ": "GOKIN-LINUX-AMD64",  // case-insensitive match, original case preserved
	}
	for in, want := range cases {
		if got := stripArchiveExtensions(in); got != want {
			t.Errorf("stripArchiveExtensions(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGetAlternativePatterns(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())

	amd := c.getAlternativePatterns(Platform{OS: "linux", Arch: "amd64"})
	assertContains(t, amd, "gokin_linux_x86_64") // amd64 → x86_64 mapping
	assertContains(t, amd, "gokin-linux-x86_64")

	arm := c.getAlternativePatterns(Platform{OS: "linux", Arch: "arm64"})
	assertContains(t, arm, "gokin_linux_aarch64") // arm64 → aarch64 mapping

	mac := c.getAlternativePatterns(Platform{OS: "darwin", Arch: "arm64"})
	assertContains(t, mac, "gokin-macos-arm64") // darwin → macos
}

func TestFindAssetForPlatform(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())

	// Build the exact-match name for whatever platform the test runs on.
	want := Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}.AssetPattern() + ".tar.gz"
	rel := &ReleaseInfo{Assets: []Asset{
		{Name: "some-other-thing.txt"},
		{Name: want},
	}}
	got := c.FindAssetForPlatform(rel)
	if got == nil || got.Name != want {
		t.Fatalf("FindAssetForPlatform = %v, want asset %q", got, want)
	}

	// No matching asset → nil (not a panic, not a wrong pick).
	none := c.FindAssetForPlatform(&ReleaseInfo{Assets: []Asset{{Name: "unrelated.txt"}}})
	if none != nil {
		t.Errorf("FindAssetForPlatform(no match) = %v, want nil", none)
	}
	// Nil/empty release → nil.
	if c.FindAssetForPlatform(nil) != nil {
		t.Error("FindAssetForPlatform(nil) != nil")
	}
	if c.FindAssetForPlatform(&ReleaseInfo{}) != nil {
		t.Error("FindAssetForPlatform(empty assets) != nil")
	}
}

func TestFindChecksumAsset(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	asset := &Asset{Name: "gokin-linux-amd64.tar.gz"}

	rel := &ReleaseInfo{Assets: []Asset{
		{Name: "gokin-linux-amd64.tar.gz"},
		{Name: "checksums.txt"},
	}}
	if got := c.FindChecksumAsset(rel, asset); got == nil || got.Name != "checksums.txt" {
		t.Fatalf("FindChecksumAsset = %v, want checksums.txt", got)
	}

	// Per-asset .sha256 takes precedence by pattern order.
	rel2 := &ReleaseInfo{Assets: []Asset{
		{Name: "gokin-linux-amd64.tar.gz.sha256"},
		{Name: "checksums.txt"},
	}}
	if got := c.FindChecksumAsset(rel2, asset); got == nil || got.Name != "gokin-linux-amd64.tar.gz.sha256" {
		t.Fatalf("FindChecksumAsset = %v, want the per-asset .sha256", got)
	}

	if c.FindChecksumAsset(nil, asset) != nil || c.FindChecksumAsset(rel, nil) != nil {
		t.Error("FindChecksumAsset with nil args should be nil")
	}
}

func TestIsCacheValid(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CheckInterval = time.Hour
	c := NewChecker(cfg, t.TempDir())

	if c.IsCacheValid(nil) {
		t.Error("IsCacheValid(nil) = true, want false")
	}
	if !c.IsCacheValid(&UpdateCache{LastCheck: time.Now().Add(-30 * time.Minute)}) {
		t.Error("fresh cache (30m < 1h) reported invalid")
	}
	if c.IsCacheValid(&UpdateCache{LastCheck: time.Now().Add(-2 * time.Hour)}) {
		t.Error("stale cache (2h > 1h) reported valid")
	}
}

func assertContains(t *testing.T, haystack []string, needle string) {
	t.Helper()
	if !slices.Contains(haystack, needle) {
		t.Errorf("expected %q in %v", needle, haystack)
	}
}
