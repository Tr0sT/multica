package agent

import (
	"log/slog"
	"strings"
	"testing"
)

func TestCodexManagedPluginsRequireActivationDefaults(t *testing.T) {
	for version, want := range map[string]bool{
		"": false, "unknown": false, "0.164.0": false,
		"codex-cli 0.165.0": true, "0.166.0": true, "1.0.0": true,
	} {
		if SupportsCodexManagedPlugins(version) != want {
			t.Errorf("managed plugin support for %q: want %v", version, want)
		}
	}
}

func TestCodexManagedPluginsOverrideOnlyPluginNamespaces(t *testing.T) {
	managed, err := CodexPluginConfigArgs(map[string]any{
		"plugins":      map[string]any{"sample@studio": map[string]any{"enabled": true}},
		"marketplaces": map[string]any{"studio": map[string]any{"source_type": "local", "source": "/task/plugins"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	args := buildCodexArgs(ExecOptions{
		ExtraArgs:       []string{"-c", `plugins.other.enabled=true`, "-c", `model="preserve"`},
		CustomArgs:      []string{"--config", `marketplaces.other.source="/wrong"`},
		CodexPluginArgs: managed,
	}, slog.Default())
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "other") || !strings.Contains(joined, `model="preserve"`) ||
		!strings.Contains(joined, "sample@studio") || !strings.Contains(joined, "/task/plugins") {
		t.Fatalf("wrong effective launch args: %v", args)
	}
	prefix := FilterCodexPluginConfigOverrides([]string{"--config", `profiles."work".plugins.other.enabled=true`, "--config", "sandbox_mode=read-only"}, slog.Default())
	if strings.Contains(strings.Join(prefix, " "), "plugins") || len(prefix) != 2 {
		t.Fatalf("profile plugin override survived: %v", prefix)
	}
	legacy := buildCodexArgs(ExecOptions{CustomArgs: []string{"-c", "plugins.other.enabled=true"}}, slog.Default())
	if !strings.Contains(strings.Join(legacy, " "), "plugins.other.enabled=true") {
		t.Fatal("changed plugin behavior for an agent without a managed selection")
	}
}
