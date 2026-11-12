package agentplugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func codexTestPlugin(t *testing.T, content string) TaskPlugin {
	t.Helper()
	bundle, err := ParseBundle("sample", []File{
		{Path: ".codex-plugin/plugin.json", Content: []byte(`{"name":"sample","version":"1","skills":"./skills/","mcpServers":"./mcp.json","hooks":"./hooks.json"}`)},
		{Path: "skills/check/SKILL.md", Content: []byte(content)},
		{Path: "mcp.json", Content: []byte(`{"mcpServers":{}}`)},
		{Path: "hooks.json", Content: []byte(`{"hooks":{}}`)},
		{Path: "bin/check", Content: []byte("#!/bin/sh\nexit 0\n"), Executable: true},
		{Path: "assets/data.bin", Content: []byte{0, 255, 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return TaskPlugin{ID: "version", Marketplace: "studio", Revision: "revision", Digest: Digest(bundle), Bundle: bundle}
}

func TestPrepareCodexPluginsIsolatesSharedHomeAndConcurrentRuns(t *testing.T) {
	shared := t.TempDir()
	if err := os.WriteFile(filepath.Join(shared, "keep"), []byte("user plugin cache"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"issue one", "issue two"} {
		home := t.TempDir()
		config := `model = "existing-model"
[plugins."personal@private"]
enabled = true
[marketplaces.private]
source_type = "git"
source = "ssh://git@example.test/private"
`
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(home, "plugins"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(shared, filepath.Join(home, "plugins", "cache")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		plugin := codexTestPlugin(t, content)
		setup, err := PrepareCodex(home, TaskContext{Plugins: []TaskPlugin{plugin}})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(setup.Plugins, []string{"sample@studio"}) || len(setup.Marketplaces) != 1 {
			t.Fatalf("incorrect install plan: %+v", setup)
		}
		source := setup.Marketplaces[0]
		raw, err := os.ReadFile(filepath.Join(source, ".agents/plugins/marketplace.json"))
		if err != nil {
			t.Fatal(err)
		}
		var catalog struct {
			Plugins []struct {
				Source struct {
					Path string `json:"path"`
				} `json:"source"`
			} `json:"plugins"`
		}
		if err := json.Unmarshal(raw, &catalog); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(source, catalog.Plugins[0].Source.Path)
		got, err := os.ReadFile(filepath.Join(root, "skills/check/SKILL.md"))
		if err != nil || string(got) != content {
			t.Fatalf("wrong task plugin: %q, %v", got, err)
		}
		for _, file := range plugin.Files {
			got, err := os.ReadFile(filepath.Join(root, file.Path))
			if err != nil || !reflect.DeepEqual(got, file.Content) {
				t.Fatalf("lost plugin component %s: %v", file.Path, err)
			}
		}
		parsed := map[string]any{}
		saved, _ := os.ReadFile(filepath.Join(home, "config.toml"))
		if err := toml.Unmarshal(saved, &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed["model"] != "existing-model" {
			t.Fatal("unrelated Codex configuration changed")
		}
		plugins := parsed["plugins"].(map[string]any)
		if plugins["_default"].(map[string]any)["enabled"] != false {
			t.Fatal("account-synchronized plugins can activate without selection")
		}
		if plugins["personal@private"].(map[string]any)["enabled"] != false || plugins["sample@studio"].(map[string]any)["enabled"] != true {
			t.Fatalf("plugin selection not enforced: %+v", plugins)
		}
		if len(parsed["marketplaces"].(map[string]any)) != 1 {
			t.Fatal("inherited Git marketplace retained")
		}
		if info, err := os.Lstat(filepath.Join(home, "plugins/cache")); err != nil || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("plugin manager would write through the shared cache: %v", err)
		}
	}
	entries, _ := os.ReadDir(shared)
	if len(entries) != 1 || entries[0].Name() != "keep" {
		t.Fatal("a task changed the shared plugin cache")
	}
}

func TestPrepareCodexPluginsEmptyAndUnsafeContexts(t *testing.T) {
	home := t.TempDir()
	setup, err := PrepareCodex(home, TaskContext{})
	if err != nil || len(setup.Plugins) != 0 || len(setup.Marketplaces) != 0 {
		t.Fatalf("explicit empty selection failed: %+v, %v", setup, err)
	}
	if setup.Config["plugins"].(map[string]any)["_default"].(map[string]any)["enabled"] != false {
		t.Fatal("empty selection inherits account plugin activation")
	}
	plugin := codexTestPlugin(t, "safe")
	plugin.Files[0].Content = []byte("tampered")
	if _, err := PrepareCodex(t.TempDir(), TaskContext{Plugins: []TaskPlugin{plugin}}); err == nil {
		t.Fatal("accepted tampered package")
	}
	victim := t.TempDir()
	unsafeHome := t.TempDir()
	if err := os.Symlink(victim, filepath.Join(unsafeHome, "plugins")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := PrepareCodex(unsafeHome, TaskContext{}); err == nil {
		t.Fatal("accepted a linked mutable plugin directory")
	}
	entries, _ := os.ReadDir(victim)
	if len(entries) != 0 {
		t.Fatal("plugin preparation wrote outside the task home")
	}
}
