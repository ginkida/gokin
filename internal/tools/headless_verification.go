package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gokin/internal/permission"
	"gokin/internal/security"

	"google.golang.org/genai"
)

// VerificationSummary describes observed checks since the last file mutation.
// It never uses the model's completion claims as evidence. Shell recognition
// is deliberately conservative; unrecognized commands leave status not_run.
type VerificationSummary struct {
	Status string              `json:"status"`
	Checks []VerificationCheck `json:"checks,omitempty"`
}

type VerificationCheck struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Status string `json:"status"`

	inspection bool
}

type verificationCheckKey struct{}

// ContextWithVerificationCheck marks a trusted check whose complete outcome
// is recorded by its runner, rather than inferred from its wrapped shell text.
func ContextWithVerificationCheck(ctx context.Context) context.Context {
	return context.WithValue(ctx, verificationCheckKey{}, true)
}

func (s *HeadlessPermissions) Verification() VerificationSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	summary := VerificationSummary{Status: "not_run"}
	for _, check := range s.verification {
		summary.Checks = append(summary.Checks, check)
	}
	sort.Slice(summary.Checks, func(i, j int) bool {
		if summary.Checks[i].Name == summary.Checks[j].Name {
			return summary.Checks[i].ID < summary.Checks[j].ID
		}
		return summary.Checks[i].Name < summary.Checks[j].Name
	})
	// A refused or failing required check cannot be hidden by a different pass.
	priority := map[string]int{"not_run": 0, "passed": 1, "denied": 2, "failed": 3}
	for _, check := range summary.Checks {
		// Passing workspace inspections do not prove a build or test ran.
		// Their failures and refusals still count as verification limitations.
		if check.inspection && check.Status == "passed" {
			continue
		}
		if priority[check.Status] > priority[summary.Status] {
			summary.Status = check.Status
		}
	}
	return summary
}

func RecordHeadlessVerification(ctx context.Context, name, status string) {
	recordHeadlessVerification(ctx, name, name, status)
}

// RecordHeadlessVerificationCheck keeps project-derived paths and command
// fragments out of public IDs while retaining a readable, redacted label.
func RecordHeadlessVerificationCheck(ctx context.Context, id, label, status string) {
	key := fmt.Sprintf("check:%x", sha256.Sum256([]byte(id)))
	recordHeadlessVerification(ctx, key, verificationCommandLabel(label), status)
}

// RecordHeadlessInspectionCheck replaces earlier outcomes, including denials,
// without treating a clean workspace inspection as build/test evidence.
func RecordHeadlessInspectionCheck(ctx context.Context, id, label, status string) {
	key := fmt.Sprintf("check:%x", sha256.Sum256([]byte(id)))
	recordHeadlessCheck(ctx, VerificationCheck{ID: key, Name: verificationCommandLabel(label), Status: status, inspection: true})
}

func recordHeadlessVerification(ctx context.Context, id, name, status string) {
	recordHeadlessCheck(ctx, VerificationCheck{ID: id, Name: name, Status: status})
}

func recordHeadlessCheck(ctx context.Context, check VerificationCheck) {
	state, _ := ctx.Value(headlessPermissionsKey{}).(*HeadlessPermissions)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.verification == nil {
		state.verification = make(map[string]VerificationCheck)
	}
	state.verification[check.ID] = check
}

// ObserveHeadlessToolResult is called at result finalization in both tool
// dispatchers, including nested calls. Labels are bounded and secret-redacted;
// stable IDs distinguish commands whose displayed labels coincide.
func ObserveHeadlessToolResult(ctx context.Context, call *genai.FunctionCall, result ToolResult) {
	state, _ := ctx.Value(headlessPermissionsKey{}).(*HeadlessPermissions)
	if state == nil || call == nil {
		return
	}
	if call.Name == "task" || call.Name == "task_output" {
		if data, ok := result.Data.(map[string]any); ok {
			id, _ := data["agent_id"].(string)
			status, _ := data["status"].(string)
			if id != "" && status != "" {
				state.mu.Lock()
				if state.childStatuses == nil {
					state.childStatuses = make(map[string]string)
				}
				state.childStatuses[id] = status
				state.mu.Unlock()
			}
		}
	}
	args := call.Args
	if result.HookUpdatedInput != nil {
		args = result.HookUpdatedInput
	}
	command, _ := args["command"].(string)
	validationCommand := call.Name == "bash" && IsVerificationCommand(command)
	runnerRecordsCheck, _ := ctx.Value(verificationCheckKey{}).(bool)
	// An arbitrary shell can write without declaring paths, including before
	// a nonzero exit. Retain earlier proof only for recognized checks or reads.
	trustedWrapper := runnerRecordsCheck && result.HookUpdatedInput == nil
	possibleShellWrite := call.Name == "bash" && !trustedWrapper && !validationCommand && !permission.IsSideEffectFreeBashCommand(command)
	confirmedWrites := (result.Success || HasWorkspaceWriteObservation(result)) && len(WrittenPathsFromResult(result)) > 0
	if possibleShellWrite && result.PolicyBlock == nil || confirmedWrites || result.Success &&
		IsImplementationTool(call.Name) && len(MutationPathsFromResult(call.Name, args, result)) > 0 {
		state.mu.Lock()
		state.verification = nil
		state.mu.Unlock()
	}
	if runnerRecordsCheck {
		return
	}
	name := call.Name
	key := name
	switch name {
	case "run_tests", "verify_code":
		encoded, _ := json.Marshal(args)
		key += fmt.Sprintf(":%x", sha256.Sum256(encoded))
	case "bash":
		if background, _ := args["run_in_background"].(bool); background {
			return
		}
		if !validationCommand {
			return
		}
		// Hash full arguments so retries replace their own outcome and distinct
		// targets remain separate without exposing raw commands in the receipt.
		key += fmt.Sprintf(":%x", sha256.Sum256([]byte(command)))
		name += ": " + verificationCommandLabel(command)
	default:
		return
	}
	status := "failed"
	switch {
	case result.HookStopReason != "":
		status = "not_run"
	case result.PolicyBlock != nil:
		status = "denied"
	case result.VerificationSkipped:
		status = "not_run"
	case result.Success:
		status = "passed"
	}
	recordHeadlessVerification(ctx, key, name, status)
}

var verificationLabelRedactor = security.NewSecretRedactor()
var verificationSecretFlag = regexp.MustCompile(`(?i)(--?(?:password|passwd|token|api[_-]?key|secret)(?:=|\s+))(?:"[^"]*"|'[^']*'|\S+)`)

func verificationCommandLabel(command string) string {
	// Go's -args tail is arbitrary user test input; omit it altogether.
	fields := strings.Fields(command)
	for i, field := range fields {
		if field == "-args" {
			fields = append(fields[:i+1], "[REDACTED]")
			break
		}
	}
	label := verificationLabelRedactor.Redact(strings.Join(fields, " "))
	label = verificationSecretFlag.ReplaceAllString(label, "${1}[REDACTED]")
	runes := []rune(label)
	if len(runes) > 88 {
		label = string(runes[:87]) + "…"
	}
	return label
}

// IsVerificationCommand conservatively recognizes completed build/test/lint
// operations, excluding informational, initialization and dry-run commands.
func IsVerificationCommand(command string) bool {
	// Only && chains preserve every command's success. Pipelines, conditional
	// skips, substitutions and arbitrary shell syntax cannot prove a check ran.
	if strings.ContainsAny(command, "|;\n`$()") || strings.Contains(strings.ReplaceAll(command, "&&", ""), "&") {
		return false
	}
	found := false
	for _, segment := range strings.Split(command, "&&") {
		fields := strings.Fields(strings.TrimSpace(segment))
		if len(fields) == 0 {
			return false
		}
		program := filepath.Base(fields[0])
		for _, field := range fields[1:] {
			flag, _, _ := strings.Cut(field, "=")
			switch flag {
			case "--help", "-h", "-help", "--version", "-version", "-v", "--collect-only", "--list", "-list", "-n", "--dry-run", "--init", "--showConfig", "--listFilesOnly", "--print-config", "--env-info", "--inspect-config", "--show-settings", "--show-files", "--add-noqa":
				// -v means verbose in most runners, but version in tsc.
				if flag == "-v" && program != "tsc" {
					continue
				}
				return false
			}
		}
		if program == "cd" {
			continue
		}
		// A successful && chain still proves its check ran when followed by
		// an inspection/success marker. File redirects and unknown tails do
		// not gain this exception.
		if found && permission.IsSideEffectFreeBashCommand(segment) {
			continue
		}
		switch program {
		case "ruff":
			if len(fields) < 2 || fields[1] != "check" && !(fields[1] == "format" && slices.Contains(fields[2:], "--check")) {
				return false
			}
			found = true
			continue
		case "tsc":
			if slices.Contains(fields[1:], "--all") || slices.Contains(fields[1:], "--clean") {
				return false
			}
			found = true
			continue
		case "pytest", "eslint", "mypy":
			found = true
			continue
		}
		if len(fields) < 2 {
			return false
		}
		verbs := ""
		switch program {
		case "go":
			verbs = "test build vet"
		case "cargo":
			verbs = "test build check clippy"
		case "npm", "pnpm", "yarn", "bun":
			if fields[1] == "run" && len(fields) > 2 {
				fields = fields[1:]
			}
			verbs = "test build lint typecheck check"
		case "make":
			verbs = "test check build lint"
		case "mvn", "gradle", "gradlew":
			verbs = "test check build verify"
		case "python", "python3":
			if len(fields) >= 3 && fields[1] == "-m" && (fields[2] == "pytest" || fields[2] == "unittest" || fields[2] == "compileall") {
				found = true
				continue
			}
		}
		if verbs == "" || !strings.Contains(" "+verbs+" ", " "+fields[1]+" ") {
			return false
		}
		found = true
	}
	return found
}
