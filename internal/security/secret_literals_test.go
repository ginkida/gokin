package security

import (
	"strings"
	"testing"
)

func TestSecretLiterals_RedactedByEveryRedactor(t *testing.T) {
	resetSecretLiteralsForTest()
	t.Cleanup(resetSecretLiteralsForTest)

	glmKey := "4f1c0a9be2d84c7f" + ".Q2xhdWRlS2V5" // opaque, no known shape
	if out := NewSecretRedactor().Redact("see " + glmKey); out != "see "+glmKey {
		t.Fatalf("precondition: pattern redactor already matches %q", out)
	}
	if !RegisterSecretLiteral(glmKey) {
		t.Fatal("register failed")
	}
	if out := NewSecretRedactor().Redact("config says " + glmKey + " ok"); strings.Contains(out, glmKey) {
		t.Fatalf("literal not redacted: %s", out)
	}
	if RegisterSecretLiteral("short") || RegisterSecretLiteral("has spaces in it ok") {
		t.Fatal("short or whitespace values must be ignored")
	}
}

func TestRegisterSensitiveEnvironment(t *testing.T) {
	resetSecretLiteralsForTest()
	t.Cleanup(resetSecretLiteralsForTest)
	n := RegisterSensitiveEnvironment([]string{
		"CLAUDE_CODE_MESSAGING_TOKEN=ab-cd_ef-" + strings.Repeat("7q", 8),
		"GIT_AUTHOR_EMAIL=someone@example.org",
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"GOPRIVATE=github.com/acme-internal",
		"SECRETS_DIR=/run/secrets/app-config",
	})
	if n != 1 {
		t.Fatalf("registered %d values, want 1 (the token only)", n)
	}
	if out := NewSecretRedactor().Redact("import github.com/acme-internal/x from /run/secrets/app-config by someone@example.org PATH=/usr/local/bin:/usr/bin:/bin"); strings.Contains(out, "REDACTED") {
		t.Fatalf("non-credential values redacted: %s", out)
	}
}

func TestRedactor_EnvStyleCredentialNames(t *testing.T) {
	r := NewSecretRedactor()
	for _, in := range []string{
		"CLAUDE_CODE_MESSAGING_TOKEN=ab-cd_ef-" + strings.Repeat("3k", 9),
		"STRIPE_SECRET=whsec-abc_def-" + strings.Repeat("5", 8),
		"DEPLOY_PRIVATE_KEY=abc-def_ghi-" + strings.Repeat("6", 6),
	} {
		if out := r.Redact(in); !strings.Contains(out, "[REDACTED]") {
			t.Fatalf("not redacted: %s -> %s", in, out)
		}
	}
	for _, in := range []string{"max_tokens=100000000", "input_tokens: 12345678"} {
		if out := r.Redact(in); out != in {
			t.Fatalf("false positive: %s -> %s", in, out)
		}
	}
}

func TestRedactor_DoesNotRewriteSourceCode(t *testing.T) {
	r := NewSecretRedactor()
	for _, in := range []string{
		"Token: cfg.GitHubToken,",
		"token = tokenizer.NextToken()",
		"PrivateKey: priv.PublicKeyBytes,",
		"refreshToken = resp.RefreshToken",
		"next_page_token: response.next_page_token",
	} {
		if out := r.Redact(in); out != in {
			t.Fatalf("source line rewritten: %q -> %q", in, out)
		}
	}
}

func TestRedactor_Idempotent(t *testing.T) {
	resetSecretLiteralsForTest()
	t.Cleanup(resetSecretLiteralsForTest)
	key := "4f1c0a9be2d84c7f" + ".Q2xhdWRlS2V5"
	RegisterSecretLiteral(key)
	r := NewSecretRedactor()
	for _, in := range []string{
		"api_key: " + key,
		"{secret=abcdefgh12345}",
		"STRIPE_SECRET=whsec-abc_def-12345678",
	} {
		once := r.Redact(in)
		if twice := r.Redact(once); twice != once {
			t.Fatalf("not idempotent: %q -> %q -> %q", in, once, twice)
		}
		if strings.Contains(once, "]]") {
			t.Fatalf("corrupted marker: %q -> %q", in, once)
		}
	}
}
