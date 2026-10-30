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

// Exercise the real colliding SQL and runner, not just a fabricated history.
// Each path owns a schema and preserves already-applied full version names.
func TestForkPrefixCollisionsUpgrade(t *testing.T) {
	fork := []string{
		"585_issue_origin_mattermost_chat",
		"586_issue_origin_mattermost_chat_validate",
		"587_channel_reply_delivery_task_idx",
	}
	upstream := []string{
		"585_issue_workflow_owner",
		"586_cloud_agent_connection",
		"587_cloud_agent_connection_provider_index",
	}
	combined := append(append([]string(nil), fork...), upstream...)
	sort.Strings(combined)

	for _, source := range []string{"fresh", "fork", "upstream"} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			admin := openTestPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			schema := fmt.Sprintf("fork_prefix_%d_%d", time.Now().UnixNano(), rand.Uint32())
			ident := pgx.Identifier{schema}.Sanitize()
			if _, err := admin.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+ident+" CASCADE"); err != nil {
					t.Errorf("cleanup schema: %v", err)
				}
			})
			pool := openTestPoolWithSearchPath(t, schema)
			if _, err := pool.Exec(ctx, `
				CREATE TABLE issue (id TEXT PRIMARY KEY, origin_type TEXT NOT NULL,
					CONSTRAINT issue_origin_type_check CHECK (origin_type IN ('autopilot', 'quick_create', 'lark_chat', 'slack_chat', 'agent_create', 'dingtalk_chat', 'wecom_chat', 'telegram_chat')));
				CREATE TABLE member (id UUID, workspace_id UUID, user_id UUID, role TEXT, created_at TIMESTAMPTZ);
				CREATE TABLE issue_workflow (id UUID, workspace_id UUID, is_default BOOLEAN);
				CREATE TABLE channel_reply_delivery (task_id UUID, channel_type TEXT, chat_id TEXT);
				CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());
				INSERT INTO issue VALUES ('historical', 'autopilot');
				INSERT INTO member VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000002', '00000000-0000-4000-8000-000000000003', 'owner', now());
				INSERT INTO issue_workflow VALUES ('00000000-0000-4000-8000-000000000004', '00000000-0000-4000-8000-000000000002', false);
				INSERT INTO channel_reply_delivery VALUES ('00000000-0000-4000-8000-000000000005', 'mattermost', 'existing-room');
			`); err != nil {
				t.Fatal(err)
			}
			options := runOptions{
				Direction: "up", SchemaMigrationsTable: schema + ".schema_migrations",
				AdvisoryLockKey: int64(rand.Uint64()&0x7fffffffffffffff) | 1,
				Hooks:           hooksForDirection("up"),
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
					t.Fatalf("%s migrations from %s: %v", direction, source, err)
				}
			}
			readHistory := func() map[string]time.Time {
				t.Helper()
				rows, err := pool.Query(ctx, "SELECT version, applied_at FROM schema_migrations")
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				history := make(map[string]time.Time)
				for rows.Next() {
					var version string
					var applied time.Time
					if err := rows.Scan(&version, &applied); err != nil {
						t.Fatal(err)
					}
					history[version] = applied
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				return history
			}
			indexOID := func() uint32 {
				t.Helper()
				var oid uint32
				if err := pool.QueryRow(ctx, "SELECT $1::regclass::oid", schema+".idx_channel_reply_delivery_task_id").Scan(&oid); err != nil {
					t.Fatal(err)
				}
				return oid
			}
			var oldIndex uint32
			switch source {
			case "fork":
				apply(fork, "up")
				oldIndex = indexOID()
				if _, err := pool.Exec(ctx, "INSERT INTO issue VALUES ('mattermost', 'mattermost_chat')"); err != nil {
					t.Fatal(err)
				}
			case "upstream":
				apply(upstream, "up")
				if _, err := pool.Exec(ctx, `INSERT INTO cloud_agent_connection (workspace_id, provider, owner_id, token_id, config_encrypted)
					VALUES ('00000000-0000-4000-8000-000000000002', 'synthetic-provider', '00000000-0000-4000-8000-000000000003', '00000000-0000-4000-8000-000000000006', 'synthetic-preserved-value')`); err != nil {
					t.Fatal(err)
				}
			}
			before := readHistory()
			apply(combined, "up")
			after := readHistory()
			if len(after) != len(combined) {
				t.Fatalf("recorded %d versions, want %d full identities", len(after), len(combined))
			}
			for _, version := range combined {
				if _, exists := after[version]; !exists {
					t.Fatalf("missing full identity %s", version)
				}
			}
			for version, applied := range before {
				if !after[version].Equal(applied) {
					t.Fatalf("rewrote previously applied history for %s", version)
				}
			}
			apply(combined, "up")
			for version, applied := range readHistory() {
				if !after[version].Equal(applied) {
					t.Fatalf("repeated apply changed history for %s", version)
				}
			}
			if oldIndex != 0 && indexOID() != oldIndex {
				t.Fatal("upgrade rebuilt the fork's already applied index")
			}
			assertIndexValidity(t, pool, schema, "idx_channel_reply_delivery_task_id", true)
			assertIndexValidity(t, pool, schema, "cloud_agent_connection_provider_index", true)
			var owner string
			if err := pool.QueryRow(ctx, "SELECT owner_id::text FROM issue_workflow").Scan(&owner); err != nil || owner != "00000000-0000-4000-8000-000000000003" {
				t.Fatalf("workflow owner backfill: %q, %v", owner, err)
			}
			var origin, room string
			if err := pool.QueryRow(ctx, "SELECT origin_type FROM issue WHERE id = 'historical'").Scan(&origin); err != nil || origin != "autopilot" {
				t.Fatalf("historical issue changed: %q, %v", origin, err)
			}
			if err := pool.QueryRow(ctx, "SELECT chat_id FROM channel_reply_delivery WHERE channel_type = 'mattermost'").Scan(&room); err != nil || room != "existing-room" {
				t.Fatalf("Mattermost delivery changed: %q, %v", room, err)
			}
			var validated bool
			if err := pool.QueryRow(ctx, "SELECT convalidated FROM pg_constraint WHERE connamespace = $1::regnamespace AND conname = 'issue_origin_type_check'", schema).Scan(&validated); err != nil || !validated {
				t.Fatalf("Mattermost constraint not validated: %v, %v", validated, err)
			}
			if source == "fork" {
				if err := pool.QueryRow(ctx, "SELECT origin_type FROM issue WHERE id = 'mattermost'").Scan(&origin); err != nil || origin != "mattermost_chat" {
					t.Fatalf("existing Mattermost origin changed: %q, %v", origin, err)
				}
			}
			if source == "upstream" {
				var value string
				if err := pool.QueryRow(ctx, "SELECT config_encrypted FROM cloud_agent_connection WHERE provider = 'synthetic-provider'").Scan(&value); err != nil || value != "synthetic-preserved-value" {
					t.Fatal("upstream connection was not preserved", err)
				}
			}
			if source == "fresh" {
				// No rows depend on the Mattermost origin or cloud connection yet.
				// The real down files must address the full identities separately.
				apply(combined, "down")
				if len(readHistory()) != 0 {
					t.Fatal("rollback left colliding identities recorded")
				}
				apply(combined, "down")
				apply(combined, "up")
				if len(readHistory()) != len(combined) {
					t.Fatal("reapply did not restore every full identity")
				}
				assertIndexValidity(t, pool, schema, "idx_channel_reply_delivery_task_id", true)
				assertIndexValidity(t, pool, schema, "cloud_agent_connection_provider_index", true)
			}
		})
	}
}
