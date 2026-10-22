package skills

import (
	"fmt"
	"strings"
)

// DynamicCommand refers to syntax in the original file, never in arguments
// or command output. Start/End include the delimiters and are byte offsets.
type DynamicCommand struct {
	Start, End int
	Command    string
}

const MaxDynamicCommands = 32

// ParseDynamicCommands validates the entire template before any execution.
// Discovery only parses; shell execution belongs to the invoking tool loop.
func ParseDynamicCommands(body string) ([]DynamicCommand, error) {
	var commands []DynamicCommand
	for i := 0; i < len(body); {
		lineStart := i == 0 || body[i-1] == '\n'
		if lineStart {
			end := strings.IndexByte(body[i:], '\n')
			if end < 0 {
				end = len(body) - i
			}
			line := strings.TrimSpace(body[i : i+end])
			fence := ""
			for _, candidate := range []string{"```!", "~~~!"} {
				if strings.HasPrefix(line, candidate) {
					if line != candidate {
						return nil, fmt.Errorf("dynamic context fence must contain only %s", candidate)
					}
					fence = candidate[:3]
				}
			}
			if fence != "" {
				commandStart := i + end + 1
				closed := false
				for next := commandStart; next < len(body); {
					lineEnd := strings.IndexByte(body[next:], '\n')
					if lineEnd < 0 {
						lineEnd = len(body) - next
					}
					if strings.TrimSpace(body[next:next+lineEnd]) == fence {
						commands = append(commands, DynamicCommand{i, next + lineEnd, strings.TrimSpace(body[commandStart:next])})
						i = next + lineEnd
						closed = true
						break
					}
					next += lineEnd + 1
				}
				if !closed {
					return nil, fmt.Errorf("unterminated dynamic context fence")
				}
				if err := validateDynamicCommand(commands); err != nil {
					return nil, err
				}
				continue
			}
		}
		if strings.HasPrefix(body[i:], "!`") && (i == 0 || isDynamicWhitespace(body[i-1])) {
			end := strings.IndexByte(body[i+2:], '`')
			if end < 0 || strings.ContainsAny(body[i+2:i+2+end], "\r\n") {
				return nil, fmt.Errorf("unterminated inline dynamic context command")
			}
			end += i + 2
			commands = append(commands, DynamicCommand{i, end + 1, body[i+2 : end]})
			if err := validateDynamicCommand(commands); err != nil {
				return nil, err
			}
			i = end + 1
			continue
		}
		i++
	}
	return commands, nil
}

func validateDynamicCommand(commands []DynamicCommand) error {
	if len(commands) > MaxDynamicCommands {
		return fmt.Errorf("dynamic context exceeds %d commands", MaxDynamicCommands)
	}
	if strings.TrimSpace(commands[len(commands)-1].Command) == "" {
		return fmt.Errorf("dynamic context command is empty")
	}
	return nil
}

func isDynamicWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}
