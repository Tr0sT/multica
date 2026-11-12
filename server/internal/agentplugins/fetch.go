package agentplugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/daemon/processtree"
)

type limitedBuffer struct {
	data  bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.data.Len() {
		return 0, errors.New("Git output exceeds the marketplace size limit")
	}
	return b.data.Write(data)
}

// Fetch reads Git objects without checking out files, running hooks, or using
// the user's Codex/plugin configuration.
func Fetch(ctx context.Context, rawURL, ref string) (*Catalog, error) {
	if err := ValidateSource(rawURL, ref); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	directory, err := os.MkdirTemp("", "multica-marketplace-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	if _, err := git(ctx, directory, 1<<20, "init", "--bare", "."); err != nil {
		return nil, errors.New("could not initialize the marketplace checkout")
	}
	if ref == "" {
		ref = "HEAD"
	}
	if _, err := git(ctx, directory, 1<<20, "fetch", "--depth=1", "--no-tags", "--no-recurse-submodules", "--", rawURL, ref); err != nil {
		return nil, errors.New("Git fetch failed; check the repository URL, ref, and runtime Git credentials")
	}
	return ReadRepository(ctx, directory, "FETCH_HEAD")
}

func git(ctx context.Context, directory string, limit int, args ...string) ([]byte, error) {
	prefix := []string{"-c", "core.hooksPath=" + filepath.Join(directory, ".no-hooks"),
		"-c", "protocol.ext.allow=never", "-c", "protocol.file.allow=never",
		"-c", "protocol.http.allow=never", "-c", "protocol.git.allow=never", "-C", directory}
	cmd := exec.Command("git", append(prefix, args...)...)
	cmd.Dir = directory
	cmd.Env = marketplaceGitEnvironment()
	out := &limitedBuffer{limit: limit}
	cmd.Stdout = out
	if err := processtree.Run(ctx, cmd, 5*time.Second); err != nil {
		return nil, err
	}
	return out.data.Bytes(), nil
}

func marketplaceGitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key == "GIT_DIR" || key == "GIT_WORK_TREE" || key == "GIT_COMMON_DIR" ||
			key == "GIT_INDEX_FILE" || key == "GIT_OBJECT_DIRECTORY" || key == "GIT_ALTERNATE_OBJECT_DIRECTORIES" ||
			strings.HasPrefix(key, "GIT_TRACE") || key == "GIT_TERMINAL_PROMPT" {
			continue
		}
		environment = append(environment, value)
	}
	environment = append(environment, "GIT_TERMINAL_PROMPT=0")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		environment = append(environment, "GIT_SSH_COMMAND=ssh -oBatchMode=yes")
	}
	return environment
}

func ReadRepository(ctx context.Context, directory, ref string) (*Catalog, error) {
	revision, err := git(ctx, directory, 1024, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return nil, errors.New("marketplace revision is unavailable")
	}
	commit := strings.TrimSpace(string(revision))
	if !revisionPattern.MatchString(commit) {
		return nil, errors.New("invalid marketplace revision")
	}
	data, err := git(ctx, directory, 256<<10, "show", commit+":.agents/plugins/marketplace.json")
	if err != nil {
		return nil, errors.New("the repository must contain .agents/plugins/marketplace.json")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("marketplace.json must contain UTF-8 text")
	}
	var manifest struct {
		Name      string `json:"name"`
		Interface struct {
			DisplayName string `json:"displayName"`
		} `json:"interface"`
		Plugins []struct {
			Name   string          `json:"name"`
			Source json.RawMessage `json:"source"`
			Policy struct {
				Installation string `json:"installation"`
			} `json:"policy"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse marketplace.json: %w", err)
	}
	if !ValidName(manifest.Name) || manifest.Plugins == nil || len(manifest.Plugins) > MaxPlugins {
		return nil, errors.New("invalid marketplace name or plugin count")
	}
	result := &Catalog{Name: manifest.Name, DisplayName: manifest.Interface.DisplayName, Revision: commit, Plugins: []Bundle{}}
	total := 0
	for _, entry := range manifest.Plugins {
		if entry.Policy.Installation == "NOT_AVAILABLE" {
			continue
		}
		if entry.Policy.Installation != "" && entry.Policy.Installation != "AVAILABLE" && entry.Policy.Installation != "INSTALLED_BY_DEFAULT" {
			return nil, fmt.Errorf("plugin %s has an unsupported installation policy", entry.Name)
		}
		var localPath string
		if err := json.Unmarshal(entry.Source, &localPath); err != nil {
			var source struct {
				Source string `json:"source"`
				Path   string `json:"path"`
			}
			// shortcut: only repository-local packages, add remote source resolution when a marketplace needs it.
			if err := json.Unmarshal(entry.Source, &source); err != nil || source.Source != "local" {
				return nil, fmt.Errorf("plugin %s must use a local path inside the marketplace repository", entry.Name)
			}
			localPath = source.Path
		}
		sourcePath := strings.TrimSuffix(strings.TrimPrefix(localPath, "./"), "/")
		if !strings.HasPrefix(localPath, "./") || (sourcePath != "" && !SafePath(sourcePath)) {
			return nil, fmt.Errorf("plugin %s has an unsafe source path", entry.Name)
		}
		tree := commit
		if sourcePath != "" {
			tree += ":" + sourcePath
		}
		archive, err := git(ctx, directory, MaxBundleBytes+(2<<20), "archive", "--format=tar", tree)
		if err != nil {
			return nil, fmt.Errorf("cannot read plugin %s at the selected revision", entry.Name)
		}
		files, err := ReadArchive(archive)
		if err != nil {
			return nil, err
		}
		bundle, err := ParseBundle(entry.Name, files)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			total += len(file.Content)
		}
		if total > MaxCatalogBytes {
			return nil, errors.New("marketplace exceeds the size limit")
		}
		result.Plugins = append(result.Plugins, bundle)
	}
	if err := ValidateCatalog(result); err != nil {
		return nil, err
	}
	wire, err := json.Marshal(SyncResult{Catalog: result})
	if err != nil || len(wire) > MaxReportBytes {
		return nil, errors.New("marketplace metadata exceeds the transfer size limit")
	}
	return result, nil
}
