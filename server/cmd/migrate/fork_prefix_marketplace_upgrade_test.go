package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Keep fork marketplace migrations 595-599 and upstream sidebar/unread
// migrations 595-599 independent even when one side was installed first.
// The real SQL is run in isolated PostgreSQL schemas using the production
// migration runner, with full persisted identities and index rollback.
func TestForkMarketplaceAndUnreadPrefixCollisionsUpgrade(t *testing.T) {
	fork := []string{
		"595_agent_marketplaces",
		"596_agent_marketplace_id_index",
		"597_agent_marketplace_source_index",
		"598_agent_marketplace_name_index",
		"599_agent_plugin_version_id_index",
	}
	upstream := []string{
		"595_issue_view_preference_sidebar",
		"596_issue_view_preference_sidebar_projects",
		"597_issue_unread",
		"598_issue_unread_unique_index",
		"599_issue_unread_primary_key",
	}
	combined := append(append([]string(nil), fork...), upstream...)
	sort.Strings(combined)

	for _, source := range []string{"fresh", "fork", "upstream"} {
		t.Run(source, func(t *testing.T) {
			admin := openTestPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			schema := fmt.Sprintf("fork_marketplace_%d_%d", time.Now().UnixNano(), rand.Uint32())
			quotedSchema := pgx.Identifier{schema}.Sanitize()
			if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
				defer stop()
				if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
					t.Errorf("cleanup isolated schema: %v", err)
				}
			})
			pool := openTestPoolWithSearchPath(t, schema)
			if _, err := pool.Exec(ctx, `
				CREATE TABLE agent (id UUID PRIMARY KEY);
				CREATE TABLE issue_view_preference (
					scope_type TEXT NOT NULL,
					CONSTRAINT issue_view_preference_scope_type_check
						CHECK (scope_type IN ('workspace', 'my', 'project'))
				);
				CREATE TABLE schema_migrations (
					version TEXT PRIMARY KEY,
					applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
				);
				INSERT INTO agent VALUES ('00000000-0000-4000-8000-000000000011');
				INSERT INTO issue_view_preference VALUES ('workspace');
			`); err != nil {
				t.Fatalf("create pre-collision fixture: %v", err)
			}
			options := runOptions{
				Direction: "up",
				SchemaMigrationsTable: schema + ".schema_migrations",
				AdvisoryLockKey: int64(rand.Uint64()&0x7fffffffffffffff) | 1,
				Hooks: hooksForDirection("up"),
			}
			apply := func(versions []string, direction string) {
				t.Helper()
				options.Direction = direction
				options.Hooks = hooksForDirection(direction)
				options.Files = realMigrationFiles(t, versions, direction)
				sort.Strings(options.Files)
				if direction == "down" {
					sort.Sort(sort.Reverse(sort.StringSlice(options.Files)))
				}
				if err := runMigrations(ctx, pool, options); err != nil {
					t.Fatalf("%s %s migrations: %v", source, direction, err)
				}
			}
			history := func() map[string]time.Time {
				t.Helper()
				rows, err := pool.Query(ctx, "SELECT version, applied_at FROM schema_migrations")
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				result := make(map[string]time.Time)
				for rows.Next() {
					var name string
					var applied time.Time
					if err := rows.Scan(&name, &applied); err != nil {
						t.Fatal(err)
					}
					result[name] = applied
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				return result
			}
			oid := func(index string) uint32 {
				t.Helper()
				var id uint32
				if err := pool.QueryRow(ctx, "SELECT $1::regclass::oid", schema+"."+index).Scan(&id); err != nil {
					t.Fatal(err)
				}
				return id
			}
			indexes := []string{
				"agent_marketplace_id_idx", "agent_marketplace_source_idx",
				"agent_marketplace_name_idx", "agent_plugin_version_id_idx",
				"issue_unread_pkey",
			}
			previousIndexOIDs := make(map[string]uint32)
			switch source {
			case "fork":
				apply(fork, "up")
				for _, index := range indexes[:4] {
					previousIndexOIDs[index] = oid(index)
				}
				if _, err := pool.Exec(ctx, `
					UPDATE agent SET plugin_selection = '{"source":"existing-fork"}'::jsonb;
					INSERT INTO agent_marketplace (
						id, workspace_id, source_url, fetch_runtime_id, created_by, name
					) VALUES (
						'00000000-0000-4000-8000-000000000012',
						'00000000-0000-4000-8000-000000000013',
						'https://synthetic.invalid/fork',
						'00000000-0000-4000-8000-000000000014',
						'00000000-0000-4000-8000-000000000015', 'fork-marketplace'
					);
					INSERT INTO agent_plugin_version (
						id, workspace_id, marketplace_id, plugin_name, revision, digest, metadata, files
					) VALUES (
						'00000000-0000-4000-8000-000000000016',
						'00000000-0000-4000-8000-000000000013',
						'00000000-0000-4000-8000-000000000012',
						'fork-plugin', 'r1', 'synthetic-digest', '{}'::jsonb, '{}'::jsonb
					);
				`); err != nil {
					t.Fatal(err)
				}
			case "upstream":
				apply(upstream, "up")
				previousIndexOIDs["issue_unread_pkey"] = oid("issue_unread_pkey")
				if _, err := pool.Exec(ctx, `
					INSERT INTO issue_view_preference VALUES ('sidebar_projects');
					INSERT INTO issue_unread (workspace_id, user_id, issue_id, outcome)
					VALUES (
						'00000000-0000-4000-8000-000000000013',
						'00000000-0000-4000-8000-000000000017',
						'00000000-0000-4000-8000-000000000018', 'manual'
					);
				`); err != nil {
					t.Fatal(err)
				}
			}
			before := history()
			apply(combined, "up")
			after := history()
			if len(after) != len(combined) {
				t.Fatalf("recorded %d full migration identities, want %d", len(after), len(combined))
			}
			for _, version := range combined {
				if _, ok := after[version]; !ok {
					t.Fatalf("missing migration identity %s", version)
				}
			}
			for version, applied := range before {
				if !after[version].Equal(applied) {
					t.Fatalf("rewrote previously applied migration %s", version)
				}
			}
			apply(combined, "up")
			for version, applied := range history() {
				if !after[version].Equal(applied) {
					t.Fatalf("repeated application changed migration history for %s", version)
				}
			}
			for _, index := range indexes {
				assertIndexValidity(t, pool, schema, index, true)
				if previous, ok := previousIndexOIDs[index]; ok && oid(index) != previous {
					t.Fatalf("upgrade rebuilt previously applied index %s", index)
				}
			}
			var originalScope string
			if err := pool.QueryRow(ctx, "SELECT scope_type FROM issue_view_preference WHERE scope_type = 'workspace'").Scan(&originalScope); err != nil || originalScope != "workspace" {
				t.Fatalf("lost existing preference: %q, %v", originalScope, err)
			}
			if source == "fork" {
				var origin, plugin string
				if err := pool.QueryRow(ctx, "SELECT plugin_selection->>'source' FROM agent").Scan(&origin); err != nil || origin != "existing-fork" {
					t.Fatalf("lost fork plugin selection: %q, %v", origin, err)
				}
				if err := pool.QueryRow(ctx, "SELECT plugin_name FROM agent_plugin_version WHERE revision = 'r1'").Scan(&plugin); err != nil || plugin != "fork-plugin" {
					t.Fatalf("lost fork plugin: %q, %v", plugin, err)
				}
			}
			if source == "upstream" {
				var scope, outcome string
				if err := pool.QueryRow(ctx, "SELECT scope_type FROM issue_view_preference WHERE scope_type = 'sidebar_projects'").Scan(&scope); err != nil || scope != "sidebar_projects" {
					t.Fatalf("lost upstream sidebar preference: %q, %v", scope, err)
				}
				if err := pool.QueryRow(ctx, "SELECT outcome FROM issue_unread").Scan(&outcome); err != nil || outcome != "manual" {
					t.Fatalf("lost upstream unread record: %q, %v", outcome, err)
				}
			}
			if source == "fresh" {
				apply(combined, "down")
				if len(history()) != 0 {
					t.Fatal("rollback left collision identities recorded")
				}
				apply(combined, "down")
				apply(combined, "up")
				if len(history()) != len(combined) {
					t.Fatal("reapply did not restore full migration history")
				}
				for _, index := range indexes {
					assertIndexValidity(t, pool, schema, index, true)
				}
			}
		})
	}
}
