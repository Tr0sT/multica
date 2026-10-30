package agentplugins

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	MaxPlugins      = 32
	MaxFiles        = 2048
	MaxFileBytes    = 8 << 20
	MaxBundleBytes  = 16 << 20
	MaxCatalogBytes = 32 << 20
	MaxReportBytes  = 48 << 20
)

var namePattern = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$")
var refPattern = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,255}$")
var revisionPattern = regexp.MustCompile("^([a-f0-9]{40}|[a-f0-9]{64})$")

type Selection struct {
	MarketplaceID string `json:"marketplace_id"`
	PluginName    string `json:"plugin_name"`
}

type File struct {
	Path       string `json:"path"`
	Content    []byte `json:"content"`
	Executable bool   `json:"executable,omitempty"`
}

type Bundle struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Skills      []string `json:"skills"`
	HasMCP      bool     `json:"has_mcp"`
	HasHooks    bool     `json:"has_hooks"`
	Files       []File   `json:"files"`
}

type Catalog struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Revision    string   `json:"revision"`
	Plugins     []Bundle `json:"plugins"`
}

type TaskPlugin struct {
	ID          string `json:"id"`
	Marketplace string `json:"marketplace"`
	Revision    string `json:"revision"`
	Digest      string `json:"digest"`
	Bundle
}

// Nil preserves runtime inheritance; an empty context explicitly selects no
// marketplace plugins for this conversation.
type TaskContext struct {
	Plugins []TaskPlugin `json:"plugins"`
}

type SyncResult struct {
	Catalog *Catalog `json:"catalog,omitempty"`
	Error   string   `json:"error,omitempty"`
}

func ValidName(value string) bool {
	return namePattern.MatchString(value) && value != "." && value != ".."
}

func ValidateSource(rawURL, ref string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "ssh" && u.Scheme != "https") ||
		u.RawQuery != "" || u.Fragment != "" || u.Path == "" || len(rawURL) > 2048 ||
		strings.HasPrefix(u.Hostname(), "-") || strings.ContainsAny(u.Hostname(), " \t\r\n\x00") ||
		strings.ContainsAny(u.Path, "\r\n\x00") || strings.ContainsAny(rawURL, "\r\n\x00") {
		return errors.New("use an HTTPS or ssh:// Git repository URL without query parameters")
	}
	if u.User != nil {
		if _, password := u.User.Password(); password || u.Scheme != "ssh" {
			return errors.New("credentials must stay on the runtime, not in the repository URL")
		}
	}
	if ref != "" && (!refPattern.MatchString(ref) || strings.Contains(ref, "..") ||
		strings.Contains(ref, "//") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".lock")) {
		return errors.New("invalid Git branch, tag, or revision")
	}
	return nil
}

func SafePath(value string) bool {
	if value == "" || len(value) > 1024 || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "\\:\x00\r\n") || path.IsAbs(value) || path.Clean(value) != value {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

func ReadArchive(data []byte) ([]File, error) {
	reader := tar.NewReader(bytes.NewReader(data))
	files := make([]File, 0)
	total := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read plugin archive: %w", err)
		}
		if header.Typeflag == tar.TypeDir || header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if header.Typeflag != tar.TypeReg || !SafePath(header.Name) ||
			header.Size < 0 || header.Size > MaxFileBytes || len(files) >= MaxFiles {
			return nil, errors.New("plugin contains an unsafe path, link, or oversized file")
		}
		total += int(header.Size)
		if total > MaxBundleBytes {
			return nil, errors.New("plugin exceeds the size limit")
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: header.Name, Content: content, Executable: header.Mode&0111 != 0})
	}
}

func ParseBundle(expectedName string, files []File) (Bundle, error) {
	bundle := Bundle{Name: expectedName, Files: files, Skills: []string{}}
	if !ValidName(expectedName) || len(files) == 0 || len(files) > MaxFiles {
		return bundle, errors.New("invalid plugin name or file count")
	}
	seen := make(map[string]bool, len(files))
	total := 0
	var manifest []byte
	for _, file := range files {
		key := strings.ToLower(file.Path)
		total += len(file.Content)
		if !SafePath(file.Path) || seen[key] || len(file.Content) > MaxFileBytes || total > MaxBundleBytes {
			return bundle, errors.New("invalid, duplicate, or oversized plugin file")
		}
		seen[key] = true
		switch file.Path {
		case ".codex-plugin/plugin.json", "plugin.json":
			if manifest != nil {
				return bundle, errors.New("plugin declares more than one manifest")
			}
			manifest = file.Content
		}
		if strings.HasPrefix(file.Path, "skills/") && path.Base(file.Path) == "SKILL.md" {
			bundle.Skills = append(bundle.Skills, path.Base(path.Dir(file.Path)))
		}
		if file.Path == "mcp.json" || file.Path == ".mcp.json" {
			bundle.HasMCP = true
		}
		if file.Path == "hooks.json" || file.Path == "hooks/hooks.json" {
			bundle.HasHooks = true
		}
	}
	if len(manifest) == 0 || len(manifest) > 256<<10 || !utf8.Valid(manifest) {
		return bundle, errors.New("plugin.json or .codex-plugin/plugin.json is required")
	}
	var metadata struct {
		Name        string          `json:"name"`
		Version     string          `json:"version"`
		Description string          `json:"description"`
		MCPServers  json.RawMessage `json:"mcpServers"`
		Hooks       json.RawMessage `json:"hooks"`
		Interface   struct {
			DisplayName string `json:"displayName"`
		} `json:"interface"`
	}
	if err := json.Unmarshal(manifest, &metadata); err != nil {
		return bundle, fmt.Errorf("parse plugin manifest: %w", err)
	}
	if metadata.Name != expectedName || len(metadata.Version) > 128 ||
		len(metadata.Description) > 8192 || len(metadata.Interface.DisplayName) > 256 ||
		strings.ContainsRune(metadata.Version+metadata.Description+metadata.Interface.DisplayName, '\x00') {
		return bundle, errors.New("plugin manifest identity does not match the catalog")
	}
	bundle.DisplayName = metadata.Interface.DisplayName
	if bundle.DisplayName == "" {
		bundle.DisplayName = expectedName
	}
	bundle.Description, bundle.Version = metadata.Description, metadata.Version
	bundle.HasMCP = bundle.HasMCP || len(metadata.MCPServers) > 0 && string(metadata.MCPServers) != "null"
	bundle.HasHooks = bundle.HasHooks || len(metadata.Hooks) > 0 && string(metadata.Hooks) != "null"
	sort.Slice(bundle.Files, func(i, j int) bool { return bundle.Files[i].Path < bundle.Files[j].Path })
	sort.Strings(bundle.Skills)
	return bundle, nil
}

func ValidateCatalog(catalog *Catalog) error {
	if catalog == nil || !ValidName(catalog.Name) || len(catalog.DisplayName) > 256 ||
		!revisionPattern.MatchString(catalog.Revision) || catalog.Plugins == nil || len(catalog.Plugins) > MaxPlugins {
		return errors.New("invalid marketplace identity, revision, or plugin count")
	}
	seen := make(map[string]bool)
	total := 0
	for i, plugin := range catalog.Plugins {
		if seen[strings.ToLower(plugin.Name)] {
			return errors.New("duplicate plugin name")
		}
		seen[strings.ToLower(plugin.Name)] = true
		validated, err := ParseBundle(plugin.Name, plugin.Files)
		if err != nil {
			return fmt.Errorf("plugin %s: %w", plugin.Name, err)
		}
		catalog.Plugins[i] = validated
		for _, file := range validated.Files {
			total += len(file.Content)
		}
	}
	if total > MaxCatalogBytes {
		return errors.New("marketplace exceeds the size limit")
	}
	if catalog.DisplayName == "" {
		catalog.DisplayName = catalog.Name
	}
	return nil
}

func Digest(bundle Bundle) string {
	data, _ := json.Marshal(bundle)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
