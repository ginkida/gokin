package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstRunWorkspaceSelectionDefaultsToProjectAndSupportsSpaces(t *testing.T) {
	work := t.TempDir()
	home := t.TempDir()
	extra := filepath.Join(work, "folder with spaces")
	if err := os.Mkdir(extra, 0700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ input, want string }{
		{"\n", work}, {"", work}, {"invalid\n", work}, {"1\n", work},
		{"2\nfolder with spaces\n", extra}, {"2\n\n", work}, {"2\nmissing-folder\n", work},
		{"3\n", home},
	} {
		var output bytes.Buffer
		if got := chooseInitialWorkspaceDir(strings.NewReader(test.input), &output, work, home); got != test.want {
			t.Fatalf("input %q: got %q want %q", test.input, got, test.want)
		}
		if !strings.Contains(output.String(), "Current project only (recommended)") {
			t.Fatal("first-run default is not explained")
		}
	}
}
