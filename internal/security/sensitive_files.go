package security

import (
	"path/filepath"
	"strings"
)

// ClassifySensitivePath classifies credential-like file names, independent of
// their directory. It does not inspect content or treat directory names as
// secrets; callers decide whether a target is a file before applying the floor.
func ClassifySensitivePath(path string) (sensitive bool, reason string) {
	name := strings.ToLower(filepath.Base(path))
	if name == ".env" || strings.HasPrefix(name, ".env.") {
		for _, suffix := range []string{".example", ".sample", ".template", ".dist", ".defaults"} {
			if strings.HasSuffix(name, suffix) {
				return false, ""
			}
		}
		return true, "environment credentials"
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore"} {
		if strings.HasSuffix(name, suffix) {
			return true, "private key or keystore"
		}
	}
	// SSH private keys are id_<type> or a custom id_<name> with no extension
	// (ssh-keygen -f ~/.ssh/id_github). An extension marks ordinary source such
	// as id_generator.go; .pub is the public half.
	if strings.HasPrefix(name, "id_") && !strings.Contains(name, ".") {
		return true, "private SSH key"
	}
	switch name {
	case "credentials.json", ".netrc", ".git-credentials", ".pypirc", ".htpasswd":
		return true, "credential file"
	}
	if strings.HasSuffix(name, ".tfstate") || strings.HasSuffix(name, ".tfstate.backup") {
		return true, "Terraform state credentials"
	}
	if strings.HasPrefix(name, "service-account") && strings.HasSuffix(name, ".json") {
		return true, "service account credentials"
	}
	return false, ""
}
