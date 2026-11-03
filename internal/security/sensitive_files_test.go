package security_test

import (
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/security"
)

func TestClassifySensitivePath(t *testing.T) {
	for _, sensitive := range []bool{true, false} {
		names := []string{".env", ".env.local", ".env.production", "server.pem", "server.key", "cert.p12", "cert.pfx", "keys.jks", "app.keystore", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "id_custom", "credentials.json", ".netrc", ".git-credentials", ".pypirc", ".htpasswd", "prod.tfstate", "prod.tfstate.backup", "service-account.json", "service-account-prod.json"}
		if !sensitive {
			names = []string{".env.example", ".env.local.sample", ".env.template", ".env.dist", ".env.defaults", "id_rsa.pub", "id_custom.pub", "env.go", "monkey", "credentials.json.example", "foo.tfstate.json", "account-service.json", ".environment", "README.md"}
		}
		for _, name := range names {
			for _, variant := range []string{name, strings.ToUpper(name)} {
				t.Run(variant, func(t *testing.T) {
					got, reason := security.ClassifySensitivePath(filepath.Join("project", variant))
					if got != sensitive || (got && reason == "") || (!got && reason != "") {
						t.Fatalf("%s = %v, %q; want %v", variant, got, reason, sensitive)
					}
				})
			}
		}
	}
}

func TestClassifySensitivePath_IDPrefixedSourceIsNotAKey(t *testing.T) {
	for _, name := range []string{"id_generator.go", "id_test.py", "id_map.json", "id_rsa.pub", "id_ed25519.pub"} {
		if sensitive, _ := security.ClassifySensitivePath(name); sensitive {
			t.Fatalf("%s misclassified as a credential", name)
		}
	}
	for _, name := range []string{"id_rsa", "id_ed25519", "id_ecdsa_sk"} {
		if sensitive, _ := security.ClassifySensitivePath(name); !sensitive {
			t.Fatalf("%s must be classified as a private key", name)
		}
	}
}
