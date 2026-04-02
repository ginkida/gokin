package security

import (
	"strings"
	"testing"
)

func TestRedactEmpty(t *testing.T) {
	r := NewSecretRedactor()
	if got := r.Redact(""); got != "" {
		t.Errorf("empty should return empty, got %q", got)
	}
}

func TestRedactNoSecrets(t *testing.T) {
	r := NewSecretRedactor()
	text := "This is a normal log line with no secrets."
	if got := r.Redact(text); got != text {
		t.Errorf("no-secret text should be unchanged, got %q", got)
	}
}

func TestRedactAWSKey(t *testing.T) {
	r := NewSecretRedactor()
	text := "Found key: AKIAIOSFODNN7EXAMPLE"
	got := r.Redact(text)
	if strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS key should be redacted: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("should contain [REDACTED]: %q", got)
	}
}

func TestRedactGitHubToken(t *testing.T) {
	r := NewSecretRedactor()
	text := "Token: ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef1234"
	got := r.Redact(text)
	if strings.Contains(got, "ghp_") {
		t.Errorf("GitHub token should be redacted: %q", got)
	}
}

func TestRedactBearerToken(t *testing.T) {
	r := NewSecretRedactor()
	text := "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.test1234567890"
	got := r.Redact(text)
	if strings.Contains(got, "eyJhbGci") {
		t.Errorf("Bearer token should be redacted: %q", got)
	}
}

func TestRedactDatabaseURL(t *testing.T) {
	r := NewSecretRedactor()
	tests := []struct {
		name string
		text string
	}{
		{"postgres", "postgres://admin:supersecret@db.host:5432/mydb"},
		{"mysql", "mysql://root:password123@localhost:3306/app"},
		{"mongodb", "mongodb://user:pass1234@mongo.host:27017/db"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Redact(tt.text)
			if strings.Contains(got, "supersecret") || strings.Contains(got, "password123") || strings.Contains(got, "pass1234") {
				t.Errorf("DB password should be redacted: %q", got)
			}
		})
	}
}

func TestRedactAPIKeyValue(t *testing.T) {
	r := NewSecretRedactor()
	text := `api_key: sk_live_abc123def456ghi789`
	got := r.Redact(text)
	if strings.Contains(got, "sk_live_abc123") {
		t.Errorf("API key value should be redacted: %q", got)
	}
}

func TestRedactPEMKey(t *testing.T) {
	r := NewSecretRedactor()
	text := "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\n-----END RSA PRIVATE KEY-----"
	got := r.Redact(text)
	if strings.Contains(got, "MIIEpAIBAAKCAQEA") {
		t.Errorf("PEM key should be redacted: %q", got)
	}
}

func TestRedactMap(t *testing.T) {
	r := NewSecretRedactor()
	m := map[string]any{
		"name":   "test",
		"secret": "AKIAIOSFODNN7EXAMPLE",
		"normal": "hello world",
		"nested": map[string]any{"key": "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef1234"},
	}

	result := r.RedactMap(m)
	if result == nil {
		t.Fatal("RedactMap returned nil")
	}

	// "name" should be preserved
	if result["name"] != "test" {
		t.Errorf("name should be preserved: %v", result["name"])
	}
}

func TestRedactMapNil(t *testing.T) {
	r := NewSecretRedactor()
	if got := r.RedactMap(nil); got != nil {
		t.Errorf("nil map should return nil: %v", got)
	}
}

func TestRedactAnyNil(t *testing.T) {
	r := NewSecretRedactor()
	if got := r.RedactAny(nil); got != nil {
		t.Error("nil should return nil")
	}
}

func TestRedactAnySlice(t *testing.T) {
	r := NewSecretRedactor()
	input := []any{"normal", "AKIAIOSFODNN7EXAMPLE"}
	result := r.RedactAny(input)
	slice, ok := result.([]any)
	if !ok {
		t.Fatal("result should be []any")
	}
	if slice[0] != "normal" {
		t.Errorf("normal string should be preserved: %v", slice[0])
	}
}

func TestAddPattern(t *testing.T) {
	r := NewSecretRedactor()
	err := r.AddPattern(`CUSTOM_[A-Z0-9]{10}`)
	if err != nil {
		t.Fatalf("AddPattern: %v", err)
	}

	text := "key=CUSTOM_ABCDE12345 here"
	got := r.Redact(text)
	if strings.Contains(got, "CUSTOM_ABCDE12345") {
		t.Errorf("custom pattern should be redacted: %q", got)
	}
}
