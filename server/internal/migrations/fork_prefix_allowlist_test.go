package migrations

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type migrationPrefixAllowance struct {
	Prefix   int      `json:"prefix"`
	Versions []string `json:"versions"`
	Reason   string   `json:"reason"`
}

func loadForkPrefixAllowlist(t *testing.T) []migrationPrefixAllowance {
	t.Helper()
	file, err := os.Open(filepath.Join(realMigrationsDir(t), "fork-prefix-allowlist.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var allowances []migrationPrefixAllowance
	if err := decoder.Decode(&allowances); err != nil {
		t.Fatalf("decode fork prefix allowlist: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("fork prefix allowlist must contain exactly one JSON array: %v", err)
	}
	return allowances
}

// An allowance names the complete group, never a wildcard or an entire number.
// Checking the reverse direction also rejects stale entries and renamed files.
func checkMigrationNumericPrefixes(files []string, allowances []migrationPrefixAllowance) error {
	// Preserve the upstream rule for historical duplicates through 128.
	const firstUniqueMigrationNumber = 129
	actual := make(map[int][]string)
	for _, file := range files {
		stem, _, ok := splitMigrationFilename(filepath.Base(file))
		if !ok {
			continue
		}
		prefix, _, ok := strings.Cut(stem, "_")
		if !ok {
			continue
		}
		number, err := strconv.Atoi(prefix)
		if err != nil || number < firstUniqueMigrationNumber {
			continue
		}
		actual[number] = append(actual[number], stem)
	}
	allowed := make(map[int][]string)
	for _, entry := range allowances {
		if entry.Prefix < firstUniqueMigrationNumber || len(entry.Versions) < 2 || strings.TrimSpace(entry.Reason) == "" {
			return fmt.Errorf("invalid fork prefix allowance %d: require a prefix >= %d, at least two exact versions and a reason", entry.Prefix, firstUniqueMigrationNumber)
		}
		if _, duplicate := allowed[entry.Prefix]; duplicate {
			return fmt.Errorf("duplicate fork prefix allowance %d", entry.Prefix)
		}
		versions := append([]string(nil), entry.Versions...)
		slices.Sort(versions)
		for i, version := range versions {
			prefix, suffix, ok := strings.Cut(version, "_")
			number, err := strconv.Atoi(prefix)
			if !ok || err != nil || number != entry.Prefix || suffix == "" || strings.ContainsAny(version, "/\\*?[] \t\r\n.") {
				return fmt.Errorf("fork prefix allowance %d has an invalid exact version %q", entry.Prefix, version)
			}
			if i > 0 && version == versions[i-1] {
				return fmt.Errorf("fork prefix allowance %d repeats version %q", entry.Prefix, version)
			}
		}
		allowed[entry.Prefix] = versions
	}
	numbers := make([]int, 0, len(actual)+len(allowed))
	for number := range actual {
		numbers = append(numbers, number)
	}
	for number := range allowed {
		if _, exists := actual[number]; !exists {
			numbers = append(numbers, number)
		}
	}
	slices.Sort(numbers)
	var problems []string
	for _, number := range numbers {
		versions := actual[number]
		slices.Sort(versions)
		expected, listed := allowed[number]
		switch {
		case listed && !slices.Equal(versions, expected):
			problems = append(problems, fmt.Sprintf("prefix %d: exact allowlist expects %v, found %v", number, expected, versions))
		case !listed && len(versions) > 1:
			problems = append(problems, fmt.Sprintf("unapproved migration prefix %d shared by %v", number, versions))
		}
	}
	if len(problems) != 0 {
		return fmt.Errorf("%s; review fork-prefix-allowlist.json (do not rename published migrations)", strings.Join(problems, "; "))
	}
	return nil
}

func TestMigrationPrefixAllowlistPolicy(t *testing.T) {
	pair := migrationPrefixAllowance{585, []string{"585_fork", "585_upstream"}, "Independently published migrations with distinct persisted identities"}
	allowed := []migrationPrefixAllowance{pair}
	for _, tc := range []struct {
		name       string
		versions   []string
		allowances []migrationPrefixAllowance
		wantError  bool
	}{
		{"unique", []string{"585_fork", "586_upstream"}, nil, false},
		{"historical-prefixes", []string{"128_a", "128_b"}, nil, false},
		{"unapproved-pair", pair.Versions, nil, true},
		{"exact-pair", pair.Versions, allowed, false},
		{"order-independent", []string{"585_upstream", "585_fork"}, allowed, false},
		{"third-is-not-approved", []string{"585_fork", "585_upstream", "585_third"}, allowed, true},
		{"third-explicitly-approved", []string{"585_fork", "585_upstream", "585_third"}, []migrationPrefixAllowance{{585, []string{"585_fork", "585_upstream", "585_third"}, "Reviewed third independent migration"}}, false},
		{"different-pair", []string{"585_fork", "585_different"}, allowed, true},
		{"missing-file", []string{"585_fork"}, allowed, true},
		{"stale-entry", nil, allowed, true},
		{"new-collision", []string{"585_fork", "585_upstream", "600_fork", "600_upstream"}, allowed, true},
		{"duplicate-file", []string{"585_fork", "585_upstream", "585_upstream"}, allowed, true},
		{"duplicate-entry", pair.Versions, append([]migrationPrefixAllowance{pair}, pair), true},
		{"duplicate-member", pair.Versions, []migrationPrefixAllowance{{585, []string{"585_fork", "585_fork"}, "Reviewed"}}, true},
		{"wrong-prefix", pair.Versions, []migrationPrefixAllowance{{586, pair.Versions, "Reviewed"}}, true},
		{"wildcard", pair.Versions, []migrationPrefixAllowance{{585, []string{"585_fork", "585_*"}, "Reviewed"}}, true},
		{"reason-required", pair.Versions, []migrationPrefixAllowance{{585, pair.Versions, " "}}, true},
		{"pair-required", pair.Versions, []migrationPrefixAllowance{{585, []string{"585_fork"}, "Reviewed"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := make([]string, len(tc.versions))
			for i, version := range tc.versions {
				files[i] = filepath.Join("migrations", version+".up.sql")
			}
			err := checkMigrationNumericPrefixes(files, tc.allowances)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, want error %v", err, tc.wantError)
			}
		})
	}
}
