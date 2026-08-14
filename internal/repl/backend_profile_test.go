package repl

import (
	"strings"
	"testing"
)

func TestWorkerSandboxProfileKeepsRuntimeReadOnly(t *testing.T) {
	profile := sandboxProfile("/workspace", "/runtime", "/python", "/python-app")
	if !strings.Contains(profile, `(allow process-exec (literal "/python") (literal "/python-app"))`) ||
		strings.Contains(profile, `(allow process*)`) || strings.Contains(profile, `process-fork`) {
		t.Fatalf("sandbox profile retained broad process authority:\n%s", profile)
	}
	if !strings.Contains(profile, `(allow file-read* (subpath "/workspace") (subpath "/runtime")`) {
		t.Fatalf("sandbox profile omitted workspace/runtime reads:\n%s", profile)
	}
	for _, forbidden := range []string{
		`(allow file-write* (subpath "/workspace")`,
		`(allow file-write* (subpath "/runtime")`,
	} {
		if strings.Contains(profile, forbidden) {
			t.Fatalf("sandbox profile retained write authority %q:\n%s", forbidden, profile)
		}
	}
	if !strings.Contains(profile, `(allow file-write* (literal "/dev/null"))`) {
		t.Fatalf("sandbox profile omitted harmless /dev/null sink:\n%s", profile)
	}
}

func TestWorkerSandboxProfileWithNoPythonPathsDoesNotAllowAllExec(t *testing.T) {
	profile := sandboxProfile("/workspace", "/runtime")
	if strings.Contains(profile, "(allow process-exec") {
		t.Fatalf("empty executable allowlist granted process-exec:\n%s", profile)
	}
}
