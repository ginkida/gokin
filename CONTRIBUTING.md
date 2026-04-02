# Contributing to Gokin

Thank you for your interest in contributing to Gokin!

## Table of Contents

- [Getting Started](#getting-started)
- [Development Setup](#development-setup)
- [Code Style](#code-style)
- [Testing](#testing)
- [Benchmarking](#benchmarking)
- [Making Changes](#making-changes)
- [Commit Messages](#commit-messages)
- [Pull Requests](#pull-requests)
- [Adding New Tools](#adding-new-tools)
- [Reporting Issues](#reporting-issues)
- [Code of Conduct](#code-of-conduct)

---

## Getting Started

### Prerequisites

- **Go 1.25+** (check with `go version`)
- **Git** (for version control)

### Setup

```bash
# Clone the repository
git clone https://github.com/ginkida/gokin.git
cd gokin

# Install dependencies
go mod download

# Verify installation
go mod verify

# Build
go build -o gokin ./cmd/gokin

# Run
./gokin
```

## Development Setup

```bash
# Development build with version info
go build -ldflags "-s -w -X main.version=$(git describe --tags --always)" -o gokin ./cmd/gokin

# Run all tests with race detector
go test -race ./...

# Run tests with coverage
go test -coverprofile=coverage.out ./...

# View coverage report
go tool cover -html=coverage.out
```

## Code Style

### Formatting

- We use `gofmt` for code formatting
- 120 character line limit
- Use `goimports` for import management

```bash
# Format all code
gofmt -w .

# Format specific file
gofmt -w internal/agent/agent.go
```

### Linting

We use golangci-lint for linting:

```bash
# Install golangci-lint
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin

# Run linter
golangci-lint run ./...

# Run with timeout
golangci-lint run ./... --timeout=5m
```

### Static Analysis

```bash
# Run go vet
go vet ./...

# Run with all checks
go vet -all ./...
```

### Naming Conventions

| Type | Convention | Example |
|------|------------|---------|
| Packages | lowercase, no underscores | `internal/agent` |
| Types | PascalCase | `Agent`, `TokenCounter` |
| Variables | camelCase | `maxTokens`, `isReady` |
| Constants | PascalCase or camelCase | `MaxHistorySize` or `maxHistorySize` |
| Functions (exported) | PascalCase | `NewAgent`, `LoadConfig` |
| Functions (unexported) | camelCase | `calculateTokens`, `parseMessage` |
| Interfaces | PascalCase with "er" suffix | `Reader`, `Writer` |

### Error Handling

- Always handle errors explicitly
- Use `fmt.Errorf` with `%w` for wrapped errors
- Never use `log.Print` for errors in libraries
- Return meaningful error messages

```go
// Good
if err != nil {
    return fmt.Errorf("failed to load config: %w", err)
}

// Bad
if err != nil {
    log.Printf("error: %v", err)
    return err
}
```

## Testing

### Writing Tests

- Use table-driven tests for multiple test cases
- Name test functions: `Test<Unit>_<Scenario>`
- Use descriptive subtest names

```go
func TestTokenCounter_CountTokens(t *testing.T) {
    tests := []struct {
        name    string
        text    string
        want    int
    }{
        {
            name:    "empty string",
            text:    "",
            want:    0,
        },
        {
            name:    "simple text",
            text:    "Hello, world!",
            want:    4,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := EstimateTokens(tt.text)
            if got != tt.want {
                t.Errorf("EstimateTokens() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

### Running Tests

```bash
# Run all tests
go test ./...

# Run tests with verbose output
go test -v ./...

# Run tests with race detector
go test -race ./...

# Run specific package tests
go test -v ./internal/mcp/...

# Run tests matching pattern
go test -v -run "TestToken" ./internal/context/...

# Skip slow tests
go test -short ./...

# Run with coverage
go test -coverprofile=coverage.out -covermode=atomic ./...
```

### Test Coverage

We aim for meaningful test coverage, not arbitrary percentages:

```bash
# Get coverage for all packages
go test -coverprofile=coverage.out ./...

# View coverage in browser
go tool cover -html=coverage.out

# Get summary
go tool cover -func=coverage.out
```

## Benchmarking

### Writing Benchmarks

- Use `Benchmark` prefix for benchmark functions
- Use `-benchtime` for longer runs
- Measure memory with `-benchmem`

```go
func BenchmarkTokenCounting_SimpleText(b *testing.B) {
    text := "This is a simple test message with some words."

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _ = EstimateTokens(text)
    }
}
```

### Running Benchmarks

```bash
# Run all benchmarks
go test -bench=. ./...

# Run with memory statistics
go test -bench=. -benchmem ./...

# Run specific benchmark
go test -bench=BenchmarkTokenCounting -benchmem ./internal/context/...

# Run with longer duration
go test -bench=. -benchtime=5s ./...

# Compare benchmarks (before/after)
benchstat old.txt new.txt
```

### Benchmark CI

Benchmarks are automatically run in CI and results are uploaded as artifacts.

## Making Changes

### Workflow

1. **Fork** the repository on GitHub
2. **Clone** your fork locally
3. **Create** a feature branch:
   ```bash
   git checkout -b feature/my-feature
   # or
   git checkout -b fix/my-fix
   ```
4. **Make** your changes
5. **Test** thoroughly:
   ```bash
   go test -race ./...
   go vet ./...
   golangci-lint run ./...
   ```
6. **Commit** with clear messages (see below)
7. **Push** to your fork
8. **Open** a Pull Request

### Branch Naming

| Type | Prefix | Example |
|------|--------|---------|
| Feature | `feature/` | `feature/add-semantic-search` |
| Bug Fix | `fix/` | `fix/token-counting-bug` |
| Refactor | `refactor/` | `refactor/context-manager` |
| Documentation | `docs/` | `docs/update-readme` |
| Performance | `perf/` | `perf/optimize-token-counter` |
| Chore | `chore/` | `chore/update-dependencies` |

## Commit Messages

### Format

```
<type>: <short description>

<longer description (optional)>

<footer (optional)>
```

### Types

| Type | Description |
|------|-------------|
| `feat` | New feature |
| `fix` | Bug fix |
| `docs` | Documentation changes |
| `style` | Code style changes (formatting, etc.) |
| `refactor` | Code refactoring |
| `perf` | Performance improvements |
| `test` | Adding or updating tests |
| `chore` | Maintenance tasks |
| `revert` | Reverting a previous commit |

### Examples

```
feat: add semantic search support for code exploration

This adds a new semantic search tool that uses embeddings to find
relevant code snippets based on natural language queries.

Closes #123
```

```
fix: resolve token counting issue with large code blocks

The token counter was incorrectly estimating tokens for code blocks
containing many identifiers. This fix adjusts the estimation algorithm.
```

```
test: add MCP transport tests

Added comprehensive tests for JSON-RPC message parsing, HTTP transport,
and concurrent send/receive operations.
```

## Pull Requests

### PR Template

When opening a PR, please fill out the template:

```markdown
## Description
Brief description of changes

## Type of Change
- [ ] Bug fix
- [ ] New feature
- [ ] Breaking change
- [ ] Documentation update

## Testing
How was this tested?

## Checklist
- [ ] Code follows style guidelines
- [ ] Self-review completed
- [ ] Comments added for complex code
- [ ] Documentation updated
- [ ] Tests added/updated
- [ ] All tests pass
```

### Review Process

1. Maintainers will review your PR
2. Address any feedback
3. Once approved, maintainers will merge

## Adding New Tools

### 1. Create the Tool File

```go
// internal/tools/my_tool.go
package tools

type MyTool struct {
    // Configuration
}

func (t *MyTool) Name() string {
    return "my_tool"
}

func (t *MyTool) Description() string {
    return "Description of what the tool does"
}

func (t *MyTool) Declaration() *genai.FunctionDeclaration {
    return &genai.FunctionDeclaration{
        Name:        t.Name(),
        Description: t.Description(),
        Parameters: &genai.Schema{
            Type: "object",
            Properties: map[string]*genai.Schema{
                "arg1": {Type: "STRING", Description: "First argument"},
            },
            Required: []string{"arg1"},
        },
    }
}

func (t *MyTool) Validate(args map[string]any) error {
    // Validate arguments
    if _, ok := args["arg1"]; !ok {
        return NewValidationError("arg1", "is required")
    }
    return nil
}

func (t *MyTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
    // Implementation
    return NewSuccessResult("result"), nil
}
```

### 2. Register the Tool

```go
// internal/tools/registry.go
func (r *Registry) registerBuiltInTools() {
    // ... existing tools
    r.tools["my_tool"] = &MyTool{}
}
```

### 3. Add Tests

```go
// internal/tools/my_tool_test.go
package tools

func TestMyTool_Validate(t *testing.T) { /* ... */ }
func TestMyTool_Execute(t *testing.T) { /* ... */ }
```

## Reporting Issues

### Bug Reports

When reporting bugs, please include:

- Clear, descriptive title
- Steps to reproduce
- Expected vs actual behavior
- System information:
  - OS and version
  - Go version (`go version`)
  - Gokin version (`gokin version`)
  - AI provider (Gemini, GLM, etc.)
- Logs (use `--verbose` or `--debug` flag)

### Feature Requests

- Describe the feature
- Explain the use case
- Provide examples

### Security Issues

**Please DO NOT report security vulnerabilities in public issues.**

Instead, email: [ya-ginkida@yandex.kz](mailto:ya-ginkida@yandex.kz)

## Code of Conduct

Please note that this project adheres to a [Code of Conduct](.github/CODE_OF_CONDUCT.md).
By participating, you are expected to uphold this code. Please report unacceptable
behavior to [ya-ginkida@yandex.kz](mailto:ya-ginkida@yandex.kz).

## Questions?

Feel free to open an issue for questions or discussions. We welcome:

- Bug reports
- Feature requests
- Questions
- Documentation improvements

---

## License

By contributing to Gokin, you agree that your contributions will be licensed
under the same license as the project.
