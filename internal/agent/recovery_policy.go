package agent

import (
	"regexp"
	"strings"

	"google.golang.org/genai"
)

// ToolRecoveryPolicy centralizes deterministic classification and recovery
// strategy for tool failures.
type ToolRecoveryPolicy struct {
	matchers []recoveryMatcher
}

type recoveryMatcher struct {
	Category string
	Patterns []*regexp.Regexp
}

func NewToolRecoveryPolicy() *ToolRecoveryPolicy {
	return &ToolRecoveryPolicy{
		matchers: []recoveryMatcher{
			{
				Category: "file_not_found",
				Patterns: []*regexp.Regexp{
					regexp.MustCompile(`no such file`),
					regexp.MustCompile(`not found`),
					regexp.MustCompile(`cannot find`),
				},
			},
			{
				Category: "unique_match_error",
				Patterns: []*regexp.Regexp{
					regexp.MustCompile(`unique`),
					regexp.MustCompile(`multiple matches`),
				},
			},
			{
				Category: "command_not_found",
				Patterns: []*regexp.Regexp{
					regexp.MustCompile(`command not found`),
					regexp.MustCompile(`not recognized as an internal or external command`),
				},
			},
			{
				Category: "invalid_args",
				Patterns: []*regexp.Regexp{
					regexp.MustCompile(`validation error`),
					regexp.MustCompile(`invalid argument`),
					regexp.MustCompile(`bad argument`),
				},
			},
			{
				Category: "timeout",
				Patterns: []*regexp.Regexp{
					regexp.MustCompile(`timeout`),
					regexp.MustCompile(`deadline exceeded`),
				},
			},
			{
				Category: "network_error",
				Patterns: []*regexp.Regexp{
					regexp.MustCompile(`connection`),
					regexp.MustCompile(`eof`),
					regexp.MustCompile(`reset by peer`),
				},
			},
			{
				Category: "rate_limit",
				Patterns: []*regexp.Regexp{
					regexp.MustCompile(`rate limit`),
					regexp.MustCompile(`too many requests`),
					regexp.MustCompile(`\b429\b`),
				},
			},
		},
	}
}

func (p *ToolRecoveryPolicy) Classify(reflection *Reflection) string {
	if reflection != nil {
		if category := normalizeRecoveryCategory(reflection.Category); category != "" && category != "unknown" {
			return category
		}
	}
	if p == nil {
		return "unknown"
	}

	errorMsg := ""
	if reflection != nil {
		errorMsg = reflection.Error
	}
	errorMsg = strings.ToLower(strings.TrimSpace(errorMsg))
	for _, matcher := range p.matchers {
		for _, pattern := range matcher.Patterns {
			if pattern.MatchString(errorMsg) {
				return matcher.Category
			}
		}
	}
	return "unknown"
}

func (p *ToolRecoveryPolicy) DeterministicFixChain(category string, reflection *Reflection, originalCall *genai.FunctionCall) []*AutoFixAction {
	if originalCall == nil {
		return nil
	}

	category = normalizeRecoveryCategory(category)
	var chain []*AutoFixAction

	switch category {
	case "file_not_found":
		chain = append(chain, &AutoFixAction{
			FixType:  "modify_and_retry",
			ToolName: "glob",
			ArgModifier: func(originalArgs map[string]any, fixResult string) map[string]any {
				foundPath := firstGlobPath(fixResult)
				if foundPath == "" {
					return nil
				}
				modified := make(map[string]any, len(originalArgs))
				for k, v := range originalArgs {
					modified[k] = v
				}
				for _, key := range []string{"file_path", "path", "filepath", "file"} {
					if _, ok := modified[key]; ok {
						modified[key] = foundPath
						return modified
					}
				}
				modified["file_path"] = foundPath
				return modified
			},
		})
		chain = append(chain, &AutoFixAction{
			FixType:  "run_tool_first",
			ToolName: "glob",
			ToolArgs: buildGlobArgs(originalCall.Args),
		})
	case "unique_match_error":
		chain = append(chain, &AutoFixAction{
			FixType:  "run_tool_first",
			ToolName: "read",
		})
	case "command_not_found":
		chain = append(chain, &AutoFixAction{
			FixType:  "run_tool_first",
			ToolName: "bash",
		})
	case "invalid_args":
		if _, ok := originalCall.Args["file_path"]; ok {
			chain = append(chain, &AutoFixAction{
				FixType:  "run_tool_first",
				ToolName: "read",
			})
		}
		if globArgs := buildGlobArgs(originalCall.Args); globArgs != nil {
			chain = append(chain, &AutoFixAction{
				FixType:  "run_tool_first",
				ToolName: "glob",
				ToolArgs: globArgs,
			})
		}
	case "timeout", "network_error", "rate_limit":
		chain = append(chain, &AutoFixAction{
			FixType:      "retry_with_args",
			ModifiedArgs: originalCall.Args,
		})
	}

	if alt := p.AlternativeTool(category, reflection); alt != "" {
		chain = append(chain, &AutoFixAction{
			FixType:  "run_tool_first",
			ToolName: alt,
		})
	}

	seen := make(map[string]bool)
	out := make([]*AutoFixAction, 0, len(chain))
	for _, fix := range chain {
		if fix == nil {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(fix.FixType)) + ":" + strings.ToLower(strings.TrimSpace(fix.ToolName))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, fix)
	}
	return out
}

func (p *ToolRecoveryPolicy) AlternativeTool(category string, reflection *Reflection) string {
	if reflection != nil {
		alt := strings.ToLower(strings.TrimSpace(reflection.Alternative))
		switch alt {
		case "read", "glob", "bash":
			return alt
		}
	}

	switch normalizeRecoveryCategory(category) {
	case "file_not_found", "unique_match_error":
		return "glob"
	case "invalid_args":
		return "read"
	case "command_not_found":
		return "bash"
	default:
		return ""
	}
}

func normalizeRecoveryCategory(category string) string {
	category = strings.ToLower(strings.TrimSpace(category))
	switch category {
	case "file_not_found", "unique_match_error", "command_not_found", "invalid_args",
		"timeout", "network_error", "rate_limit", "unknown":
		return category
	default:
		return category
	}
}
