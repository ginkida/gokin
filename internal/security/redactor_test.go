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
	text := "Token: ghp" + "_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef1234"
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
		"nested": map[string]any{"key": "ghp" + "_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef1234"},
	}

	result := r.RedactMap(m)
	if result == nil {
		t.Fatal("RedactMap returned nil")
	}

	// "name" should be preserved
	if result["name"] != "test" {
		t.Errorf("name should be preserved: %v", result["name"])
	}
	if secret, _ := result["secret"].(string); strings.Contains(secret, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("top-level secret should be redacted: %v", result["secret"])
	}
	nested, ok := result["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested map should be preserved as map[string]any, got %T", result["nested"])
	}
	if key, _ := nested["key"].(string); strings.Contains(key, "ghp_") {
		t.Errorf("nested token should be redacted: %v", nested["key"])
	}
	if original, _ := m["secret"].(string); original != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("RedactMap should not mutate input map: %v", m["secret"])
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
	if secret, _ := slice[1].(string); strings.Contains(secret, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("secret string should be redacted: %v", slice[1])
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

// TestRedactAllPatterns exercises every built-in regex so a refactor that
// silently breaks a pattern is caught.
func TestRedactAllPatterns(t *testing.T) {
	r := NewSecretRedactor()

	cases := []struct {
		name  string
		input string
		leaks []string
	}{
		{
			name:  "apikey_context",
			input: `config: api_key = "a1b2c3d4e5f6g7h8i9j0"`,
			leaks: []string{"a1b2c3d4e5f6g7h8i9j0"},
		},
		{
			name:  "password_context",
			input: `password: SuperSecret123!`,
			leaks: []string{"SuperSecret123!"},
		},
		{
			name:  "bearer",
			input: "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload123.signature4567890ab",
			leaks: []string{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload123.signature4567890ab"},
		},
		{
			name:  "aws_access_key",
			input: "Found key: AKIAIOSFODNN7EXAMPLE",
			leaks: []string{"AKIAIOSFODNN7EXAMPLE"},
		},
		{
			name:  "aws_secret_key",
			input: "aws_secret_key = abcdefghijklmnopqrstuvwxyz0123456789ABCD",
			leaks: []string{"abcdefghijklmnopqrstuvwxyz0123456789ABCD"},
		},
		{
			name:  "github",
			input: "Token: ghp" + "_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef1234",
			leaks: []string{"ghp" + "_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef1234"},
		},
		{
			name:  "github_fine_grained",
			input: "token github_pa" + "t_11ABCDEFGhijklmnopqrstuvwxyz0123456789_ABCDEFGHIJKLMNOPQRSTUVWXYZ",
			leaks: []string{"github_pa" + "t_11ABCDEFGhijklmnopqrstuvwxyz0123456789_ABCDEFGHIJKLMNOPQRSTUVWXYZ"},
		},
		{
			name:  "provider_api_key",
			input: "raw provider token sk" + "-abcdefghijklmnopqrstuvwxyz0123456789",
			leaks: []string{"sk" + "-abcdefghijklmnopqrstuvwxyz0123456789"},
		},
		{
			name:  "gitlab_pat",
			input: "GitLab token glpa" + "t-abcdefghijklmnopqrstuvwxyz123456",
			leaks: []string{"glpa" + "t-abcdefghijklmnopqrstuvwxyz123456"},
		},
		{
			name:  "stripe_live",
			input: "key: sk_liv" + "e_ABCdefGHIjklMNO123456pqr",
			leaks: []string{"sk_liv" + "e_ABCdefGHIjklMNO123456pqr"},
		},
		{
			name:  "stripe_test",
			input: "key: sk_tes" + "t_ABCdefGHIjklMNO123456pqr",
			leaks: []string{"sk_tes" + "t_ABCdefGHIjklMNO123456pqr"},
		},
		{
			name:  "google_api",
			input: "google_api_key=AIzaSy" + "A1B2C3D4E5F6G7H8I9J0KLMNOPQRSTUv",
			leaks: []string{"AIzaSy" + "A1B2C3D4E5F6G7H8I9J0KLMNOPQRSTUv"},
		},
		{
			name:  "jwt",
			input: "token: eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
			leaks: []string{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"},
		},
		{
			name:  "pem_private_key",
			input: "-----BEGIN PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\n-----END PRIVATE KEY-----",
			leaks: []string{"MIIEpAIBAAKCAQEA"},
		},
		{
			name:  "base64_secret",
			input: `secret: c2VjcmV0UGFzc3dvcmQxMjM0NTY3OA==`,
			leaks: []string{"c2VjcmV0UGFzc3dvcmQxMjM0NTY3OA=="},
		},
		{
			name:  "postgres",
			input: "postgres://admin:supersecret@db.host:5432/mydb",
			leaks: []string{"supersecret"},
		},
		{
			name:  "mysql",
			input: "mysql://root:password123@10.0.0.1:3306/app",
			leaks: []string{"password123"},
		},
		{
			name:  "mongodb",
			input: "mongodb://user:pass1234@mongo.host:27017/db",
			leaks: []string{"pass1234"},
		},
		{
			name:  "redis",
			input: "redis://:myredispassword123@redis.host:6379",
			leaks: []string{"myredispassword123"},
		},
		{
			name:  "conn_string",
			input: "Server=myhost;Database=mydb;User=myuser;Password=secret123456;",
			leaks: []string{"secret123456"},
		},
		{
			name:  "slack_webhook",
			input: "https://hooks.slack.com/serv" + "ices/T00000000/B00000000/XXXXXXXXXXXXXXXXXXXXXXXX",
			leaks: []string{"hooks.slack.com/serv" + "ices/T00000000/B00000000/XXXXXXXXXXXXXXXXXXXXXXXX"},
		},
		{
			name:  "slack_bot_token",
			input: "xox" + "b-1234567890-0987654321-abcdefghijklmnopqrstuv",
			leaks: []string{"xox" + "b-1234567890-0987654321-abcdefghijklmnopqrstuv"},
		},
		{
			name:  "discord_bot_token",
			input: "MTIzNDU2Nzg5MDEyMzQ1Njc4.GabcDE.fghijklmnopqrstuvwxyz1234567",
			leaks: []string{"MTIzNDU2Nzg5MDEyMzQ1Njc4.GabcDE.fghijklmnopqrstuvwxyz1234567"},
		},
		{
			name:  "twilio",
			input: "account: AC12345678" + "90abcdef1234567890abcdef",
			leaks: []string{"AC12345678" + "90abcdef1234567890abcdef"},
		},
		{
			name:  "firebase",
			input: `"private_key": "` + strings.Repeat("a", 120) + `"`,
			leaks: []string{strings.Repeat("a", 120)},
		},
		{
			name:  "ssh_dsa_key",
			input: "-----BEGIN DSA PRIVATE KEY-----",
			leaks: []string{"-----BEGIN DSA PRIVATE KEY-----"},
		},
		{
			name:  "ssh_ec_key",
			input: "-----BEGIN EC PRIVATE KEY-----",
			leaks: []string{"-----BEGIN EC PRIVATE KEY-----"},
		},
		{
			name:  "auth_basic",
			input: "Authorization: Basic dXNlcm5hbWU6cGFzc3dvcmQxMjM=",
			leaks: []string{"dXNlcm5hbWU6cGFzc3dvcmQxMjM="},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Redact(tc.input)
			if !strings.Contains(got, "[REDACTED]") {
				t.Errorf("pattern %q did not produce [REDACTED] marker:\n  in:  %s\n  out: %s", tc.name, tc.input, got)
			}
			for _, leak := range tc.leaks {
				if strings.Contains(got, leak) {
					t.Errorf("pattern %q leaked %q:\n  in:  %s\n  out: %s", tc.name, leak, tc.input, got)
				}
			}
		})
	}
}

// TestRedactWhitelistAndShortValues verifies that exact safe values and short
// strings are not redacted.
func TestRedactWhitelistAndShortValues(t *testing.T) {
	r := NewSecretRedactor()

	cases := []struct {
		name  string
		input string
	}{
		{"placeholder_exact", `api_key: "placeholder"`},
		{"safe_redacted_marker", `secret: "redacted"`},
		{"short_value", `password: "abcd"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Redact(tc.input)
			if got != tc.input {
				t.Errorf("safe value should be preserved:\n  in:  %s\n  out: %s", tc.input, got)
			}
		})
	}
}

func TestRedactSyntheticPlaceholderWithSuffix(t *testing.T) {
	r := NewSecretRedactor()
	input := `api_key: "example12345678"`

	got := r.Redact(input)
	if strings.Contains(got, "example12345678") {
		t.Errorf("placeholder-like value with a suffix should still be redacted: %q", got)
	}
}

func TestRedactPreservesCommonDelimiters(t *testing.T) {
	r := NewSecretRedactor()

	cases := []struct {
		name      string
		input     string
		leak      string
		wantPiece string
	}{
		{
			name:      "bearer_comma",
			input:     "Authorization: Bearer abcdefghij1234567890, retrying",
			leak:      "abcdefghij1234567890",
			wantPiece: "Bearer [REDACTED], retrying",
		},
		{
			name:      "bearer_closing_paren",
			input:     "(Authorization: Bearer abcdefghij1234567890)",
			leak:      "abcdefghij1234567890",
			wantPiece: "Bearer [REDACTED])",
		},
		{
			name:      "context_value_closing_bracket",
			input:     "headers=[api_key=abcdef1234567890]",
			leak:      "abcdef1234567890",
			wantPiece: "api_key=[REDACTED]]",
		},
		{
			name:      "context_value_closing_brace",
			input:     "{secret=abcdef1234567890}",
			leak:      "abcdef1234567890",
			wantPiece: "secret=[REDACTED]}",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Redact(tc.input)
			if strings.Contains(got, tc.leak) {
				t.Fatalf("secret leaked:\n  in:  %s\n  out: %s", tc.input, got)
			}
			if !strings.Contains(got, tc.wantPiece) {
				t.Fatalf("delimiter was not preserved:\n  want piece: %s\n  out:        %s", tc.wantPiece, got)
			}
		})
	}
}

// TestRedactJSONRoundTrip verifies RedactAny on a typed struct via JSON round-trip.
func TestRedactJSONRoundTrip(t *testing.T) {
	r := NewSecretRedactor()
	type creds struct {
		Token string `json:"token"`
		Name  string `json:"name"`
	}
	in := creds{Token: "ghp" + "_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef1234", Name: "svc"}

	got := r.RedactAny(in)
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any from JSON round-trip, got %T", got)
	}
	if token, _ := m["token"].(string); strings.Contains(token, "ghp_") {
		t.Errorf("token should be redacted in JSON round-trip: %v", m["token"])
	}
	if name, _ := m["name"].(string); name != "svc" {
		t.Errorf("normal field should be preserved, got %v", m["name"])
	}
}
