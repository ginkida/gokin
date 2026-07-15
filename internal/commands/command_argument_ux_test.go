package commands

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/config"
)

func TestSessionCommandsRejectIgnoredArgumentsBeforeAccessingState(t *testing.T) {
	app := &fakeAppForMCP{}
	tests := []struct {
		name string
		run  func() (string, error)
		want string
	}{
		{
			name: "save extra positional",
			run: func() (string, error) {
				return (&SaveCommand{}).Execute(context.Background(), []string{"one", "two"}, app)
			},
			want: "Unexpected argument",
		},
		{
			name: "save force without name",
			run: func() (string, error) {
				return (&SaveCommand{}).Execute(context.Background(), []string{"--force"}, app)
			},
			want: "requires a session name",
		},
		{
			name: "resume extra positional",
			run: func() (string, error) {
				return (&ResumeCommand{}).Execute(context.Background(), []string{"session", "junk"}, app)
			},
			want: "Unexpected argument",
		},
		{
			name: "resume force without id",
			run: func() (string, error) {
				return (&ResumeCommand{}).Execute(context.Background(), []string{"--force"}, app)
			},
			want: "Missing session ID",
		},
		{
			name: "sessions unknown scope",
			run: func() (string, error) {
				return (&SessionsCommand{}).Execute(context.Background(), []string{"typo"}, app)
			},
			want: "Unexpected argument",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.run()
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if !strings.Contains(got, tc.want) || !strings.Contains(got, "/") {
				t.Fatalf("message = %q, want %q plus canonical usage", got, tc.want)
			}
		})
	}
}

func TestDocumentedForceFlagMayPrecedeSessionTarget(t *testing.T) {
	target, force, errMsg := parseOptionalTargetAndFlag([]string{"--force", "checkpoint"}, "--force", "/save [name] [--force]")
	if errMsg != "" || target != "checkpoint" || !force {
		t.Fatalf("parse = target %q force %v error %q", target, force, errMsg)
	}
}

func TestLogoutRejectsAmbiguousInputWithoutRemovingCredentials(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "extra target", args: []string{"all", "extra", "--force"}, want: "Unexpected argument"},
		{name: "force on one provider", args: []string{"kimi", "--force"}, want: "only valid with /logout all --force"},
		{name: "force without target", args: []string{"--force"}, want: "only valid with /logout all --force"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{API: config.APIConfig{
				ActiveProvider: "kimi",
				KimiKey:        "keep-kimi",
				DeepSeekKey:    "keep-deepseek",
			}}
			app := newAuthApp(cfg)
			got, err := (&LogoutCommand{}).Execute(context.Background(), tc.args, app)
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("message = %q, want %q", got, tc.want)
			}
			if cfg.API.KimiKey != "keep-kimi" || cfg.API.DeepSeekKey != "keep-deepseek" || app.applyCalls != 0 {
				t.Fatalf("invalid input mutated credentials: cfg=%+v applyCalls=%d", cfg.API, app.applyCalls)
			}
		})
	}
}
