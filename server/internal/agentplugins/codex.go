package agentplugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type CodexSetup struct {
	Marketplaces []string
	Plugins      []string
	Config       map[string]any
}

// PrepareCodex only writes inside the already-created task home. It never
// installs into the cache symlink used by runtime-inherited plugins.
func PrepareCodex(home string, context TaskContext) (CodexSetup, error) {
	setup := CodexSetup{Config: map[string]any{"plugins": map[string]any{}, "marketplaces": map[string]any{}}}
	if !filepath.IsAbs(home) || len(context.Plugins) > MaxPlugins {
		return setup, errors.New("invalid task plugin home or plugin count")
	}
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return setup, errors.New("plugin setup requires an isolated task Codex home")
	}
	if _, err := privateDirectory(home, "plugins"); err != nil {
		return setup, err
	}
	cache := filepath.Join(home, "plugins", "cache")
	if info, err := os.Lstat(cache); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(cache); err != nil {
			return setup, err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return setup, err
	}
	if _, err := privateDirectory(home, "plugins/cache"); err != nil {
		return setup, err
	}
	if _, err := privateDirectory(home, "plugins/data"); err != nil {
		return setup, err
	}
	configPath := filepath.Join(home, "config.toml")
	config := map[string]any{}
	if raw, err := os.ReadFile(configPath); err == nil {
		if err := toml.Unmarshal(raw, &config); err != nil {
			return setup, fmt.Errorf("read task Codex config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return setup, err
	}
	plugins := setup.Config["plugins"].(map[string]any)
	// Explicitly disable inherited choices, including installations whose
	// absence from the config would otherwise restore a provider default.
	if previous, ok := config["plugins"].(map[string]any); ok {
		for name := range previous {
			plugins[name] = map[string]any{"enabled": false}
		}
	}
	// Account-synchronized installations may not have entries in config.toml.
	plugins["_default"] = map[string]any{"enabled": false}
	marketplaces := setup.Config["marketplaces"].(map[string]any)
	entries := map[string][]map[string]any{}
	seen := map[string]bool{}
	total := 0
	for _, plugin := range context.Plugins {
		if !ValidName(plugin.Marketplace) || plugin.ID == "" {
			return setup, errors.New("invalid task marketplace identity")
		}
		bundle, err := ParseBundle(plugin.Name, plugin.Files)
		if err != nil || Digest(bundle) != plugin.Digest {
			return setup, fmt.Errorf("plugin %s failed integrity validation", plugin.Name)
		}
		selector := plugin.Name + "@" + plugin.Marketplace
		if seen[selector] {
			return setup, errors.New("duplicate task plugin")
		}
		seen[selector] = true
		relativeRoot := ".multica-plugin-sources/" + plugin.Marketplace
		root, err := privateDirectory(home, relativeRoot)
		if err != nil {
			return setup, err
		}
		for _, file := range bundle.Files {
			total += len(file.Content)
			if total > MaxCatalogBytes {
				return setup, errors.New("task plugins exceed the size limit")
			}
			relative := relativeRoot + "/packages/" + plugin.Digest + "/" + file.Path
			mode := os.FileMode(0644)
			if file.Executable {
				mode = 0755
			}
			if err := writePrivateFile(home, relative, file.Content, mode); err != nil {
				return setup, err
			}
		}
		marketplaces[plugin.Marketplace] = map[string]any{"source_type": "local", "source": root}
		plugins[selector] = map[string]any{"enabled": true}
		entries[plugin.Marketplace] = append(entries[plugin.Marketplace], map[string]any{
			"name":     plugin.Name,
			"source":   map[string]string{"source": "local", "path": "./packages/" + plugin.Digest},
			"policy":   map[string]string{"installation": "AVAILABLE", "authentication": "ON_USE"},
			"category": "Developer Tools",
		})
		setup.Plugins = append(setup.Plugins, selector)
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	sort.Strings(setup.Plugins)
	for _, name := range names {
		data, err := json.Marshal(map[string]any{"name": name, "plugins": entries[name]})
		if err != nil {
			return setup, err
		}
		relative := ".multica-plugin-sources/" + name
		if err := writePrivateFile(home, relative+"/.agents/plugins/marketplace.json", data, 0644); err != nil {
			return setup, err
		}
		setup.Marketplaces = append(setup.Marketplaces, filepath.Join(home, filepath.FromSlash(relative)))
	}
	config["plugins"], config["marketplaces"] = plugins, marketplaces
	data, err := toml.Marshal(config)
	if err != nil {
		return setup, err
	}
	if err := writePrivateFile(home, "config.toml", data, 0600); err != nil {
		return setup, err
	}
	return setup, nil
}

func privateDirectory(root, relative string) (string, error) {
	current := root
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", errors.New("invalid plugin directory")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0700); err != nil {
				return "", err
			}
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("task plugin directory contains a link or non-directory")
		}
	}
	return current, nil
}

func writePrivateFile(root, relative string, data []byte, mode os.FileMode) error {
	if !SafePath(relative) {
		return errors.New("invalid task plugin file path")
	}
	parent, err := privateDirectory(root, filepath.ToSlash(filepath.Dir(relative)))
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(parent, ".plugin-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), filepath.Join(parent, filepath.Base(relative)))
}
