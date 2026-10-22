package app

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func chooseInitialWorkspaceDir(input io.Reader, output io.Writer, workDir, homeDir string) string {
	reader := bufio.NewReader(input)
	fmt.Fprintf(output, "\nWorkspace access\n\n  Project: %s\n\n", workDir)
	fmt.Fprintln(output, "  [1] Current project only (recommended)")
	fmt.Fprintln(output, "  [2] Add another folder")
	fmt.Fprintf(output, "  [3] Home folder — all projects under %s\n\n", homeDir)
	fmt.Fprint(output, "  Choice [1/2/3, Enter = 1]: ")
	response, _ := reader.ReadString('\n')
	switch strings.TrimSpace(response) {
	case "3":
		return homeDir
	case "2":
		fmt.Fprint(output, "  Folder path (Enter cancels): ")
		path, _ := reader.ReadString('\n')
		path = strings.TrimSpace(path)
		if path == "" {
			return workDir
		}
		if path == "~" {
			path = homeDir
		} else if strings.HasPrefix(path, "~/") {
			path = filepath.Join(homeDir, path[2:])
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(workDir, path)
		}
		path = filepath.Clean(path)
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			fmt.Fprintln(output, "  Folder unavailable — using the current project only.")
			return workDir
		}
		return path
	default:
		return workDir
	}
}
