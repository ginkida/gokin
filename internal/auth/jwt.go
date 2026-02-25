package auth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// ExtractJWTClaims decodes the payload of a JWT token without signature verification.
// This is used for extracting user info from OpenAI access tokens.
func ExtractJWTClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT: expected 3 parts, got %d", len(parts))
	}

	// Decode payload (second part)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("failed to parse JWT claims: %w", err)
	}

	return claims, nil
}

// ExtractOpenAIEmail extracts the email from an OpenAI JWT access token.
func ExtractOpenAIEmail(token string) (string, error) {
	claims, err := ExtractJWTClaims(token)
	if err != nil {
		return "", err
	}

	// Try standard email claim first
	if email, ok := claims["email"].(string); ok && email != "" {
		return email, nil
	}

	// Try nested OpenAI auth claim
	if authClaim, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if email, ok := authClaim["email"].(string); ok && email != "" {
			return email, nil
		}
	}

	return "", fmt.Errorf("no email found in JWT claims")
}

// ExtractOpenAIAccountID extracts the chatgpt_account_id from an OpenAI JWT access token.
func ExtractOpenAIAccountID(token string) (string, error) {
	claims, err := ExtractJWTClaims(token)
	if err != nil {
		return "", err
	}

	// Look in the OpenAI auth claim
	authClaim, ok := claims["https://api.openai.com/auth"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("no OpenAI auth claim found in JWT")
	}

	accountID, ok := authClaim["chatgpt_account_id"].(string)
	if !ok || accountID == "" {
		return "", fmt.Errorf("no chatgpt_account_id found in JWT")
	}

	return accountID, nil
}
