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

// The two published 665 migrations are identified by their complete names.
// Exercise the real SQL and production runner with isolated PostgreSQL schemas,
// including hosts which previously applied only one side.
func TestForkRuntimeRateLimitAndWeixinPrefixCollisionUpgrade(t *testing.T) {
	weixin := []string{"665_weixin_state"}
	limits := []string{"665_runtime_rate_limit"}
	combined := []string{limits[0], weixin[0]}
	sort.Strings(combined)

	for _, source := range []string{"fresh", "weixin", "limits"} {
		t.Run(source, func(t *testing.T) {
			admin := openTestPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			schema := fmt.Sprintf("fork_runtime_limits_%d_%d", time.Now().UnixNano(), rand.Uint32())
			ident := pgx.Identifier{schema}.Sanitize()
			if _, err := admin.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
				defer stop()
				if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+ident+" CASCADE"); err != nil {
					t.Errorf("cleanup isolated schema: %v", err)
				}
			})
			pool := openTestPoolWithSearchPath(t, schema)
			if _, err := pool.Exec(ctx, `
				CREATE TABLE schema_migrations (
					version TEXT PRIMARY KEY,
					applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
				);
			`); err != nil {
				t.Fatal(err)
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
					t.Fatalf("apply %s migrations on %s schema: %v", direction, source, err)
				}
			}
			history := func() map[string]time.Time {
				t.Helper()
				rows, err := pool.Query(ctx, "SELECT version, applied_at FROM schema_migrations")
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				got := map[string]time.Time{}
				for rows.Next() {
					var version string
					var applied time.Time
					if err := rows.Scan(&version, &applied); err != nil {
						t.Fatal(err)
					}
					got[version] = applied
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				return got
			}
			const installationID = "00000000-0000-4000-8000-000000000101"
			const runtimeID = "00000000-0000-4000-8000-000000000102"
			const workspaceID = "00000000-0000-4000-8000-000000000103"

			switch source {
			case "weixin":
				apply(weixin, "up")
				if _, err := pool.Exec(ctx, `
					INSERT INTO weixin_account_state (installation_id, bot_id, user_id)
					VALUES ($1, 'synthetic-bot', 'synthetic-user')
				`, installationID); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `
					INSERT INTO weixin_login_session (id, workspace_id, agent_id, initiator_user_id, state_encrypted, expires_at)
					VALUES ('00000000-0000-4000-8000-000000000104', $1, $2, $1, 'synthetic-ciphertext', now() + interval '1 day')
				`, workspaceID, installationID); err != nil {
					t.Fatal(err)
				}
			case "limits":
				apply(limits, "up")
				if _, err := pool.Exec(ctx, `
					INSERT INTO runtime_rate_limit (runtime_id, workspace_id, snapshot, observed_at)
					VALUES ($1, $2, '{"source":"synthetic-limit"}'::jsonb, now())
				`, runtimeID, workspaceID); err != nil {
					t.Fatal(err)
				}
			}

			before := history()
			apply(combined, "up")
			after := history()
			if len(after) != len(combined) {
				t.Fatalf("recorded %d migrations, want both full version identities", len(after))
			}
			for _, version := range combined {
				if _, ok := after[version]; !ok {
					t.Fatalf("missing full migration version %q", version)
				}
			}
			for version, applied := range before {
				if !after[version].Equal(applied) {
					t.Fatalf("migration history changed for already applied %q", version)
				}
			}
			apply(combined, "up")
			for version, applied := range history() {
				if !after[version].Equal(applied) {
					t.Fatalf("repeated apply changed history for %q", version)
				}
			}
			for _, table := range []string{"runtime_rate_limit", "weixin_login_session", "weixin_account_state"} {
				var exists bool
				if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", schema+"."+table).Scan(&exists); err != nil || !exists {
					t.Fatalf("independent table %s missing after upgrade: %v", table, err)
				}
			}
			if source == "weixin" {
				var bot, user, state string
				if err := pool.QueryRow(ctx, "SELECT bot_id, user_id FROM weixin_account_state WHERE installation_id = $1", installationID).Scan(&bot, &user); err != nil || bot != "synthetic-bot" || user != "synthetic-user" {
					t.Fatalf("existing Weixin account data lost: %q %q, %v", bot, user, err)
				}
				if err := pool.QueryRow(ctx, "SELECT state_encrypted::text FROM weixin_login_session WHERE id = '00000000-0000-4000-8000-000000000104'").Scan(&state); err != nil || state == "" {
					t.Fatalf("existing Weixin login state lost: %v", err)
				}
			}
			if source == "limits" {
				var limitSource string
				if err := pool.QueryRow(ctx, "SELECT snapshot->>'source' FROM runtime_rate_limit WHERE runtime_id = $1", runtimeID).Scan(&limitSource); err != nil || limitSource != "synthetic-limit" {
					t.Fatalf("existing rate-limit snapshot lost: %q %v", limitSource, err)
				}
			}
			if source == "fresh" {
				apply(combined, "down")
				if len(history()) != 0 {
					t.Fatal("rollback retained a colliding migration identity")
				}
				for _, table := range []string{"runtime_rate_limit", "weixin_login_session", "weixin_account_state"} {
					var exists bool
					if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", schema+"."+table).Scan(&exists); err != nil || exists {
						t.Fatalf("rollback retained table %s: %v", table, err)
					}
				}
				apply(combined, "down")
				apply(combined, "up")
				if len(history()) != len(combined) {
					t.Fatal("reapply did not restore both full migration identities")
				}
			}
		})
	}
}
