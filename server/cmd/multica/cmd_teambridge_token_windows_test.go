package main

import (
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestTeamBridgeTokenRejectsWindowsJunction(t *testing.T) {
	for _, mode := range []string{"winsymlink=0", "winsymlink=1"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int64
			teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
			t.Setenv("GODEBUG", mode)
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
