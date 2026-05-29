package update

import (
	"errors"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	// Empty GitHubRepo is invalid.
	c := DefaultConfig()
	c.GitHubRepo = ""
	if err := c.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("Validate() with empty repo = %v, want ErrInvalidConfig", err)
	}

	// Out-of-range values are clamped to safe minimums, not rejected.
	c = DefaultConfig()
	c.CheckInterval = time.Second // below 1m
	c.MaxBackups = 0              // below 1
	c.Timeout = time.Second       // below 5s
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() clamping = %v, want nil", err)
	}
	if c.CheckInterval != time.Minute {
		t.Errorf("CheckInterval = %v, want clamped to 1m", c.CheckInterval)
	}
	if c.MaxBackups != 1 {
		t.Errorf("MaxBackups = %d, want clamped to 1", c.MaxBackups)
	}
	if c.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want clamped to 5s", c.Timeout)
	}
}

func TestConfigMerge(t *testing.T) {
	base := DefaultConfig()
	origRepo := base.GitHubRepo

	// nil is a no-op.
	base.Merge(nil)
	if base.GitHubRepo != origRepo {
		t.Error("Merge(nil) altered config")
	}

	// Zero values in `other` do NOT overwrite; non-zero ones do.
	base.Merge(&Config{
		GitHubRepo:    "", // zero → keep base
		CheckInterval: 0,  // zero → keep base
		MaxBackups:    7,  // non-zero → override
		Channel:       ChannelBeta,
	})
	if base.GitHubRepo != origRepo {
		t.Errorf("Merge overwrote repo with empty: %q", base.GitHubRepo)
	}
	if base.MaxBackups != 7 {
		t.Errorf("Merge did not apply MaxBackups: %d", base.MaxBackups)
	}
	if base.Channel != ChannelBeta {
		t.Errorf("Merge did not apply Channel: %q", base.Channel)
	}
}

func TestConfigShouldCheck(t *testing.T) {
	c := DefaultConfig()
	c.CheckInterval = time.Hour

	if !c.ShouldCheck(time.Now().Add(-2 * time.Hour)) {
		t.Error("ShouldCheck after interval elapsed = false, want true")
	}
	if c.ShouldCheck(time.Now()) {
		t.Error("ShouldCheck immediately after a check = true, want false")
	}

	c.Enabled = false
	if c.ShouldCheck(time.Now().Add(-2 * time.Hour)) {
		t.Error("ShouldCheck with Enabled=false = true, want false")
	}
	c.Enabled = true
	c.AutoCheck = false
	if c.ShouldCheck(time.Now().Add(-2 * time.Hour)) {
		t.Error("ShouldCheck with AutoCheck=false = true, want false")
	}
}

func TestConfigMatchesChannel(t *testing.T) {
	stable := &ReleaseInfo{Prerelease: false, Draft: false}
	pre := &ReleaseInfo{Prerelease: true, Draft: false}
	draft := &ReleaseInfo{Prerelease: false, Draft: true}

	cases := []struct {
		channel Channel
		release *ReleaseInfo
		want    bool
	}{
		{ChannelStable, stable, true},
		{ChannelStable, pre, false},      // stable excludes prereleases
		{ChannelStable, draft, false},    // and drafts
		{ChannelBeta, pre, true},         // beta includes prereleases
		{ChannelBeta, draft, false},      // but not drafts
		{ChannelNightly, draft, true},    // nightly includes everything
		{Channel("garbage"), pre, false}, // unknown channel falls back to stable rules
		{ChannelStable, nil, false},      // nil release never matches
	}
	c := DefaultConfig()
	for _, tc := range cases {
		c.Channel = tc.channel
		if got := c.MatchesChannel(tc.release); got != tc.want {
			t.Errorf("MatchesChannel(channel=%q, %+v) = %v, want %v", tc.channel, tc.release, got, tc.want)
		}
	}
}
