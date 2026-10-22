package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gokin/internal/fileutil"
	"gokin/internal/hooks"
	"gokin/internal/pluginpolicy"
)

func (m *Manager) publishLinkedTree(root, id string) (string, string, string, error) {
	real, entries, err := pluginpolicy.CheckLinkedRoot(root, m.opts.WorkDir)
	if err != nil {
		return "", "", "", err
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "link:%d:%s:", len(real), real)
	for _, entry := range entries {
		link := ""
		if entry.Type()&os.ModeSymlink != 0 {
			link, err = os.Readlink(filepath.Join(real, entry.Name()))
			if err != nil {
				return "", "", "", err
			}
		}
		fmt.Fprintf(hash, "%d:%s:%d:%d:%s:", len(entry.Name()), entry.Name(), entry.Type(), len(link), link)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	parent := filepath.Join(m.cacheDir, "plugins", hooks.ClaudePluginStorageKey(id))
	if err := fileutil.EnsurePrivateDir(parent); err != nil {
		return "", "", "", err
	}
	target := filepath.Join(parent, digest)
	if _, err := os.Lstat(target); err == nil {
		if _, err := pluginpolicy.ResolveLinkedRoot(target, real, m.opts.WorkDir); err != nil {
			return "", "", "", err
		}
		cached, err := os.ReadDir(target)
		if err != nil {
			return "", "", "", err
		}
		if len(cached) != len(entries) {
			return "", "", "", fmt.Errorf("linked plugin cache integrity mismatch")
		}
		for i := range cached {
			if cached[i].Name() != entries[i].Name() {
				return "", "", "", fmt.Errorf("linked plugin cache integrity mismatch")
			}
		}
		return target, real, digest, nil
	} else if !os.IsNotExist(err) {
		return "", "", "", err
	}
	stage, err := os.MkdirTemp(parent, ".staging-")
	if err != nil {
		return "", "", "", err
	}
	defer os.RemoveAll(stage)
	for _, entry := range entries {
		if err := os.Symlink(filepath.Join(real, entry.Name()), filepath.Join(stage, entry.Name())); err != nil {
			return "", "", "", err
		}
	}
	if _, err := pluginpolicy.ResolveLinkedRoot(stage, real, m.opts.WorkDir); err != nil {
		return "", "", "", err
	}
	if err := os.Rename(stage, target); err != nil {
		return "", "", "", err
	}
	return target, real, digest, nil
}

func (m *Manager) installedManifest(record InstallRecord) (map[string]json.RawMessage, error) {
	root := record.InstallPath
	if record.Source.Kind == "command" && record.Source.Mode == "link" {
		var err error
		root, err = pluginpolicy.ResolveLinkedRoot(root, record.LinkedRoot, "")
		if err != nil {
			return nil, err
		}
	}
	var manifest map[string]json.RawMessage
	var err error
	if record.Source.Kind == "command" && record.Source.Mode == "link" {
		manifest, err = pluginpolicy.ReadLinkedManifest(root)
	} else {
		manifest, err = readObject(filepath.Join(root, ".claude-plugin", "plugin.json"))
	}
	if err != nil {
		return nil, err
	}
	if record.Source.Kind == "command" && record.Source.Mode == "link" {
		var entry map[string]json.RawMessage
		if json.Unmarshal(record.LinkEntry, &entry) != nil || entry == nil {
			return nil, fmt.Errorf("linked plugin marketplace entry is unavailable")
		}
		return pluginpolicy.MergePluginManifest(manifest, entry)
	}
	return manifest, nil
}

// Only the effective manifest is materialized for schema/option validation;
// component files continue to be read from the producer's directory.
func (m *Manager) installedOptionRoot(record InstallRecord) (string, func(), error) {
	if record.Source.Kind != "command" || record.Source.Mode != "link" {
		return record.InstallPath, func() {}, nil
	}
	manifest, err := m.installedManifest(record)
	if err != nil {
		return "", nil, err
	}
	stage, err := os.MkdirTemp(filepath.Join(m.pluginDir, "gokin"), "link-manifest-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(stage) }
	path := filepath.Join(stage, ".claude-plugin", "plugin.json")
	if err := fileutil.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		cleanup()
		return "", nil, err
	}
	data, err := json.Marshal(manifest)
	if err == nil {
		err = fileutil.AtomicWrite(path, data, 0600)
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return stage, cleanup, nil
}

func commandMode(source Source) string {
	if source.Mode == "" {
		return "copy"
	}
	return source.Mode
}

func sameCommandSource(a, b Source) bool {
	return a.Kind == "command" && b.Kind == "command" && a.Command == b.Command && commandMode(a) == commandMode(b)
}
