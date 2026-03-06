package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathValidator_EmptyPath(t *testing.T) {
	v := NewPathValidator(nil, true)
	_, err := v.Validate("")
	if err == nil {
		t.Error("empty path should error")
	}
}

func TestPathValidator_NullByte(t *testing.T) {
	v := NewPathValidator(nil, true)
	_, err := v.Validate("/tmp/test\x00evil")
	if err == nil {
		t.Error("null byte in path should error")
	}
}

func resolveDir(dir string) string {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir
	}
	return resolved
}

func TestPathValidator_AllowedDir(t *testing.T) {
	dir := resolveDir(t.TempDir())
	file := filepath.Join(dir, "test.txt")
	os.WriteFile(file, []byte("test"), 0644)

	v := NewPathValidator([]string{dir}, true)
	resolved, err := v.Validate(file)
	if err != nil {
		t.Fatalf("should allow file in allowed dir: %v", err)
	}
	if resolved == "" {
		t.Error("resolved path should not be empty")
	}
}

func TestPathValidator_OutsideAllowed(t *testing.T) {
	dir := resolveDir(t.TempDir())
	otherDir := resolveDir(t.TempDir())
	file := filepath.Join(otherDir, "test.txt")
	os.WriteFile(file, []byte("test"), 0644)

	v := NewPathValidator([]string{dir}, true)
	_, err := v.Validate(file)
	if err == nil {
		t.Error("should reject file outside allowed dirs")
	}
}

func TestPathValidator_NoRestrictions(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.txt")
	os.WriteFile(file, []byte("test"), 0644)

	v := NewPathValidator(nil, true)
	_, err := v.Validate(file)
	if err != nil {
		t.Errorf("no restrictions should allow all: %v", err)
	}
}

func TestPathValidator_NewFile(t *testing.T) {
	dir := resolveDir(t.TempDir())
	newFile := filepath.Join(dir, "new.txt")

	v := NewPathValidator([]string{dir}, true)
	_, err := v.Validate(newFile)
	if err != nil {
		t.Errorf("new file in allowed dir should be ok: %v", err)
	}
}

func TestPathValidator_ValidateFile(t *testing.T) {
	dir := resolveDir(t.TempDir())
	file := filepath.Join(dir, "test.txt")
	os.WriteFile(file, []byte("test"), 0644)

	v := NewPathValidator([]string{dir}, true)
	_, err := v.ValidateFile(file)
	if err != nil {
		t.Errorf("ValidateFile: %v", err)
	}

	// Parent doesn't exist — use a subdir that truly doesn't exist
	_, err = v.ValidateFile(filepath.Join(dir, "nonexistent_subdir", "file.txt"))
	if err == nil {
		t.Error("ValidateFile should fail if parent doesn't exist")
	}
}

func TestPathValidator_ValidateDir(t *testing.T) {
	dir := resolveDir(t.TempDir())
	subDir := filepath.Join(dir, "sub")
	os.Mkdir(subDir, 0755)

	v := NewPathValidator([]string{dir}, true)
	_, err := v.ValidateDir(subDir)
	if err != nil {
		t.Errorf("ValidateDir: %v", err)
	}

	// File is not a directory
	file := filepath.Join(dir, "file.txt")
	os.WriteFile(file, []byte("test"), 0644)
	_, err = v.ValidateDir(file)
	if err == nil {
		t.Error("file should not pass ValidateDir")
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"normal.txt", "normal.txt"},
		{"../traversal", "__traversal"},
		{"file\x00null", "file_null"},
		{"path/sep", "path_sep"},
		{`win\sep`, `win_sep`},
		{"col:on", "col_on"},
		{"star*", "star_"},
		{"quest?", "quest_"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SanitizeFilename(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestJoinPathSafe(t *testing.T) {
	// Normal join
	result, err := JoinPathSafe("/base", "sub/file.txt")
	if err != nil {
		t.Fatalf("normal join: %v", err)
	}
	if result != filepath.Join("/base", "sub/file.txt") {
		t.Errorf("result = %q", result)
	}

	// Traversal attempt
	_, err = JoinPathSafe("/base", "../../etc/passwd")
	if err == nil {
		t.Error("path traversal should be blocked")
	}

	// Absolute relative path
	_, err = JoinPathSafe("/base", "/etc/passwd")
	if err == nil {
		t.Error("absolute relative path should be blocked")
	}

	// Empty base
	_, err = JoinPathSafe("", "file.txt")
	if err == nil {
		t.Error("empty base should error")
	}
}
