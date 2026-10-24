package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	backgroundstore "gokin/internal/background"

	"github.com/spf13/cobra"
)

type commandStatusError struct {
	message string
	code    int
}

func (e *commandStatusError) Error() string { return e.message }
func (e *commandStatusError) ExitCode() int { return e.code }

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var status *commandStatusError
	if errors.As(err, &status) {
		return status.ExitCode()
	}
	return 1
}

func backgroundTerminalError(job backgroundstore.Job) error {
	if job.State == backgroundstore.StateSucceeded && job.ExitCode == 0 {
		return nil
	}
	return fmt.Errorf("background job %s ended in state %s (exit code %d)", job.ID, job.State, job.ExitCode)
}

func newBackgroundResultCmd() *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use: "result <id>", Short: "Print the final result of a detached Gokin session",
		Long: "Print only the last completed result record. --json preserves the full result envelope. Exit codes: 0 success, 1 missing/failed result or job, 2 job still running.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := backgroundstore.NewStore()
			if err != nil {
				return err
			}
			job, err := resolveBackgroundJob(store, args[0])
			if err != nil {
				return err
			}
			job, err = store.Reconcile(job)
			if err != nil {
				return err
			}
			if !job.Terminal() {
				return &commandStatusError{message: fmt.Sprintf("background job %s is still running (state %s)", job.ID, job.State), code: 2}
			}
			path, _ := store.StdoutPath(job.ID)
			record, err := readBackgroundResult(path)
			if err != nil {
				return fmt.Errorf("read result for background job %s: %w", job.ID, err)
			}
			var result struct {
				Result string `json:"result"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(record, &result); err != nil {
				return err
			}
			if asJSON {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(record))
			} else {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), result.Result)
			}
			if err != nil {
				return err
			}
			if err := backgroundTerminalError(job); err != nil {
				return err
			}
			if result.Status != "success" {
				return fmt.Errorf("background result has status %s", result.Status)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "print the complete final result record as JSON")
	return command
}

func readBackgroundResult(path string) (json.RawMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("background output is not a regular file")
	}
	// Logs are unbounded, but individual result/tool records are bounded here.
	// Keep only the last result: steered workers can complete several turns.
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	var last json.RawMessage
	for scanner.Scan() {
		var frame struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			return nil, fmt.Errorf("invalid background JSONL: %w", err)
		}
		if frame.Type == "result" {
			last = append(last[:0], scanner.Bytes()...)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if last == nil {
		return nil, fmt.Errorf("job ended without a result record")
	}
	return last, nil
}
