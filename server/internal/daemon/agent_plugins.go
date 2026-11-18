package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/agentplugins"
	"github.com/multica-ai/multica/server/internal/daemon/processtree"
	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/redact"
)

type pluginCommandOutput struct {
	buffer bytes.Buffer
}

func (out *pluginCommandOutput) Write(data []byte) (int, error) {
	if remaining := (16 << 10) - out.buffer.Len(); remaining > 0 {
		_, _ = out.buffer.Write(data[:min(len(data), remaining)])
	}
	return len(data), nil
}

func (d *Daemon) prepareManagedAgentPlugins(ctx context.Context, provider, version, executable string, prefix []string, environment map[string]string, home string, plugins *agentplugins.TaskContext) ([]string, error) {
	if plugins == nil {
		return nil, nil
	}
	if provider != "codex" || executable == "" || home == "" {
		return nil, fmt.Errorf("marketplace plugins require an isolated Codex runtime")
	}
	if !agent.SupportsCodexManagedPlugins(version) {
		return nil, fmt.Errorf("selected marketplace plugins require Codex CLI 0.165.0 or newer; update the runtime")
	}
	setup, err := agentplugins.PrepareCodex(home, *plugins)
	if err != nil {
		return nil, err
	}
	overrides, err := agent.CodexPluginConfigArgs(setup.Config)
	if err != nil {
		return nil, err
	}
	prefix = agent.FilterCodexPluginConfigOverrides(prefix, d.logger)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	run := func(args ...string) error {
		command := append(append([]string{}, prefix...), overrides...)
		command = append(command, args...)
		cmd := exec.Command(executable, command...)
		cmd.Dir = home
		cmd.Env = os.Environ()
		for key, value := range environment {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		cmd.Env = append(cmd.Env, "CODEX_HOME="+home)
		output := &pluginCommandOutput{}
		cmd.Stdout, cmd.Stderr = output, output
		if err := processtree.Run(ctx, cmd, 2*time.Second); err != nil {
			detail := redact.Text(strings.TrimSpace(output.buffer.String()))
			if len(detail) > 2000 {
				detail = detail[:2000]
			}
			return fmt.Errorf("Codex plugin setup failed: %w: %s", err, detail)
		}
		return nil
	}
	for _, source := range setup.Marketplaces {
		if err := run("plugin", "marketplace", "add", source); err != nil {
			return nil, err
		}
	}
	for _, plugin := range setup.Plugins {
		if err := run("plugin", "add", plugin, "--json"); err != nil {
			return nil, err
		}
	}
	return overrides, nil
}
