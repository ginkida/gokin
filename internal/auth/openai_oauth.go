package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gokin/internal/security"
)

// OpenAIOAuthManager handles the OAuth 2.0 PKCE flow for OpenAI (public client, no client secret).
type OpenAIOAuthManager struct {
	clientID    string
	redirectURI string
	scopes      []string

	// PKCE state
	codeVerifier  string
	codeChallenge string
	state         string

	httpClient *http.Client
}

// NewOpenAIOAuthManager creates an OAuth manager for OpenAI Codex authentication.
func NewOpenAIOAuthManager() *OpenAIOAuthManager {
	return NewOpenAIOAuthManagerWithPort(OpenAIOAuthCallbackPort)
}

// NewOpenAIOAuthManagerWithPort creates an OpenAI OAuth manager with a custom callback port.
func NewOpenAIOAuthManagerWithPort(port int) *OpenAIOAuthManager {
	httpClient, err := security.CreateDefaultHTTPClient()
	if err != nil {
		httpClient = &http.Client{Timeout: OAuthHTTPTimeout}
	}
	return &OpenAIOAuthManager{
		clientID:    OpenAIOAuthClientID,
		redirectURI: fmt.Sprintf("http://localhost:%d%s", port, OpenAIOAuthCallbackPath),
		scopes: []string{
			OpenAIScopeOpenID,
			OpenAIScopeProfile,
			OpenAIScopeEmail,
			OpenAIScopeOfflineAccess,
		},
		httpClient: httpClient,
	}
}

// GenerateAuthURL creates the authorization URL with PKCE parameters.
func (m *OpenAIOAuthManager) GenerateAuthURL() (string, error) {
	var err error
	m.codeVerifier, err = generateCodeVerifier()
	if err != nil {
		return "", fmt.Errorf("failed to generate code verifier: %w", err)
	}
	m.codeChallenge = generateCodeChallenge(m.codeVerifier)
	m.state, err = generateState()
	if err != nil {
		return "", fmt.Errorf("failed to generate state: %w", err)
	}

	params := url.Values{
		"client_id":                     {m.clientID},
		"redirect_uri":                  {m.redirectURI},
		"response_type":                 {"code"},
		"scope":                         {strings.Join(m.scopes, " ")},
		"code_challenge":                {m.codeChallenge},
		"code_challenge_method":         {"S256"},
		"state":                         {m.state},
		"audience":                      {OpenAIOAuthAudience},
		"id_token_add_organizations":    {"true"},
		"codex_cli_simplified_flow":     {"true"},
		"originator":                    {"codex_cli_rs"},
	}

	return OpenAIAuthURL + "?" + params.Encode(), nil
}

// GetState returns the current state parameter for validation.
func (m *OpenAIOAuthManager) GetState() string {
	return m.state
}

// openAITokenResponse represents the non-standard token response from OpenAI.
type openAITokenResponse struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	Expires int64  `json:"expires"` // milliseconds since epoch
	// Standard fields (fallback)
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// ExchangeCode exchanges the authorization code for access and refresh tokens.
func (m *OpenAIOAuthManager) ExchangeCode(ctx context.Context, code string) (*OAuthToken, error) {
	data := url.Values{
		"client_id":     {m.clientID},
		"code":          {code},
		"code_verifier": {m.codeVerifier},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {m.redirectURI},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", OpenAITokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB max
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		bodyStr := string(body)
		if len(bodyStr) > 500 {
			bodyStr = bodyStr[:500] + "...(truncated)"
		}
		return nil, fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, bodyStr)
	}

	var tokenResp openAITokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	// Handle non-standard response format: {"access":"...","refresh":"...","expires":123456789}
	accessToken := tokenResp.Access
	if accessToken == "" {
		accessToken = tokenResp.AccessToken
	}
	refreshToken := tokenResp.Refresh
	if refreshToken == "" {
		refreshToken = tokenResp.RefreshToken
	}

	var expiresAt time.Time
	if tokenResp.Expires > 0 {
		// Expires is in milliseconds since epoch
		expiresAt = time.UnixMilli(tokenResp.Expires)
	} else if tokenResp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	} else {
		// Default to 1 hour
		expiresAt = time.Now().Add(1 * time.Hour)
	}

	if accessToken == "" {
		return nil, fmt.Errorf("no access token in response")
	}

	token := &OAuthToken{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    "Bearer",
		ExpiresAt:    expiresAt,
	}

	// Extract email from JWT access token
	email, err := ExtractOpenAIEmail(accessToken)
	if err != nil {
		// Non-fatal
		email = ""
	}
	token.Email = email

	return token, nil
}

// RefreshToken refreshes an expired access token using the refresh token.
// OpenAI is a public client — no client_secret is sent.
func (m *OpenAIOAuthManager) RefreshToken(ctx context.Context, refreshToken string) (*OAuthToken, error) {
	data := url.Values{
		"client_id":     {m.clientID},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", OpenAITokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB max
	if err != nil {
		return nil, fmt.Errorf("failed to read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		bodyStr := string(body)
		if len(bodyStr) > 500 {
			bodyStr = bodyStr[:500] + "...(truncated)"
		}
		return nil, fmt.Errorf("token refresh failed (%d): %s", resp.StatusCode, bodyStr)
	}

	var tokenResp openAITokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse refresh response: %w", err)
	}

	accessToken := tokenResp.Access
	if accessToken == "" {
		accessToken = tokenResp.AccessToken
	}
	if accessToken == "" {
		return nil, fmt.Errorf("no access token in refresh response")
	}

	newRefreshToken := tokenResp.Refresh
	if newRefreshToken == "" {
		newRefreshToken = tokenResp.RefreshToken
	}
	if newRefreshToken == "" {
		newRefreshToken = refreshToken // Keep the old one
	}

	var expiresAt time.Time
	if tokenResp.Expires > 0 {
		expiresAt = time.UnixMilli(tokenResp.Expires)
	} else if tokenResp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	} else {
		expiresAt = time.Now().Add(1 * time.Hour)
	}

	token := &OAuthToken{
		AccessToken:  accessToken,
		RefreshToken: newRefreshToken,
		TokenType:    "Bearer",
		ExpiresAt:    expiresAt,
	}

	// Extract email from new JWT
	email, err := ExtractOpenAIEmail(accessToken)
	if err != nil {
		email = ""
	}
	token.Email = email

	return token, nil
}
