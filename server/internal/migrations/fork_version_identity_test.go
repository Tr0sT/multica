package migrations

import "testing"

// Fork migrations can share an ordinal with a later upstream migration. Their
// complete names, not numeric prefixes, are the persisted migration identity.
func TestForkAndUpstreamMigrationIdentitiesRemainDistinct(t *testing.T) {
	versions, err := AllVersions()
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int, len(versions))
	for _, version := range versions {
		counts[version]++
	}
	for _, pair := range [][2]string{
		{"585_issue_origin_mattermost_chat", "585_issue_workflow_owner"},
		{"586_issue_origin_mattermost_chat_validate", "586_cloud_agent_connection"},
		{"587_channel_reply_delivery_task_idx", "587_cloud_agent_connection_provider_index"},
	} {
		for _, version := range pair {
			if counts[version] != 1 {
				t.Fatalf("migration %q occurs %d times; preserve full fork and upstream identities", version, counts[version])
			}
			for _, direction := range []string{"up", "down"} {
				if got := ExtractVersion(version + "." + direction + ".sql"); got != version {
					t.Fatalf("migration identity collapsed: got %q, want %q", got, version)
				}
			}
		}
	}
}
