package agentplugins

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestValidateSource(t *testing.T) {
	for _, source := range []string{"https://example.test/team/plugins.git", "ssh://git@example.test:4422/team/plugins.git"} {
		if err := ValidateSource(source, "feature/plugins"); err != nil {
			t.Fatal(err)
		}
	}
	for _, source := range []string{"file:///etc", "ext::sh -c whoami", "https://user:secret@example.test/repo", "ssh://git:secret@example.test/repo", "https://example.test/repo?token=secret", "/tmp/repo"} {
		if err := ValidateSource(source, "main"); err == nil {
			t.Fatalf("accepted unsafe source %q", source)
		}
	}
	for _, ref := range []string{"--upload-pack=anything", "main..other", "refs//heads/main", "main\nother"} {
		if err := ValidateSource("https://example.test/repo", ref); err == nil {
			t.Fatalf("accepted invalid ref %q", ref)
		}
	}
}

func TestReadArchiveRejectsLinksAndTraversal(t *testing.T) {
	for _, header := range []*tar.Header{
		{Name: "../outside", Typeflag: tar.TypeReg, Size: 1},
		{Name: "C:/outside", Typeflag: tar.TypeReg, Size: 1},
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "hardlink", Typeflag: tar.TypeLink, Linkname: "../outside"},
	} {
		var data bytes.Buffer
		writer := tar.NewWriter(&data)
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			_, _ = writer.Write([]byte("x"))
		}
		_ = writer.Close()
		if _, err := ReadArchive(data.Bytes()); err == nil {
			t.Fatalf("accepted unsafe archive entry %+v", header)
		}
	}
}

func TestParseBundlePreservesFilesAndRejectsAmbiguity(t *testing.T) {
	files := []File{
		{Path: ".codex-plugin/plugin.json", Content: []byte(`{"name":"sample","version":"release-a","description":"Sample","interface":{"displayName":"Sample plugin"},"futureField":true}`)},
		{Path: "skills/check/SKILL.md", Content: []byte("---\nname: check\n---\nCheck it.")},
		{Path: "bin/check", Content: []byte("#!/bin/sh\nexit 0\n"), Executable: true},
		{Path: "assets/icon.bin", Content: []byte{0, 255, 1}},
	}
	bundle, err := ParseBundle("sample", files)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Version != "release-a" || bundle.DisplayName != "Sample plugin" || !reflect.DeepEqual(bundle.Skills, []string{"check"}) {
		t.Fatalf("unexpected metadata: %+v", bundle)
	}
	encoded, _ := json.Marshal(bundle)
	var roundtrip Bundle
	if err := json.Unmarshal(encoded, &roundtrip); err != nil || !reflect.DeepEqual(bundle, roundtrip) {
		t.Fatalf("binary/executable roundtrip changed files: %v", err)
	}
	for _, invalid := range [][]File{
		append(append([]File{}, files...), File{Path: "ASSETS/icon.bin"}),
		append(append([]File{}, files...), File{Path: "../secret"}),
		append(append([]File{}, files...), File{Path: "plugin.json", Content: []byte("{}")}),
	} {
		if _, err := ParseBundle("sample", invalid); err == nil {
			t.Fatal("accepted ambiguous or unsafe plugin")
		}
	}
	if _, err := ParseBundle("another-plugin", files); err == nil {
		t.Fatal("accepted a manifest with a different identity")
	}
}

func TestGitOutputBound(t *testing.T) {
	out := &limitedBuffer{limit: 8}
	if _, err := io.Copy(out, io.LimitReader(strings.NewReader(strings.Repeat("x", 64)), 64)); err == nil {
		t.Fatal("output limit was bypassed by io.Copy")
	}
	if out.data.Len() > 8 {
		t.Fatal("buffer exceeded limit")
	}
}

func TestMarketplaceGitEnvironmentKeepsCredentialsButNotRepositoryOverrides(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.https://example.test/.extraheader")
	t.Setenv("GIT_CONFIG_VALUE_0", "fixture-auth")
	t.Setenv("GIT_DIR", "/unrelated/repository")
	t.Setenv("GIT_OBJECT_DIRECTORY", "/unrelated/objects")
	t.Setenv("GIT_TRACE", "/unrelated/log")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	values := map[string]string{}
	for _, entry := range marketplaceGitEnvironment() {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	if values["GIT_CONFIG_VALUE_0"] != "fixture-auth" || values["GIT_CONFIG_COUNT"] != "1" {
		t.Fatal("runtime Git credential configuration was discarded")
	}
	if values["GIT_DIR"] != "" || values["GIT_OBJECT_DIRECTORY"] != "" || values["GIT_TRACE"] != "" ||
		values["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatal("checkout location, trace, or prompting settings leaked into the private fetch")
	}
}

func TestReadRepositoryUsesCommittedMarketplaceSnapshot(t *testing.T) {
	for _, sourcePath := range []string{"./plugins/sample/", "./"} {
		t.Run(sourcePath, func(t *testing.T) {
			root := t.TempDir()
			write := func(name, value string) {
				t.Helper()
				full := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(value), 0644); err != nil {
					t.Fatal(err)
				}
			}
			manifest, _ := json.Marshal(map[string]any{"name": "studio", "plugins": []map[string]any{{"name": "sample", "source": map[string]string{"source": "local", "path": sourcePath}}}})
			write(".agents/plugins/marketplace.json", string(manifest))
			write(strings.TrimPrefix(sourcePath, "./")+".codex-plugin/plugin.json", `{"name":"sample","version":"1.0"}`)
			write(strings.TrimPrefix(sourcePath, "./")+"skills/check/SKILL.md", "Committed instructions")
			for _, args := range [][]string{{"init", "--object-format=sha1", "."}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.test", "-c", "commit.gpgsign=false", "commit", "-m", "Fixture"}} {
				cmd := exec.Command("git", append([]string{"-C", root, "-c", "core.hooksPath=" + t.TempDir()}, args...)...)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fixture git: %v: %s", err, out)
				}
			}
			write(strings.TrimPrefix(sourcePath, "./")+"skills/check/SKILL.md", "Uncommitted instructions must not leak")
			catalog, err := ReadRepository(context.Background(), root, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			if catalog.Name != "studio" || len(catalog.Plugins) != 1 || len(catalog.Revision) != 40 {
				t.Fatalf("wrong catalog: %+v", catalog)
			}
			for _, file := range catalog.Plugins[0].Files {
				if file.Path == "skills/check/SKILL.md" && string(file.Content) != "Committed instructions" {
					t.Fatal("import read the dirty working tree instead of the Git revision")
				}
			}
			write(".agents/plugins/marketplace.json", `{"name":"studio","plugins":[{"name":"sample","source":{"source":"local","path":"./missing"},"policy":{"installation":"NOT_AVAILABLE"}}]}`)
			cmd := exec.Command("git", "-C", root, "-c", "core.hooksPath="+t.TempDir(), "-c", "user.name=Test", "-c", "user.email=test@example.test", "-c", "commit.gpgsign=false", "commit", "-am", "Withdraw plugin")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("withdraw fixture: %v: %s", err, out)
			}
			empty, err := ReadRepository(context.Background(), root, "HEAD")
			if err != nil || empty == nil || len(empty.Plugins) != 0 {
				t.Fatalf("withdrawn plugins must produce a valid empty catalog: %+v %v", empty, err)
			}
		})
	}
}
