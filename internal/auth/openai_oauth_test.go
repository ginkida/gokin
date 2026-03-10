package auth

import (
	"net/url"
	"testing"
)

func TestOpenAIOAuthGenerateAuthURLMatchesExpectedAuthorizeEndpoint(t *testing.T) {
	manager := NewOpenAIOAuthManagerWithPort(1455)

	authURL, err := manager.GenerateAuthURL()
	if err != nil {
		t.Fatalf("GenerateAuthURL() error = %v", err)
	}

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	if parsed.Scheme != "https" {
		t.Fatalf("scheme = %q, want https", parsed.Scheme)
	}
	if parsed.Host != "auth.openai.com" {
		t.Fatalf("host = %q, want auth.openai.com", parsed.Host)
	}
	if parsed.Path != "/oauth/authorize" {
		t.Fatalf("path = %q, want /oauth/authorize", parsed.Path)
	}

	query := parsed.Query()
	if got := query.Get("client_id"); got != OpenAIOAuthClientID {
		t.Fatalf("client_id = %q, want %q", got, OpenAIOAuthClientID)
	}
	if got := query.Get("redirect_uri"); got != "http://localhost:1455/auth/callback" {
		t.Fatalf("redirect_uri = %q", got)
	}
	if got := query.Get("response_type"); got != "code" {
		t.Fatalf("response_type = %q, want code", got)
	}
	if got := query.Get("scope"); got != "openid profile email offline_access" {
		t.Fatalf("scope = %q", got)
	}
	if got := query.Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", got)
	}
	if got := query.Get("id_token_add_organizations"); got != "true" {
		t.Fatalf("id_token_add_organizations = %q, want true", got)
	}
	if got := query.Get("codex_cli_simplified_flow"); got != "true" {
		t.Fatalf("codex_cli_simplified_flow = %q, want true", got)
	}
	if got := query.Get("originator"); got != "codex_cli_rs" {
		t.Fatalf("originator = %q, want codex_cli_rs", got)
	}
	if got := query.Get("code_challenge"); got == "" {
		t.Fatal("code_challenge should not be empty")
	}
	if got := query.Get("state"); got == "" {
		t.Fatal("state should not be empty")
	}
	if got := query.Get("audience"); got != "" {
		t.Fatalf("audience = %q, want empty", got)
	}
}
