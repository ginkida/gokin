package hooks

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type hooksFile struct {
	Hooks []*Hook `yaml:"hooks"`
}

// LoadFile reads a standalone hooks YAML file (`hooks:` list using the same
// schema as the main config's hooks section). A missing file is fine (nil,
// nil); a parse or validation error in an EXISTING file is returned so a
// typo'd hook never silently does nothing. Hooks default to Enabled unless
// the file explicitly sets `enabled: false` — a standalone hooks file that
// declares a hook obviously wants it active, and the zero-value-Go default
// (false) would make every project hook a silent no-op.
func LoadFile(path string) ([]*Hook, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read hooks file %s: %w", path, err)
	}

	var file struct {
		Hooks []map[string]any `yaml:"hooks"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse hooks file %s: %w", path, err)
	}

	var typed hooksFile
	if err := yaml.Unmarshal(data, &typed); err != nil {
		return nil, fmt.Errorf("parse hooks file %s: %w", path, err)
	}

	for i, h := range typed.Hooks {
		if h == nil {
			return nil, fmt.Errorf("hooks file %s: hook #%d is empty", path, i+1)
		}
		if h.Command == "" {
			return nil, fmt.Errorf("hooks file %s: hook #%d (%q) has no command", path, i+1, h.Name)
		}
		if !knownTypes[h.Type] {
			return nil, fmt.Errorf("hooks file %s: hook #%d (%q) has unknown type %q (known: pre_tool, post_tool, on_error, on_start, on_exit, stop)", path, i+1, h.Name, h.Type)
		}
		// Default-enabled unless explicitly disabled in THIS file.
		if _, explicit := file.Hooks[i]["enabled"]; !explicit {
			h.Enabled = true
		}
	}
	return typed.Hooks, nil
}
