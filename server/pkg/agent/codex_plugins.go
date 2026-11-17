package agent

import (
	"fmt"
	"log/slog"
	"regexp"
)

// 0.165.0 is the supported baseline for native plugin activation defaults.
func SupportsCodexManagedPlugins(version string) bool {
	parsed, err := parseSemver(version)
	return err == nil && !parsed.lessThan(semver{Minor: 165})
}

var codexManagedPluginKeyRe = regexp.MustCompile(
	`^\s*(?:profiles\s*\.\s*` + codexProfileNameKeyPattern + `\s*\.\s*)?(?:"?(?:plugins|marketplaces)"?|'(?:plugins|marketplaces)')\s*(?:\.|=|$)`)

func FilterCodexPluginConfigOverrides(args []string, logger *slog.Logger) []string {
	return filterCodexConfigOverrides(args, codexManagedPluginKeyRe, "plugins", logger)
}

// CodexPluginConfigArgs applies the managed activation settings at CLI
// precedence. Codex merges tables, so the payload includes explicit disables.
func CodexPluginConfigArgs(config map[string]any) ([]string, error) {
	args := make([]string, 0, 4)
	for _, key := range []string{"marketplaces", "plugins"} {
		value, ok := config[key]
		if !ok {
			return nil, fmt.Errorf("missing managed Codex %s configuration", key)
		}
		inline, err := jsonValueToCodexTOMLInline(value)
		if err != nil {
			return nil, err
		}
		args = append(args, "-c", key+"="+inline)
	}
	return args, nil
}
