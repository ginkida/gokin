package tools

// allowedDirsSetter is implemented by every path-scoping tool — the ones that
// build a PathValidator from workDir plus a set of additional allowed dirs
// (read, edit, write, list_dir, glob, grep, tree, copy, move, delete, mkdir,
// batch, refactor, go_to_definition, find_references).
type allowedDirsSetter interface {
	SetAllowedDirs(dirs []string)
}

// allowedDirsRegistry is the minimal registry surface the propagation helper
// needs. The eager *Registry satisfies it.
type allowedDirsRegistry interface {
	List() []Tool
}

// SetAllowedDirsOnRegistry pushes the additional-allowed-directories list to
// EVERY path-scoping tool in reg. It is the SINGLE source of truth for
// allowed-dir propagation — the builder uses it at boot (replacing a 60-line
// per-tool copy), App uses it for runtime /add-dir grants, and the sub-agent
// clone path uses it so spawned agents inherit the user's grants.
//
// Because it discovers tools by the allowedDirsSetter interface (not a static
// name list), a new path-scoping tool is covered automatically. Passing an
// empty/short list resets every tool to its workDir-only default — that is how
// a session grant is revoked (/remove-dir, /clear). Returns the number of tools
// updated (for logging and the drift-guard test).
func SetAllowedDirsOnRegistry(reg allowedDirsRegistry, dirs []string) int {
	if reg == nil {
		return 0
	}
	count := 0
	for _, t := range reg.List() {
		if s, ok := t.(allowedDirsSetter); ok {
			s.SetAllowedDirs(dirs)
			count++
		}
	}
	return count
}
