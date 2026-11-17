package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/agentplugins"
)

func TestAgentPluginCommandHelper(t *testing.T) {
	if os.Getenv("MULTICA_TEST_PLUGIN_SETUP") != "1" {
		return
	}
	file, err := os.OpenFile(os.Getenv("MULTICA_TEST_PLUGIN_LOG"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		os.Exit(2)
	}
	cwd, _ := os.Getwd()
	_ = json.NewEncoder(file).Encode(map[string]any{"home": os.Getenv("CODEX_HOME"), "cwd": cwd, "args": os.Args})
	file.Close()
	if os.Getenv("MULTICA_TEST_PLUGIN_FAIL") == "1" {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestManagedAgentPluginsOnlyInvokeTaskLocalPluginManager(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	plugin, err := agentplugins.ParseBundle("sample", []agentplugins.File{
		{Path: ".codex-plugin/plugin.json", Content: []byte(`{"name":"sample","version":"1"}`)},
		{Path: "skills/check/SKILL.md", Content: []byte("Task instructions")},
	})
	if err != nil {
		t.Fatal(err)
	}
	pluginContext := &agentplugins.TaskContext{Plugins: []agentplugins.TaskPlugin{
		{ID: "version", Marketplace: "studio", Bundle: plugin, Digest: agentplugins.Digest(plugin)},
	}}
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			home := t.TempDir()
			log := t.TempDir() + "/commands.jsonl"
			env := map[string]string{"MULTICA_TEST_PLUGIN_SETUP": "1", "MULTICA_TEST_PLUGIN_LOG": log}
			if fail {
				env["MULTICA_TEST_PLUGIN_FAIL"] = "1"
			}
			d := &Daemon{logger: slog.Default()}
			args, err := d.prepareManagedAgentPlugins(
				context.Background(), "codex", "0.165.0", executable, []string{"-test.run=^TestAgentPluginCommandHelper$", "--"},
				env, home, pluginContext)
			if fail {
				if err == nil {
					t.Fatal("plugin manager failure was ignored")
				}
				return
			}
			if err != nil || len(args) != 4 {
				t.Fatalf("prepare plugins: args=%v err=%v", args, err)
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) != 2 {
				t.Fatalf("expected a local catalog add and plugin add, got %d calls", len(lines))
			}
			for _, line := range lines {
				var call struct {
					Home string   `json:"home"`
					Cwd  string   `json:"cwd"`
					Args []string `json:"args"`
				}
				if err := json.Unmarshal([]byte(line), &call); err != nil {
					t.Fatal(err)
				}
				if call.Home != home || call.Cwd != home {
					t.Fatalf("plugin manager escaped its task home: %+v", call)
				}
			}
		})
	}
}
