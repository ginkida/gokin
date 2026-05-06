package setup

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gokin/internal/config"
	"gokin/internal/security"

	"github.com/ollama/ollama/api"
	"gopkg.in/yaml.v3"
)

// ANSI color codes for enhanced output
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
)

const (
	welcomeMessage = `
%s╔══════════════════════════════════════════════════════════════════════════╗
║                                                                          ║
║                            %sWelcome to Gokin!%s                             ║
║         AI assistant: GLM, MiniMax, Kimi, DeepSeek & Ollama              ║
║                                                                          ║
╚══════════════════════════════════════════════════════════════════════════╝%s

Gokin helps you work with code:
  • Read, create, and edit files
  • Execute terminal commands
  • Search your project (glob, grep, tree)
  • Manage Git (commit, log, diff)
  • And much more!

Choose your AI provider to get started.
`
)

// Spinner animation frames
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type setupChoice struct {
	Action string
	Title  string
	Lines  []string
}

func buildSetupChoices() []setupChoice {
	var choices []setupChoice

	// Present choices in user-facing priority order — Kimi first (v0.69 default),
	// then the other cloud providers, then Ollama. Registry order stays untouched
	// for programmatic callers; the wizard is UI-only.
	ordered := make([]config.ProviderDef, 0, len(config.Providers))
	priority := []string{"kimi", "glm", "deepseek", "minimax", "ollama"}
	seen := map[string]bool{}
	for _, name := range priority {
		if p := config.GetProvider(name); p != nil {
			ordered = append(ordered, *p)
			seen[name] = true
		}
	}
	for _, p := range config.Providers {
		if !seen[p.Name] {
			ordered = append(ordered, p)
		}
	}

	for _, p := range ordered {
		switch p.Name {
		case "glm":
			choices = append(choices, setupChoice{
				Action: "api:" + p.Name,
				Title:  "GLM Coding Plan",
				Lines: []string{
					"GLM-4/GLM-5 models via Z.ai",
					"Optimized for code tasks",
					"Budget-friendly (~$3/month)",
					"Get key at: " + p.SetupKeyURL,
				},
			})
		case "deepseek":
			choices = append(choices, setupChoice{
				Action: "api:" + p.Name,
				Title:  "DeepSeek (Cloud)",
				Lines: []string{
					"DeepSeek V4 Pro/Flash — top SWE-bench reasoning",
					"Anthropic-compatible at api.deepseek.com/anthropic",
					"Extended thinking on V4 & reasoner variants",
					"Get key at: " + p.SetupKeyURL,
				},
			})
		case "anthropic":
			choices = append(choices, setupChoice{
				Action: "api:" + p.Name,
				Title:  "Anthropic (Cloud)",
				Lines: []string{
					"Claude Sonnet & Haiku models",
					"Extended thinking support",
					"Get key at: " + p.SetupKeyURL,
				},
			})
		case "minimax":
			choices = append(choices, setupChoice{
				Action: "api:" + p.Name,
				Title:  "MiniMax (Cloud)",
				Lines: []string{
					"MiniMax M2.7: 200K context, M2.7-highspeed available",
					"Anthropic-compatible API",
					"Get key at: " + p.SetupKeyURL,
				},
			})
		case "kimi":
			choices = append(choices, setupChoice{
				Action: "api:" + p.Name,
				Title:  "Kimi Coding Plan (recommended)",
				Lines: []string{
					"K2.6 model, 262K context, extended thinking",
					"Anthropic-compatible via api.kimi.com/coding",
					"Get key at: " + p.SetupKeyURL,
				},
			})
		case "ollama":
			choices = append(choices, setupChoice{
				Action: "ollama",
				Title:  "Ollama (Local)",
				Lines: []string{
					"Run LLMs locally, no API key needed",
					"Privacy-focused, works offline",
					"Requires: ollama serve",
				},
			})
		}
	}

	return choices
}

// detectEnvAPIKeys checks for API key environment variables and returns the first found.
// Returns (envVarName, backend, apiKey) or empty strings if none found.
//
// Legacy GOKIN_API_KEY was routed to the Gemini backend in pre-v0.65 builds.
// Gemini was removed in v0.65.0, so that code path is gone — a user with only
// GOKIN_API_KEY set will fall through to the provider-picker menu, which is
// the right outcome (we don't know which provider that key belongs to).
func detectEnvAPIKeys() (string, string, string) {
	// Check provider-specific env vars via registry
	for _, p := range config.Providers {
		if p.KeyOptional {
			continue
		}
		for _, envVar := range p.EnvVars {
			if key := os.Getenv(envVar); key != "" {
				return envVar, p.Name, key
			}
		}
	}
	return "", "", ""
}

// RunSetupWizard runs the enhanced first-time setup wizard.
func RunSetupWizard() error {
	// Print colorful welcome message
	fmt.Printf(welcomeMessage, colorCyan, colorBold, colorCyan, colorReset)

	reader := bufio.NewReader(os.Stdin)

	// Check for existing env var API keys
	if envVar, backend, apiKey := detectEnvAPIKeys(); envVar != "" {
		fmt.Printf("\n%s✓ Found %s in environment.%s\n", colorGreen, envVar, colorReset)
		fmt.Printf("%sUse it for setup? [Y/n]:%s ", colorCyan, colorReset)

		answer, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("error reading input: %w", err)
		}

		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer == "" || answer == "y" || answer == "yes" {
			ve := runValidation(backend, apiKey)
			proceed := true
			switch {
			case ve == nil:
				// ok
			case ve.Kind == ValidationBadKey:
				fmt.Printf("\n%s✗ Key validation failed: %s%s\n", colorRed, ve.Error(), colorReset)
				fmt.Printf("%sContinuing with manual setup...%s\n\n", colorYellow, colorReset)
				proceed = false
			default:
				if !promptSaveDespiteValidation(reader, ve) {
					proceed = false
				}
			}
			if proceed {
				configPath, err := saveProviderConfig(backend, apiKey, "")
				if err != nil {
					return err
				}
				fmt.Printf("\n%s✓ Configured with %s!%s\n", colorGreen, envVar, colorReset)
				fmt.Printf("  %sConfig:%s %s\n", colorYellow, colorReset, configPath)
				showNextSteps()
				return nil
			}
		}
	}

	choices := buildSetupChoices()
	for {
		fmt.Printf("\n%sChoose AI provider:%s\n\n", colorYellow, colorReset)
		for i, ch := range choices {
			fmt.Printf("  %s[%d]%s %s\n", colorGreen, i+1, colorReset, ch.Title)
			for _, line := range ch.Lines {
				fmt.Printf("                       • %s\n", line)
			}
			fmt.Println()
		}
		fmt.Printf("%sEnter your choice (1-%d):%s ", colorCyan, len(choices), colorReset)

		choice, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("error reading input: %w", err)
		}

		choice = strings.TrimSpace(choice)
		idx, parseErr := strconv.Atoi(choice)
		if parseErr != nil || idx < 1 || idx > len(choices) {
			fmt.Printf("\n%s⚠ Invalid choice. Please enter a number from 1 to %d.%s\n", colorRed, len(choices), colorReset)
			continue
		}

		selected := choices[idx-1]
		switch {
		case selected.Action == "ollama":
			return setupOllama(reader)
		case strings.HasPrefix(selected.Action, "api:"):
			return setupAPIKey(reader, strings.TrimPrefix(selected.Action, "api:"))
		default:
			return fmt.Errorf("unsupported setup action: %s", selected.Action)
		}
	}
}

func setupAPIKey(reader *bufio.Reader, backend string) error {
	p := config.GetProvider(backend)
	if p == nil {
		return fmt.Errorf("unsupported provider: %s", backend)
	}
	keyType := p.DisplayName
	keyURL := p.SetupKeyURL

	fmt.Printf("\n%s─── %s API Key Setup ───%s\n", colorCyan, keyType, colorReset)
	fmt.Printf("\n%sGet your key at:%s\n", colorYellow, colorReset)
	fmt.Printf("  %s%s%s\n\n", colorBold, keyURL, colorReset)
	fmt.Printf("%sEnter API key:%s ", colorGreen, colorReset)

	apiKey, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}

	apiKey = strings.TrimSpace(apiKey)

	if len(apiKey) < 10 {
		return fmt.Errorf("invalid API key format (too short)")
	}

	ve := runValidation(backend, apiKey)
	switch {
	case ve == nil:
		// validated
	case ve.Kind == ValidationBadKey:
		fmt.Printf("\n%s✗ The %s server rejected this key (HTTP %d).%s\n", colorRed, p.DisplayName, ve.StatusCode, colorReset)
		if p.SetupKeyURL != "" {
			fmt.Printf("  Make sure you copied the full key from %s%s%s\n", colorBold, p.SetupKeyURL, colorReset)
		}
		fmt.Printf("  Then run %sgokin --setup%s to try again.\n", colorBold, colorReset)
		return fmt.Errorf("API key validation failed: %w", ve)
	default:
		if !promptSaveDespiteValidation(reader, ve) {
			return fmt.Errorf("setup cancelled by user after validation warning")
		}
	}

	configPath, err := saveProviderConfig(backend, apiKey, p.DefaultModel)
	if err != nil {
		return err
	}

	fmt.Printf("\n%s✓ %s API key saved!%s\n", colorGreen, keyType, colorReset)
	fmt.Printf("  %sConfig:%s %s\n", colorYellow, colorReset, configPath)
	switch backend {
	case "glm":
		fmt.Printf("  %sEndpoint:%s Z.ai Coding Plan\n", colorYellow, colorReset)
	case "kimi":
		fmt.Printf("  %sEndpoint:%s Kimi Coding Plan (api.kimi.com/coding)\n", colorYellow, colorReset)
	case "deepseek":
		fmt.Printf("  %sEndpoint:%s DeepSeek (api.deepseek.com/anthropic)\n", colorYellow, colorReset)
	}

	// Show next steps
	showNextSteps()

	return nil
}

// saveProviderConfig persists backend + apiKey + default model without
// clobbering any unrelated fields already present in config.yaml (other
// provider keys, MCP servers, tools.allowed_dirs, theme, …).
//
// We load existing YAML as a generic tree, mutate only the api/model
// sections, and write atomically. If no config file exists yet, we start
// from an empty tree.
//
// The key is written to the provider-specific field (glm_key, kimi_key, …).
// We deliberately do NOT touch the legacy api.api_key field so that users
// with a custom api_key keep it for other purposes.
func saveProviderConfig(backend, apiKey, modelName string) (string, error) {
	p := config.GetProvider(backend)
	if p == nil {
		return "", fmt.Errorf("unknown provider: %s", backend)
	}
	if modelName == "" {
		modelName = p.DefaultModel
	}

	configPath, err := getConfigPath()
	if err != nil {
		return "", fmt.Errorf("failed to get config path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	root, err := loadRawConfigOrEmpty(configPath)
	if err != nil {
		return "", fmt.Errorf("failed to read existing config: %w", err)
	}

	api := ensureMap(root, "api")
	// Write to the provider-specific key field (glm_key / kimi_key / minimax_key).
	keyField := backend + "_key"
	api[keyField] = apiKey
	api["active_provider"] = backend
	api["backend"] = backend // keep legacy alias in sync for older readers

	model := ensureMap(root, "model")
	model["provider"] = backend
	model["name"] = modelName

	data, err := yaml.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("failed to marshal config: %w", err)
	}
	if err := writeFileAtomic(configPath, data, 0600); err != nil {
		return "", fmt.Errorf("failed to save config: %w", err)
	}
	return configPath, nil
}

// saveOllamaConfig persists an Ollama setup (local or cloud) while preserving
// every other field already in config.yaml. Earlier wizard code used raw
// os.WriteFile with a hand-built YAML literal, which silently wiped every
// non-Ollama key (GLM/Kimi/MiniMax/DeepSeek keys, MCP servers, aliases, …)
// the user had configured before running `gokin --setup` and picking Ollama.
//
// apiKey is optional — only the Cloud variant collects one, Local mode leaves
// it empty. serverURL is optional — Local users can keep the default
// localhost endpoint by submitting an empty value, Cloud users get the
// fixed ollama.com URL.
func saveOllamaConfig(apiKey, modelName, serverURL string) (string, error) {
	if modelName == "" {
		modelName = "llama3.2"
	}

	configPath, err := getConfigPath()
	if err != nil {
		return "", fmt.Errorf("failed to get config path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	root, err := loadRawConfigOrEmpty(configPath)
	if err != nil {
		return "", fmt.Errorf("failed to read existing config: %w", err)
	}

	api := ensureMap(root, "api")
	api["active_provider"] = "ollama"
	api["backend"] = "ollama"
	// Reset ollama-specific fields before applying the current mode. Without
	// this, a user who ran Cloud setup (writing ollama_key + ollama_base_url=
	// https://ollama.com) and later re-ran the wizard picking Local with the
	// default endpoint would end up with stale cloud settings still in place
	// — the client would keep hitting ollama.com. Other providers' keys and
	// unrelated config sections are intentionally untouched.
	delete(api, "ollama_key")
	delete(api, "ollama_base_url")
	if apiKey != "" {
		api["ollama_key"] = apiKey
	}
	if serverURL != "" {
		api["ollama_base_url"] = serverURL
	}

	model := ensureMap(root, "model")
	model["provider"] = "ollama"
	model["name"] = modelName

	data, err := yaml.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("failed to marshal config: %w", err)
	}
	if err := writeFileAtomic(configPath, data, 0600); err != nil {
		return "", fmt.Errorf("failed to save config: %w", err)
	}
	return configPath, nil
}

// loadRawConfigOrEmpty reads config.yaml as a generic YAML tree so we can
// preserve unknown fields across a save. Missing file returns an empty map.
func loadRawConfigOrEmpty(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

// ensureMap returns the submap at parent[key], creating it (and coercing
// legacy map[any]any shapes) if necessary.
func ensureMap(parent map[string]any, key string) map[string]any {
	switch sub := parent[key].(type) {
	case map[string]any:
		return sub
	case map[any]any:
		converted := make(map[string]any, len(sub))
		for k, v := range sub {
			converted[fmt.Sprintf("%v", k)] = v
		}
		parent[key] = converted
		return converted
	}
	fresh := map[string]any{}
	parent[key] = fresh
	return fresh
}

// writeFileAtomic writes data to path via a temp file + rename so an
// interrupted save can't leave a partial config on disk.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return os.WriteFile(path, data, perm)
	}
	return nil
}

func setupOllama(reader *bufio.Reader) error {
	fmt.Printf("\n%s─── Ollama Setup ───%s\n", colorCyan, colorReset)
	fmt.Printf(`
%sChoose Ollama mode:%s

  %s[1]%s Local        • Run on your machine (requires GPU)
                   • Free, private, works offline

  %s[2]%s Cloud        • Run on Ollama Cloud (no GPU needed)
                   • Requires API key from ollama.com

%sEnter your choice (1 or 2):%s `, colorYellow, colorReset, colorGreen, colorReset, colorGreen, colorReset, colorCyan, colorReset)

	choice, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}

	choice = strings.TrimSpace(choice)

	switch choice {
	case "1":
		return setupOllamaLocal(reader)
	case "2":
		return setupOllamaCloud(reader)
	default:
		fmt.Printf("\n%s⚠ Invalid choice, defaulting to Local.%s\n", colorYellow, colorReset)
		return setupOllamaLocal(reader)
	}
}

func setupOllamaLocal(reader *bufio.Reader) error {
	fmt.Printf("\n%s─── Ollama Local Setup ───%s\n", colorCyan, colorReset)
	fmt.Printf("\n%sPrerequisites:%s\n", colorYellow, colorReset)
	fmt.Printf("  1. Install Ollama: %scurl -fsSL https://ollama.ai/install.sh | sh%s\n", colorBold, colorReset)
	fmt.Printf("  2. Start server:   %sollama serve%s\n", colorBold, colorReset)
	fmt.Printf("  3. Pull a model:   %sollama pull llama3.2%s\n\n", colorBold, colorReset)

	// Check for installed models
	fmt.Printf("%sChecking installed models...%s\n", colorYellow, colorReset)

	models, err := detectInstalledOllamaModels("")
	if err != nil {
		fmt.Printf("  %s⚠ Could not connect to Ollama: %s%s\n", colorRed, err, colorReset)
		fmt.Printf("  %sMake sure Ollama is running: ollama serve%s\n\n", colorYellow, colorReset)
	} else if len(models) > 0 {
		fmt.Printf("  %s✓ Found %d installed model(s):%s\n", colorGreen, len(models), colorReset)
		for i, m := range models {
			if i < 5 { // Show first 5
				fmt.Printf("    • %s\n", m)
			}
		}
		if len(models) > 5 {
			fmt.Printf("    • ... and %d more\n", len(models)-5)
		}
		fmt.Println()
	} else {
		fmt.Printf("  %s⚠ No models installed. Run: ollama pull llama3.2%s\n\n", colorYellow, colorReset)
	}

	fmt.Printf("%sEnter model name (or press Enter for 'llama3.2'):%s ", colorGreen, colorReset)

	modelName, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}

	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		modelName = "llama3.2"
	}

	// Ask for remote URL (optional)
	fmt.Printf("\n%sOllama server URL (press Enter for local '%s'):%s ", colorGreen, config.DefaultOllamaBaseURL, colorReset)

	serverURL, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}

	serverURL = strings.TrimSpace(serverURL)

	configPath, err := saveOllamaConfig("", modelName, serverURL)
	if err != nil {
		return err
	}

	fmt.Printf("\n%s✓ Ollama Local configured!%s\n", colorGreen, colorReset)
	fmt.Printf("  %sConfig:%s %s\n", colorYellow, colorReset, configPath)
	fmt.Printf("  %sModel:%s %s\n", colorYellow, colorReset, modelName)
	if serverURL != "" {
		fmt.Printf("  %sServer:%s %s\n", colorYellow, colorReset, serverURL)
	}

	showOllamaLocalNextSteps(modelName)

	return nil
}

func setupOllamaCloud(reader *bufio.Reader) error {
	fmt.Printf("\n%s─── Ollama Cloud Setup ───%s\n", colorCyan, colorReset)
	fmt.Printf("\n%sOllama Cloud runs models on remote servers — no local GPU needed.%s\n", colorYellow, colorReset)
	fmt.Printf("\n%sGet your API key:%s\n", colorYellow, colorReset)
	fmt.Printf("  1. Sign in: %sollama signin%s\n", colorBold, colorReset)
	fmt.Printf("  2. Or get key at: %shttps://ollama.com/settings/keys%s\n\n", colorBold, colorReset)

	fmt.Printf("%sEnter Ollama API key:%s ", colorGreen, colorReset)

	apiKey, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}

	apiKey = strings.TrimSpace(apiKey)
	if len(apiKey) < 10 {
		return fmt.Errorf("invalid API key format (too short)")
	}

	fmt.Printf("\n%sEnter model name (or press Enter for 'llama3.2'):%s ", colorGreen, colorReset)

	modelName, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}

	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		modelName = "llama3.2"
	}

	configPath, err := saveOllamaConfig(apiKey, modelName, "https://ollama.com")
	if err != nil {
		return err
	}

	fmt.Printf("\n%s✓ Ollama Cloud configured!%s\n", colorGreen, colorReset)
	fmt.Printf("  %sConfig:%s %s\n", colorYellow, colorReset, configPath)
	fmt.Printf("  %sModel:%s %s\n", colorYellow, colorReset, modelName)
	fmt.Printf("  %sEndpoint:%s https://ollama.com\n", colorYellow, colorReset)

	showOllamaCloudNextSteps()

	return nil
}

func showNextSteps() {
	fmt.Printf(`
%s─── Next Steps ───%s

  1. Run %sgokin%s in your project directory
  2. Run %s/quickstart%s to see practical examples
  3. Run %s/doctor%s to validate setup and connectivity
  4. Use %s/help%s to see all commands

%sHappy coding!%s 🚀
`, colorCyan, colorReset, colorBold, colorReset, colorBold, colorReset, colorBold, colorReset, colorBold, colorReset, colorGreen, colorReset)
}

func showOllamaLocalNextSteps(modelName string) {
	fmt.Printf(`
%s─── Next Steps ───%s

  1. Make sure Ollama is running: %sollama serve%s
  2. Pull your model if needed:   %sollama pull %s%s
  3. Run %sgokin%s in your project directory
  4. Run %s/quickstart%s and %s/doctor%s
  5. Use %s/help%s to see all commands

%sTip:%s List installed models with: %sollama list%s

%sHappy coding!%s 🚀
`, colorCyan, colorReset, colorBold, colorReset, colorBold, modelName, colorReset, colorBold, colorReset, colorBold, colorReset, colorBold, colorReset, colorBold, colorReset, colorYellow, colorReset, colorBold, colorReset, colorGreen, colorReset)
}

func showOllamaCloudNextSteps() {
	fmt.Printf(`
%s─── Next Steps ───%s

  1. Run %sgokin%s in your project directory
  2. Run %s/quickstart%s to see practical examples
  3. Run %s/doctor%s to validate setup and connectivity
  4. Use %s/help%s to see all commands

%sTip:%s No local GPU needed — processing runs on Ollama Cloud!

%sHappy coding!%s 🚀
`, colorCyan, colorReset, colorBold, colorReset, colorBold, colorReset, colorBold, colorReset, colorBold, colorReset, colorYellow, colorReset, colorGreen, colorReset)
}

// spin shows a spinner animation while waiting for a task to complete. Once
// the elapsed wall-clock exceeds 3 seconds it appends an elapsed-time counter
// so a slow network doesn't feel like a hang.
func spin(message string, done <-chan bool) {
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	start := time.Now()
	i := 0
	lastLineLen := 0
	clear := func() {
		if lastLineLen > 0 {
			fmt.Printf("\r%s\r", strings.Repeat(" ", lastLineLen))
		}
	}
	for {
		select {
		case <-done:
			clear()
			return
		case <-ticker.C:
			elapsed := time.Since(start)
			frame := spinnerFrames[i%len(spinnerFrames)]
			var line string
			if elapsed >= 3*time.Second {
				line = fmt.Sprintf("%s %s (%ds elapsed — Ctrl+C to abort)", frame, message, int(elapsed.Seconds()))
			} else {
				line = fmt.Sprintf("%s %s", frame, message)
			}
			if len(line) < lastLineLen {
				fmt.Printf("\r%s\r", strings.Repeat(" ", lastLineLen))
			}
			fmt.Printf("\r%s", line)
			lastLineLen = len(line)
			i++
		}
	}
}

// getConfigPath returns the path to the config file.
//
// Delegates to config.GetConfigPath() — previously this function had its own
// copy of the path-resolution logic with OPPOSITE macOS precedence (wizard
// preferred ~/.config while loader preferred ~/Library/Application Support).
// If a user ended up with BOTH files (e.g. manually migrated or re-installed),
// the wizard would save to one location while the loader read from the other
// — the user's newly-saved key would appear to be lost on next launch.
// Going through the loader's canonical helper eliminates that divergence by
// construction.
//
// The error return is preserved so callers don't need updating; we convert
// an empty path (loader's "couldn't resolve home dir" signal) into an error.
func getConfigPath() (string, error) {
	path := config.GetConfigPath()
	if path == "" {
		return "", fmt.Errorf("failed to resolve config path (HOME not set?)")
	}
	return path, nil
}

// detectInstalledOllamaModels returns a list of installed Ollama models.
func detectInstalledOllamaModels(serverURL string) ([]string, error) {
	if serverURL == "" {
		serverURL = config.DefaultOllamaBaseURL
	}

	baseURL, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("invalid server URL: %w", err)
	}

	client := api.NewClient(baseURL, &http.Client{Timeout: 5 * time.Second})

	resp, err := client.List(context.Background())
	if err != nil {
		return nil, err
	}

	models := make([]string, 0, len(resp.Models))
	for _, m := range resp.Models {
		models = append(models, m.Name)
	}
	return models, nil
}

// ValidationKind classifies a key-validation result so the wizard can decide
// whether to hard-fail (bad key) or soft-fail (server was unreachable or
// returned an unexpected status — the key might still be fine).
type ValidationKind int

const (
	// ValidationUnknown covers classification gaps — treated as transient.
	ValidationUnknown ValidationKind = iota
	// ValidationBadKey means the provider explicitly rejected the key (401/403).
	ValidationBadKey
	// ValidationTransient covers network timeouts, 5xx, rate limits, unexpected
	// status codes — i.e. the provider didn't tell us the key is bad.
	ValidationTransient
)

// ValidationError is the structured error returned by validateAPIKey.
type ValidationError struct {
	Kind       ValidationKind
	StatusCode int
	Err        error
}

func (e *ValidationError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *ValidationError) Unwrap() error { return e.Err }

// validationTimeout bounds the online key check. Kept short so a slow or broken
// endpoint can't stall setup — failures above this threshold are classified as
// transient, not as "bad key".
const validationTimeout = 5 * time.Second

// validateAPIKey tests an API key by making a lightweight API call and returns
// a classified error. Nil means "server said 200-ish".
func validateAPIKey(backend, apiKey string) *ValidationError {
	ctx, cancel := context.WithTimeout(context.Background(), validationTimeout)
	defer cancel()

	p := config.GetProvider(backend)
	if p == nil {
		return &ValidationError{Kind: ValidationTransient, Err: fmt.Errorf("unknown provider %q", backend)}
	}

	if p.KeyValidation.URL == "" {
		if len(apiKey) < 20 {
			return &ValidationError{Kind: ValidationBadKey, Err: fmt.Errorf("API key seems too short for %s", backend)}
		}
		return nil
	}

	return validateWithProviderConfig(ctx, p, apiKey)
}

// validateAPIKeyReal is retained for backward compatibility with existing tests.
// Deprecated: use validateAPIKey and inspect ValidationError.Kind instead.
func validateAPIKeyReal(backend, apiKey string) error {
	if ve := validateAPIKey(backend, apiKey); ve != nil {
		return ve
	}
	return nil
}

func validateWithProviderConfig(ctx context.Context, p *config.ProviderDef, apiKey string) *ValidationError {
	v := p.KeyValidation
	reqURL := v.URL

	if v.AuthMode == "query" && v.QueryParam != "" {
		reqURL += "?" + v.QueryParam + "=" + apiKey
	}

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return &ValidationError{Kind: ValidationTransient, Err: err}
	}

	switch v.AuthMode {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+apiKey)
	case "header":
		if v.HeaderName == "" {
			return &ValidationError{Kind: ValidationTransient, Err: fmt.Errorf("provider %s key validation misconfigured: missing header name", p.Name)}
		}
		req.Header.Set(v.HeaderName, apiKey)
	}

	for k, val := range v.ExtraHeaders {
		req.Header.Set(k, val)
	}

	httpClient, err := security.CreateDefaultHTTPClient()
	if err != nil {
		return &ValidationError{Kind: ValidationTransient, Err: fmt.Errorf("failed to create HTTP client: %w", err)}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// Context-deadline / DNS / TLS / connect failures. Not a bad-key signal.
		return &ValidationError{Kind: ValidationTransient, Err: fmt.Errorf("connection error: %w", err)}
	}
	_ = resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return &ValidationError{Kind: ValidationBadKey, StatusCode: resp.StatusCode, Err: fmt.Errorf("invalid API key (HTTP %d)", resp.StatusCode)}
	}

	okStatuses := v.SuccessStatuses
	if len(okStatuses) == 0 {
		okStatuses = []int{200}
	}
	if slices.Contains(okStatuses, resp.StatusCode) {
		return nil
	}

	// Any other status (404/429/5xx/…) — server is reachable but returned
	// something we don't know how to interpret. The key may still be fine,
	// so let the caller soft-fail instead of dead-ending setup.
	return &ValidationError{Kind: ValidationTransient, StatusCode: resp.StatusCode, Err: fmt.Errorf("unexpected response (HTTP %d)", resp.StatusCode)}
}

// shouldSkipValidation returns true when the user opted out via env.
// Useful in CI, during network-restricted demos, or when a provider endpoint
// is known to be flaky.
func shouldSkipValidation() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("GOKIN_SKIP_VALIDATION")))
	return v == "1" || v == "true" || v == "yes"
}

// runValidation runs validateAPIKey behind the spinner, respecting the skip
// env var. Returns nil on success or user-opted-skip.
func runValidation(backend, apiKey string) *ValidationError {
	if shouldSkipValidation() {
		fmt.Printf("%s⏭ Skipping validation (GOKIN_SKIP_VALIDATION set).%s\n", colorYellow, colorReset)
		return nil
	}
	done := make(chan bool, 1)
	var ve *ValidationError
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ve = &ValidationError{Kind: ValidationTransient, Err: fmt.Errorf("validation panic: %v", r)}
			}
			done <- true
		}()
		ve = validateAPIKey(backend, apiKey)
	}()
	spin("Validating API key...", done)
	return ve
}

// promptSaveDespiteValidation asks the user whether to save a key that failed
// transient validation. Returns true to proceed, false to abort.
func promptSaveDespiteValidation(reader *bufio.Reader, ve *ValidationError) bool {
	fmt.Printf("\n%s⚠ Could not verify the key online: %s%s\n", colorYellow, ve.Error(), colorReset)
	fmt.Printf("  This is usually a network/endpoint hiccup — the key itself may be fine.\n")
	fmt.Printf("%sSave it anyway and continue? [Y/n]:%s ", colorCyan, colorReset)

	answer, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "" || answer == "y" || answer == "yes"
}
