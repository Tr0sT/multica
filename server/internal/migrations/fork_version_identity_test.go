package migrations

import (
	"os"
	"path/filepath"
	"testing"
)

// Complete names, not numeric prefixes, are the persisted migration identity.
func TestForkAndUpstreamMigrationIdentitiesRemainDistinct(t *testing.T) {
	dir := realMigrationsDir(t)
	// Package-local execution otherwise resolves this package directory as
	// "migrations" instead of the SQL directory next to cmd and internal.
	t.Chdir(filepath.Dir(dir))
	versions, err := AllVersions()
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int, len(versions))
	for _, version := range versions {
		counts[version]++
	}
	for _, allowance := range loadForkPrefixAllowlist(t) {
		for _, version := range allowance.Versions {
			if counts[version] != 1 {
				t.Fatalf("migration %q occurs %d times; preserve full fork and upstream identities", version, counts[version])
			}
			for _, direction := range []string{"up", "down"} {
				filename := filepath.Join(dir, version+"."+direction+".sql")
				if info, err := os.Stat(filename); err != nil || !info.Mode().IsRegular() {
					t.Fatalf("allowlisted migration direction missing or not regular: %s (%v)", filename, err)
				}
				if got := ExtractVersion(filename); got != version {
					t.Fatalf("migration identity collapsed: got %q, want %q", got, version)
				}
			}
		}
	}
}
