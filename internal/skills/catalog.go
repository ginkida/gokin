package skills

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	// Skill bodies travel through the regular tool-result channel. Keeping the
	// source below that channel's cap prevents silently truncated workflows.
	MaxSkillBytes         = 28 * 1024
	maxDescriptionBytes   = 512
	maxDisplayNameBytes   = 128
	maxWhenToUseBytes     = 512
	maxArgumentHintBytes  = 256
	maxSkillArgumentBytes = 64
	maxSkillArgumentCount = 32
	MaxSkillCount         = 128
	MaxSkillScanEntries   = 256
	MaxCatalogBytes       = 2 * 1024 * 1024
)

var (
	validSkillName    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	validArgumentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)
	dynamicContext    = regexp.MustCompile("(?m)(^|[[:space:]])!`")
)

// Root is one skill-discovery directory. Earlier roots have higher priority.
type Root struct {
	Path     string
	Source   string
	Boundary string // Optional trusted ancestor; Path may not resolve outside it.
}

// Skill is one validated, immutable workflow loaded from SKILL.md.
type Skill struct {
	Name                   string
	DisplayName            string
	Description            string
	WhenToUse              string
	ArgumentHint           string
	Arguments              []string
	Body                   string
	Source                 string
	Path                   string
	DisableModelInvocation bool
	UserInvocable          bool
}

type skillFrontmatter struct {
	Name                   yaml.Node `yaml:"name"`
	Description            yaml.Node `yaml:"description"`
	DisableModelInvocation bool      `yaml:"disable-model-invocation"`
	UserInvocable          *bool     `yaml:"user-invocable"`

	// Recognized Claude-compatible metadata. These fields do not grant
	// capabilities, so older Gokin runtimes may safely load the workflow even
	// before they surface the metadata in the UI.
	WhenToUse    yaml.Node `yaml:"when_to_use"`
	ArgumentHint yaml.Node `yaml:"argument-hint"`
	Arguments    yaml.Node `yaml:"arguments"`

	// Recognized execution controls. Silently ignoring any of these could run a
	// workflow with broader tools, a different agent/model, or missing hooks and
	// path/shell constraints. Keep their raw YAML shape for compatibility, then
	// reject them explicitly until the corresponding behavior is implemented.
	AllowedTools    yaml.Node `yaml:"allowed-tools"`
	DisallowedTools yaml.Node `yaml:"disallowed-tools"`
	Model           yaml.Node `yaml:"model"`
	Effort          yaml.Node `yaml:"effort"`
	Context         yaml.Node `yaml:"context"`
	Agent           yaml.Node `yaml:"agent"`
	Hooks           yaml.Node `yaml:"hooks"`
	Paths           yaml.Node `yaml:"paths"`
	Shell           yaml.Node `yaml:"shell"`
}

func (fm skillFrontmatter) unsupportedExecutionField() string {
	fields := []struct {
		name string
		node yaml.Node
	}{
		{"allowed-tools", fm.AllowedTools},
		{"disallowed-tools", fm.DisallowedTools},
		{"model", fm.Model},
		{"effort", fm.Effort},
		{"context", fm.Context},
		{"agent", fm.Agent},
		{"hooks", fm.Hooks},
		{"paths", fm.Paths},
		{"shell", fm.Shell},
	}
	for _, field := range fields {
		if field.node.Kind != 0 {
			return field.name
		}
	}
	return ""
}

// Catalog discovers skills and atomically swaps validated snapshots, so a
// model call can reload files without racing concurrent declaration/list calls.
type Catalog struct {
	mu       sync.RWMutex
	reloadMu sync.Mutex
	roots    []Root
	skills   map[string]Skill
	warnings []string
}

// NewCatalog creates and initially loads a catalog.
func NewCatalog(roots []Root) *Catalog {
	c := &Catalog{
		roots:  append([]Root(nil), roots...),
		skills: make(map[string]Skill),
	}
	c.Reload()
	return c
}

// DefaultRoots returns project and user skill locations. Personal roots win
// over project roots, matching Claude Code's user-over-project precedence;
// native Gokin roots win over Claude-compatible roots at the same scope.
func DefaultRoots(workDir string) []Root {
	projectRoots := []Root{
		{Path: filepath.Join(workDir, ".gokin", "skills"), Source: "project", Boundary: workDir},
		{Path: filepath.Join(workDir, ".claude", "skills"), Source: "claude-project", Boundary: workDir},
	}

	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return projectRoots
	}
	configBase := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if configBase == "" {
		configBase = filepath.Join(home, ".config")
	}
	return append([]Root{
		Root{Path: filepath.Join(configBase, "gokin", "skills"), Source: "global", Boundary: filepath.Join(configBase, "gokin")},
		Root{Path: filepath.Join(home, ".claude", "skills"), Source: "claude-global", Boundary: filepath.Join(home, ".claude")},
	}, projectRoots...)
}

// Reload rescans every root and atomically publishes a fresh snapshot. Broken
// skills are skipped with warnings; one malformed workflow must not disable all
// other project skills.
func (c *Catalog) Reload() []string {
	c.reloadMu.Lock()
	defer c.reloadMu.Unlock()

	next := make(map[string]Skill)
	claimed := make(map[string]bool)
	var warnings []string
	seenRoots := make(map[string]bool)
	totalBytes := 0

	for _, root := range c.roots {
		root.Path = filepath.Clean(strings.TrimSpace(root.Path))
		if root.Path == "." || seenRoots[root.Path] {
			continue
		}
		seenRoots[root.Path] = true

		secureRoot, err := openSkillRoot(root)
		if err != nil {
			if !os.IsNotExist(err) {
				warnings = append(warnings, fmt.Sprintf("open %s skills: %v", root.Source, err))
			}
			continue
		}
		dir, err := secureRoot.Open(".")
		if err != nil {
			secureRoot.Close()
			warnings = append(warnings, fmt.Sprintf("read %s skills: %v", root.Source, err))
			continue
		}
		entries, readErr := dir.ReadDir(MaxSkillScanEntries + 1)
		dir.Close()
		if readErr != nil && readErr != io.EOF {
			secureRoot.Close()
			warnings = append(warnings, fmt.Sprintf("read %s skills: %v", root.Source, readErr))
			continue
		}
		if len(entries) > MaxSkillScanEntries {
			warnings = append(warnings, fmt.Sprintf("%s skills: entry limit %d reached; remaining entries ignored", root.Source, MaxSkillScanEntries))
			entries = entries[:MaxSkillScanEntries]
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

		for _, entry := range entries {
			if len(next) >= MaxSkillCount {
				warnings = append(warnings, fmt.Sprintf("skill count limit %d reached; lower-priority skills ignored", MaxSkillCount))
				break
			}
			// A symlinked directory/file could escape the trusted skill root. Skills
			// are instructions, so fail closed instead of following it.
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			rawDirectoryName := entry.Name()
			directoryName := strings.ToLower(rawDirectoryName)
			relativePath := filepath.Join(entry.Name(), "SKILL.md")
			displayPath := filepath.Join(root.Path, relativePath)
			if rawDirectoryName != directoryName || strings.TrimSpace(rawDirectoryName) != rawDirectoryName || !validSkillName.MatchString(directoryName) {
				warnings = append(warnings, fmt.Sprintf("%s skill %s: invalid skill directory name %q", root.Source, displayPath, entry.Name()))
				continue
			}
			if claimed[directoryName] {
				continue // Earlier (more specific) directory key wins.
			}
			// Presence of a valid higher-priority directory reserves its invocation
			// key even when the document is malformed. Frontmatter metadata may not
			// make discovery fall through to a lower-priority workflow.
			claimed[directoryName] = true

			skill, err := loadSkill(secureRoot, relativePath, displayPath, directoryName, root.Source)
			if err != nil {
				if os.IsNotExist(err) {
					warnings = append(warnings, fmt.Sprintf("%s skill %s: SKILL.md is missing", root.Source, displayPath))
				} else {
					warnings = append(warnings, fmt.Sprintf("%s skill %s: %v", root.Source, displayPath, err))
				}
				continue
			}
			skillBytes := catalogSkillBytes(skill)
			if totalBytes+skillBytes > MaxCatalogBytes {
				warnings = append(warnings, fmt.Sprintf("skill catalog byte limit %d reached; %s ignored", MaxCatalogBytes, displayPath))
				continue
			}
			next[directoryName] = skill
			totalBytes += skillBytes
		}
		secureRoot.Close()
		if len(next) >= MaxSkillCount {
			break
		}
	}

	c.mu.Lock()
	c.skills = next
	c.warnings = append([]string(nil), warnings...)
	c.mu.Unlock()
	return warnings
}

func openSkillRoot(root Root) (*os.Root, error) {
	rootPath, err := filepath.Abs(root.Path)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(root.Boundary) == "" {
		return os.OpenRoot(rootPath)
	}
	boundary, err := filepath.Abs(root.Boundary)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(boundary, rootPath)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("skill root %s escapes boundary %s", rootPath, boundary)
	}
	boundaryRoot, err := os.OpenRoot(boundary)
	if err != nil {
		return nil, err
	}
	defer boundaryRoot.Close()
	return boundaryRoot.OpenRoot(rel)
}

func loadSkill(root *os.Root, relativePath, displayPath, invocationName, source string) (Skill, error) {
	info, err := root.Lstat(relativePath)
	if err != nil {
		return Skill{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Skill{}, fmt.Errorf("SKILL.md must be a regular non-symlink file")
	}

	// os.Root keeps every open beneath the already-open catalog directory even
	// if the path is swapped between Lstat and Open.
	f, err := root.Open(relativePath)
	if err != nil {
		return Skill{}, err
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() {
		return Skill{}, fmt.Errorf("SKILL.md changed to a non-regular file during load")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxSkillBytes+1))
	if err != nil {
		return Skill{}, err
	}
	if len(data) > MaxSkillBytes {
		return Skill{}, fmt.Errorf("file exceeds %d-byte limit", MaxSkillBytes)
	}
	if !utf8.Valid(data) {
		return Skill{}, fmt.Errorf("SKILL.md must be valid UTF-8")
	}

	fm, body, err := parseSkillDocument(string(data))
	if err != nil {
		return Skill{}, err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return Skill{}, fmt.Errorf("skill body is empty")
	}
	if dynamicContext.MatchString(body) || containsDynamicContextFence(body) {
		return Skill{}, fmt.Errorf("Claude dynamic context injection is recognized but unsupported by this runtime")
	}

	displayName, err := boundedMetadataScalar(fm.Name, "name", maxDisplayNameBytes)
	if err != nil {
		return Skill{}, err
	}
	if displayName == "" {
		displayName = invocationName
	}
	description, err := boundedMetadataScalar(fm.Description, "description", maxDescriptionBytes)
	if err != nil {
		return Skill{}, err
	}
	if description == "" {
		description = boundedFallbackDescription(firstMarkdownParagraph(body), maxDescriptionBytes)
	}
	if description == "" {
		return Skill{}, fmt.Errorf("description is required when the body has no markdown paragraph")
	}
	whenToUse, err := boundedMetadataScalar(fm.WhenToUse, "when_to_use", maxWhenToUseBytes)
	if err != nil {
		return Skill{}, err
	}
	argumentHint, err := boundedMetadataScalar(fm.ArgumentHint, "argument-hint", maxArgumentHintBytes)
	if err != nil {
		return Skill{}, err
	}
	arguments, err := parseSkillArguments(fm.Arguments)
	if err != nil {
		return Skill{}, err
	}

	userInvocable := true
	if fm.UserInvocable != nil {
		userInvocable = *fm.UserInvocable
	}
	return Skill{
		Name:                   invocationName,
		DisplayName:            displayName,
		Description:            description,
		WhenToUse:              whenToUse,
		ArgumentHint:           argumentHint,
		Arguments:              arguments,
		Body:                   body,
		Source:                 source,
		Path:                   displayPath,
		DisableModelInvocation: fm.DisableModelInvocation,
		UserInvocable:          userInvocable,
	}, nil
}

func containsDynamicContextFence(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```!") || strings.HasPrefix(line, "~~~!") {
			return true
		}
	}
	return false
}

func boundedMetadataScalar(node yaml.Node, field string, maxBytes int) (string, error) {
	if node.Kind == 0 {
		return "", nil
	}
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("frontmatter %s must be a string scalar", field)
	}
	if !utf8.ValidString(node.Value) {
		return "", fmt.Errorf("frontmatter %s must be valid UTF-8", field)
	}
	value := strings.Join(strings.Fields(node.Value), " ")
	if len(value) > maxBytes {
		return "", fmt.Errorf("frontmatter %s exceeds %d bytes", field, maxBytes)
	}
	return value, nil
}

func parseSkillArguments(node yaml.Node) ([]string, error) {
	if node.Kind == 0 {
		return nil, nil
	}
	var values []string
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag != "!!str" {
			return nil, fmt.Errorf("frontmatter arguments must be a space-separated string or a list of strings")
		}
		values = strings.Fields(node.Value)
	case yaml.SequenceNode:
		values = make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
				return nil, fmt.Errorf("frontmatter arguments must contain only string identifiers")
			}
			values = append(values, strings.TrimSpace(item.Value))
		}
	default:
		return nil, fmt.Errorf("frontmatter arguments must be a space-separated string or a list of strings")
	}

	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if len(value) > maxSkillArgumentBytes || !validArgumentName.MatchString(value) {
			return nil, fmt.Errorf("frontmatter argument %q is not a valid identifier", value)
		}
		if value == "ARGUMENTS" {
			return nil, fmt.Errorf("frontmatter argument %q is reserved", value)
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("frontmatter argument %q is duplicated", value)
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) > maxSkillArgumentCount {
			return nil, fmt.Errorf("frontmatter arguments exceed %d unique identifiers", maxSkillArgumentCount)
		}
	}
	return result, nil
}

func firstMarkdownParagraph(body string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	paragraph := make([]string, 0, 4)
	headingFallback := ""
	inFence := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			if len(paragraph) > 0 {
				return strings.Join(strings.Fields(strings.Join(paragraph, " ")), " ")
			}
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if trimmed == "" {
			if len(paragraph) > 0 {
				return strings.Join(strings.Fields(strings.Join(paragraph, " ")), " ")
			}
			continue
		}
		if len(paragraph) == 0 {
			if heading, ok := markdownHeading(trimmed); ok {
				if headingFallback == "" {
					headingFallback = heading
				}
				continue
			}
			if isMarkdownThematicBreak(trimmed) {
				continue
			}
		}
		paragraph = append(paragraph, trimmed)
	}
	if len(paragraph) > 0 {
		return strings.Join(strings.Fields(strings.Join(paragraph, " ")), " ")
	}
	return strings.Join(strings.Fields(headingFallback), " ")
}

func markdownHeading(line string) (string, bool) {
	count := 0
	for count < len(line) && count < 6 && line[count] == '#' {
		count++
	}
	if count == 0 || (count < len(line) && line[count] != ' ' && line[count] != '\t') {
		return "", false
	}
	return strings.TrimSpace(line[count:]), true
}

func isMarkdownThematicBreak(line string) bool {
	compact := strings.ReplaceAll(strings.ReplaceAll(line, " ", ""), "\t", "")
	if len(compact) < 3 {
		return false
	}
	marker := compact[0]
	if marker != '-' && marker != '*' && marker != '_' {
		return false
	}
	for i := 1; i < len(compact); i++ {
		if compact[i] != marker {
			return false
		}
	}
	return true
}

func boundedFallbackDescription(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxBytes {
		return value
	}
	const ellipsis = "…"
	end := maxBytes - len(ellipsis)
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return strings.TrimSpace(value[:end]) + ellipsis
}

func catalogSkillBytes(skill Skill) int {
	total := len(skill.Name) + len(skill.DisplayName) + len(skill.Description) +
		len(skill.WhenToUse) + len(skill.ArgumentHint) + len(skill.Body) +
		len(skill.Source) + len(skill.Path)
	for _, argument := range skill.Arguments {
		total += len(argument)
	}
	return total
}

func parseSkillDocument(document string) (skillFrontmatter, string, error) {
	document = strings.TrimPrefix(document, "\ufeff")
	document = strings.ReplaceAll(document, "\r\n", "\n")
	if !strings.HasPrefix(document, "---\n") {
		return skillFrontmatter{}, "", fmt.Errorf("YAML frontmatter is required")
	}
	rest := strings.TrimPrefix(document, "---\n")
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return skillFrontmatter{}, "", fmt.Errorf("unterminated frontmatter")
	}
	var fm skillFrontmatter
	decoder := yaml.NewDecoder(strings.NewReader(rest[:idx]))
	decoder.KnownFields(true)
	if err := decoder.Decode(&fm); err != nil {
		return skillFrontmatter{}, "", fmt.Errorf("frontmatter: %w", err)
	}
	if field := fm.unsupportedExecutionField(); field != "" {
		return fm, "", fmt.Errorf("frontmatter field %q is recognized but unsupported by this runtime", field)
	}
	return fm, rest[idx+len("\n---\n"):], nil
}

// Get returns a copy of a skill by its normalized name.
func (c *Catalog) Get(name string) (Skill, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	c.mu.RLock()
	skill, ok := c.skills[name]
	c.mu.RUnlock()
	if ok {
		skill = cloneSkill(skill)
	}
	return skill, ok
}

// List returns all skills in deterministic name order.
func (c *Catalog) List() []Skill {
	c.mu.RLock()
	result := make([]Skill, 0, len(c.skills))
	for _, skill := range c.skills {
		result = append(result, cloneSkill(skill))
	}
	c.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func cloneSkill(skill Skill) Skill {
	skill.Arguments = append([]string(nil), skill.Arguments...)
	return skill
}

// Warnings returns the last reload's non-fatal discovery errors.
func (c *Catalog) Warnings() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.warnings...)
}
