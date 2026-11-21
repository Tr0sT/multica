package main

import (
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestTeamBridgeTokenRejectsWindowsJunction(t *testing.T) {
	// Subtest names enter temporary paths passed to cmd.exe; avoid '=' there.
	for _, mode := range []struct {
		name    string
		godebug string
	}{
		{name: "irregular-mode", godebug: "winsymlink=0"},
		{name: "symlink-mode", godebug: "winsymlink=1"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			var requests atomic.Int64
			teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
			t.Setenv("GODEBUG", mode.godebug)
			dir := teamBridgeTokenTestDirectory(t)
			target := filepath.Join(dir, "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "existing-token"), []byte(tbSecret), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(dir, "junction")
			createJunction(t, target, link)
			for _, args := range [][]string{
				{"teambridge", "token", "export", tbInstallationID, "--output-file", filepath.Join(link, "new-token")},
				{"teambridge", "token", "import", tbInstallationID, "--token-file", filepath.Join(link, "existing-token")},
			} {
				out, err := executeTeamBridge(t, "", args...)
				assertTeamBridgeTokenFailure(t, out, err)
			}
			if requests.Load() != 0 {
				t.Fatal("token operation through a junction reached the API")
			}
			entries, err := os.ReadDir(target)
			if err != nil || len(entries) != 1 || entries[0].Name() != "existing-token" {
				t.Fatal("token operation changed a junction target")
			}
		})
	}
}
