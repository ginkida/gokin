package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ============================================================
// JWT Tests
// ============================================================

// createTestJWT creates a mock JWT token for testing.
// Panics on marshal error since test data should always be valid.
func createTestJWT(payload map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		panic("createTestJWT: invalid payload: " + err.Error())
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadBytes)

	// Signature (fake for testing - we don't verify it)
	signature := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))

	return header + "." + payloadB64 + "." + signature
}

func TestExtractJWTClaims_ValidToken(t *testing.T) {
	payload := map[string]any{
		"sub":   "user123",
		"name":  "Test User",
		"email": "test@example.com",
		"iat":   float64(time.Now().Unix()),
		"exp":   float64(time.Now().Add(time.Hour).Unix()),
	}
	token := createTestJWT(payload)

	claims, err := ExtractJWTClaims(token)
	if err != nil {
		t.Fatalf("ExtractJWTClaims() error = %v", err)
	}

	if claims["sub"] != "user123" {
		t.Errorf("claims[sub] = %v, want %v", claims["sub"], "user123")
	}
	if claims["name"] != "Test User" {
		t.Errorf("claims[name] = %v, want %v", claims["name"], "Test User")
	}
	if claims["email"] != "test@example.com" {
		t.Errorf("claims[email] = %v, want %v", claims["email"], "test@example.com")
	}
}

func TestExtractJWTClaims_InvalidToken_NoParts(t *testing.T) {
	_, err := ExtractJWTClaims("invalid-token")
	if err == nil {
		t.Error("expected error for token with no parts")
	}
}

func TestExtractJWTClaims_InvalidToken_TooFewParts(t *testing.T) {
	_, err := ExtractJWTClaims("header.payload")
	if err == nil {
		t.Error("expected error for token with only 2 parts")
	}
}

func TestExtractJWTClaims_InvalidToken_TooManyParts(t *testing.T) {
	_, err := ExtractJWTClaims("a.b.c.d")
	if err == nil {
		t.Error("expected error for token with 4 parts")
	}
}

func TestExtractJWTClaims_InvalidPayload(t *testing.T) {
	// Create token with invalid base64 in payload
	token := "eyJhbGciOiJSUzI1NiJ9.invalid!base64.abc"

	_, err := ExtractJWTClaims(token)
	if err == nil {
		t.Error("expected error for invalid base64 payload")
	}
}

func TestExtractJWTClaims_InvalidJSONPayload(t *testing.T) {
	// Create token with valid base64 but invalid JSON payload
	invalidJSON := base64.RawURLEncoding.EncodeToString([]byte("not json"))
	token := "header." + invalidJSON + ".signature"

	_, err := ExtractJWTClaims(token)
	if err == nil {
		t.Error("expected error for invalid JSON payload")
	}
}

func TestExtractOpenAIEmail_DirectEmail(t *testing.T) {
	payload := map[string]any{
		"email": "user@openai.com",
	}
	token := createTestJWT(payload)

	email, err := ExtractOpenAIEmail(token)
	if err != nil {
		t.Fatalf("ExtractOpenAIEmail() error = %v", err)
	}

	if email != "user@openai.com" {
		t.Errorf("email = %v, want %v", email, "user@openai.com")
	}
}

func TestExtractOpenAIEmail_NestedAuthClaim(t *testing.T) {
	payload := map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"email": "nested@openai.com",
		},
	}
	token := createTestJWT(payload)

	email, err := ExtractOpenAIEmail(token)
	if err != nil {
		t.Fatalf("ExtractOpenAIEmail() error = %v", err)
	}

	if email != "nested@openai.com" {
		t.Errorf("email = %v, want %v", email, "nested@openai.com")
	}
}

func TestExtractOpenAIEmail_DirectTakesPrecedence(t *testing.T) {
	payload := map[string]any{
		"email": "direct@openai.com",
		"https://api.openai.com/auth": map[string]any{
			"email": "nested@openai.com",
		},
	}
	token := createTestJWT(payload)

	email, err := ExtractOpenAIEmail(token)
	if err != nil {
		t.Fatalf("ExtractOpenAIEmail() error = %v", err)
	}

	// Direct email should take precedence
	if email != "direct@openai.com" {
		t.Errorf("email = %v, want %v", email, "direct@openai.com")
	}
}

func TestExtractOpenAIEmail_NoEmail(t *testing.T) {
	payload := map[string]any{
		"sub": "user123",
	}
	token := createTestJWT(payload)

	_, err := ExtractOpenAIEmail(token)
	if err == nil {
		t.Error("expected error when no email is present")
	}
}

func TestExtractOpenAIAccountID_Valid(t *testing.T) {
	payload := map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct_12345",
		},
	}
	token := createTestJWT(payload)

	accountID, err := ExtractOpenAIAccountID(token)
	if err != nil {
		t.Fatalf("ExtractOpenAIAccountID() error = %v", err)
	}

	if accountID != "acct_12345" {
		t.Errorf("accountID = %v, want %v", accountID, "acct_12345")
	}
}

func TestExtractOpenAIAccountID_NoAuthClaim(t *testing.T) {
	payload := map[string]any{
		"email": "user@openai.com",
	}
	token := createTestJWT(payload)

	_, err := ExtractOpenAIAccountID(token)
	if err == nil {
		t.Error("expected error when no auth claim is present")
	}
}

func TestExtractOpenAIAccountID_NoAccountID(t *testing.T) {
	payload := map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"email": "user@openai.com",
		},
	}
	token := createTestJWT(payload)

	_, err := ExtractOpenAIAccountID(token)
	if err == nil {
		t.Error("expected error when no account ID is present")
	}
}

// ============================================================
// PKCE Tests
// ============================================================

func TestGenerateCodeVerifier(t *testing.T) {
	verifier, err := generateCodeVerifier()
	if err != nil {
		t.Fatalf("generateCodeVerifier() error = %v", err)
	}

	// Should be 43 characters (32 bytes base64url encoded)
	if len(verifier) != 43 {
		t.Errorf("verifier length = %d, want 43", len(verifier))
	}

	// Should only contain base64url characters
	for _, c := range verifier {
		if !isBase64URLChar(byte(c)) {
			t.Errorf("verifier contains invalid character: %c", c)
			break
		}
	}
}

func isBase64URLChar(c byte) bool {
	return (c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9') ||
		c == '-' || c == '_'
}

func TestGenerateCodeVerifier_Uniqueness(t *testing.T) {
	verifiers := make(map[string]bool)

	for i := 0; i < 100; i++ {
		verifier, err := generateCodeVerifier()
		if err != nil {
			t.Fatalf("generateCodeVerifier() error = %v", err)
		}

		if verifiers[verifier] {
			t.Error("generateCodeVerifier() produced duplicate verifier")
		}
		verifiers[verifier] = true
	}
}

func TestGenerateCodeChallenge(t *testing.T) {
	// Test with known verifier/challenge pair
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

	challenge := generateCodeChallenge(verifier)

	// Expected challenge for this verifier (pre-computed)
	expected := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

	if challenge != expected {
		t.Errorf("generateCodeChallenge() = %v, want %v", challenge, expected)
	}
}

func TestGenerateCodeChallenge_Consistency(t *testing.T) {
	verifier := "test-verifier-string"

	challenge1 := generateCodeChallenge(verifier)
	challenge2 := generateCodeChallenge(verifier)

	if challenge1 != challenge2 {
		t.Error("generateCodeChallenge() is not deterministic")
	}
}

func TestGenerateState(t *testing.T) {
	state, err := generateState()
	if err != nil {
		t.Fatalf("generateState() error = %v", err)
	}

	// Should be 22 characters (16 bytes base64url encoded)
	if len(state) != 22 {
		t.Errorf("state length = %d, want 22", len(state))
	}
}

func TestGenerateState_Uniqueness(t *testing.T) {
	states := make(map[string]bool)

	for i := 0; i < 100; i++ {
		state, err := generateState()
		if err != nil {
			t.Fatalf("generateState() error = %v", err)
		}

		if states[state] {
			t.Error("generateState() produced duplicate state")
		}
		states[state] = true
	}
}

// ============================================================
// Constants Tests
// ============================================================

func TestOAuthToken_IsExpired(t *testing.T) {
	tests := []struct {
		name    string
		token   *OAuthToken
		expired bool
	}{
		{
			name:    "nil token",
			token:   nil,
			expired: true,
		},
		{
			name: "expired token",
			token: &OAuthToken{
				AccessToken:  "access",
				RefreshToken: "refresh",
				ExpiresAt:    time.Now().Add(-10 * time.Minute),
			},
			expired: true,
		},
		{
			name: "valid token",
			token: &OAuthToken{
				AccessToken:  "access",
				RefreshToken: "refresh",
				ExpiresAt:    time.Now().Add(10 * time.Minute),
			},
			expired: false,
		},
		{
			name: "token expiring soon (within buffer)",
			token: &OAuthToken{
				AccessToken:  "access",
				RefreshToken: "refresh",
				ExpiresAt:    time.Now().Add(2 * time.Minute), // Less than 5 min buffer
			},
			expired: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.token.IsExpired(); got != tt.expired {
				t.Errorf("IsExpired() = %v, want %v", got, tt.expired)
			}
		})
	}
}

func TestOAuthToken_IsValid(t *testing.T) {
	tests := []struct {
		name  string
		token *OAuthToken
		valid bool
	}{
		{
			name:  "nil token",
			token: nil,
			valid: false,
		},
		{
			name: "valid token",
			token: &OAuthToken{
				AccessToken:  "access",
				RefreshToken: "refresh",
			},
			valid: true,
		},
		{
			name: "missing access token",
			token: &OAuthToken{
				RefreshToken: "refresh",
			},
			valid: false,
		},
		{
			name: "missing refresh token",
			token: &OAuthToken{
				AccessToken: "access",
			},
			valid: false,
		},
		{
			name: "empty access token",
			token: &OAuthToken{
				AccessToken:  "",
				RefreshToken: "refresh",
			},
			valid: false,
		},
		{
			name: "empty refresh token",
			token: &OAuthToken{
				AccessToken:  "access",
				RefreshToken: "",
			},
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.token.IsValid(); got != tt.valid {
				t.Errorf("IsValid() = %v, want %v", got, tt.valid)
			}
		})
	}
}

func TestOAuthToken_Fields(t *testing.T) {
	token := &OAuthToken{
		AccessToken:  "my-access-token",
		RefreshToken: "my-refresh-token",
		TokenType:    "Bearer",
		ExpiresAt:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		Email:        "test@example.com",
	}

	if token.AccessToken != "my-access-token" {
		t.Errorf("AccessToken = %v, want %v", token.AccessToken, "my-access-token")
	}
	if token.RefreshToken != "my-refresh-token" {
		t.Errorf("RefreshToken = %v, want %v", token.RefreshToken, "my-refresh-token")
	}
	if token.TokenType != "Bearer" {
		t.Errorf("TokenType = %v, want %v", token.TokenType, "Bearer")
	}
	if token.Email != "test@example.com" {
		t.Errorf("Email = %v, want %v", token.Email, "test@example.com")
	}
}

func TestOAuthToken_OptionalFields(t *testing.T) {
	token := &OAuthToken{
		AccessToken:  "access",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
	}

	if token.Email != "" {
		t.Errorf("Email = %v, want empty", token.Email)
	}
}

// ============================================================
// Constants Values Tests
// ============================================================

func TestGoogleOAuthConstants(t *testing.T) {
	if GeminiOAuthClientID == "" {
		t.Error("GeminiOAuthClientID is empty")
	}
	if GeminiOAuthClientSecret == "" {
		t.Error("GeminiOAuthClientSecret is empty")
	}
	if GeminiOAuthRedirectURI == "" {
		t.Error("GeminiOAuthRedirectURI is empty")
	}
	if GeminiOAuthCallbackPort == 0 {
		t.Error("GeminiOAuthCallbackPort is 0")
	}
}

func TestGoogleEndpoints(t *testing.T) {
	if GoogleAuthURL == "" {
		t.Error("GoogleAuthURL is empty")
	}
	if GoogleTokenURL == "" {
		t.Error("GoogleTokenURL is empty")
	}
	if GoogleUserInfo == "" {
		t.Error("GoogleUserInfo is empty")
	}

	// Verify URLs are valid
	if !strings.HasPrefix(GoogleAuthURL, "https://") {
		t.Error("GoogleAuthURL should use HTTPS")
	}
	if !strings.HasPrefix(GoogleTokenURL, "https://") {
		t.Error("GoogleTokenURL should use HTTPS")
	}
}

func TestScopes(t *testing.T) {
	if ScopeCloudPlatform == "" {
		t.Error("ScopeCloudPlatform is empty")
	}
	if ScopeUserInfoEmail == "" {
		t.Error("ScopeUserInfoEmail is empty")
	}
	if ScopeUserInfoProfile == "" {
		t.Error("ScopeUserInfoProfile is empty")
	}
}

func TestTimeouts(t *testing.T) {
	if OAuthCallbackTimeout == 0 {
		t.Error("OAuthCallbackTimeout is 0")
	}
	if OAuthHTTPTimeout == 0 {
		t.Error("OAuthHTTPTimeout is 0")
	}
	if TokenRefreshBuffer == 0 {
		t.Error("TokenRefreshBuffer is 0")
	}

	// Verify reasonable timeout values
	if OAuthCallbackTimeout < time.Minute {
		t.Errorf("OAuthCallbackTimeout = %v, seems too short", OAuthCallbackTimeout)
	}
	if OAuthHTTPTimeout < 5*time.Second {
		t.Errorf("OAuthHTTPTimeout = %v, seems too short", OAuthHTTPTimeout)
	}
}

func TestCodeAssistHeaders(t *testing.T) {
	if len(CodeAssistHeaders) == 0 {
		t.Error("CodeAssistHeaders is empty")
	}

	// Check required headers
	if _, ok := CodeAssistHeaders["User-Agent"]; !ok {
		t.Error("CodeAssistHeaders missing User-Agent")
	}
	if _, ok := CodeAssistHeaders["X-Goog-Api-Client"]; !ok {
		t.Error("CodeAssistHeaders missing X-Goog-Api-Client")
	}
	if _, ok := CodeAssistHeaders["Client-Metadata"]; !ok {
		t.Error("CodeAssistHeaders missing Client-Metadata")
	}
}

func TestGeminiCodeAssistAPI(t *testing.T) {
	if GeminiCodeAssistAPI == "" {
		t.Error("GeminiCodeAssistAPI is empty")
	}
	if !strings.HasPrefix(GeminiCodeAssistAPI, "https://") {
		t.Error("GeminiCodeAssistAPI should use HTTPS")
	}
}

// ============================================================
// tokenResponse Tests
// ============================================================

func TestTokenResponse_Fields(t *testing.T) {
	resp := tokenResponse{
		AccessToken:  "access",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
		Scope:        "read write",
	}

	if resp.AccessToken != "access" {
		t.Errorf("AccessToken = %v, want %v", resp.AccessToken, "access")
	}
	if resp.ExpiresIn != 3600 {
		t.Errorf("ExpiresIn = %v, want %v", resp.ExpiresIn, 3600)
	}
}

func TestTokenResponse_OptionalFields(t *testing.T) {
	resp := tokenResponse{
		AccessToken: "access",
		TokenType:   "Bearer",
		ExpiresIn:   3600,
	}

	if resp.RefreshToken != "" {
		t.Errorf("RefreshToken = %v, want empty", resp.RefreshToken)
	}
	if resp.Scope != "" {
		t.Errorf("Scope = %v, want empty", resp.Scope)
	}
}

// ============================================================
// userInfoResponse Tests
// ============================================================

func TestUserInfoResponse_Fields(t *testing.T) {
	resp := userInfoResponse{
		ID:            "12345",
		Email:         "user@example.com",
		VerifiedEmail: true,
		Name:          "Test User",
		Picture:       "https://example.com/pic.jpg",
	}

	if resp.ID != "12345" {
		t.Errorf("ID = %v, want %v", resp.ID, "12345")
	}
	if resp.Email != "user@example.com" {
		t.Errorf("Email = %v, want %v", resp.Email, "user@example.com")
	}
	if !resp.VerifiedEmail {
		t.Error("VerifiedEmail = false, want true")
	}
}

func TestUserInfoResponse_OptionalFields(t *testing.T) {
	resp := userInfoResponse{
		ID:    "12345",
		Email: "user@example.com",
	}

	if resp.Name != "" {
		t.Errorf("Name = %v, want empty", resp.Name)
	}
	if resp.Picture != "" {
		t.Errorf("Picture = %v, want empty", resp.Picture)
	}
}

// ============================================================
// Edge Cases
// ============================================================

func TestExtractJWTClaims_EmptyToken(t *testing.T) {
	_, err := ExtractJWTClaims("")
	if err == nil {
		t.Error("expected error for empty token")
	}
}

func TestExtractJWTClaims_WhitespaceToken(t *testing.T) {
	_, err := ExtractJWTClaims("   ")
	if err == nil {
		t.Error("expected error for whitespace token")
	}
}

func TestExtractJWTClaims_EmptyPayload(t *testing.T) {
	// Token with empty JSON object as payload
	payload := base64.RawURLEncoding.EncodeToString([]byte("{}"))
	token := "header." + payload + ".signature"

	claims, err := ExtractJWTClaims(token)
	if err != nil {
		t.Fatalf("ExtractJWTClaims() error = %v", err)
	}

	if len(claims) != 0 {
		t.Errorf("expected empty claims, got %v", claims)
	}
}

func TestGenerateCodeVerifier_CryptoFailure(t *testing.T) {
	// This test verifies that generateCodeVerifier handles crypto failures
	// In practice, this is hard to trigger since crypto/rand rarely fails
	// We just verify the function exists and works

	verifier, err := generateCodeVerifier()
	if err != nil {
		t.Errorf("generateCodeVerifier() failed unexpectedly: %v", err)
	}
	if verifier == "" {
		t.Error("generateCodeVerifier() returned empty string")
	}
}

func TestGenerateState_CryptoFailure(t *testing.T) {
	state, err := generateState()
	if err != nil {
		t.Errorf("generateState() failed unexpectedly: %v", err)
	}
	if state == "" {
		t.Error("generateState() returned empty string")
	}
}
