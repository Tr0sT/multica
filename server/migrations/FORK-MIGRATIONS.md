# Fork migration collisions

Migration identity is the complete filename stem, for example
`585_issue_origin_mattermost_chat`, not just `585`. The runner records that full
identity in `schema_migrations`, checks it before applying SQL, and rolls it back
by the same identity. Do not rename or renumber published migrations, rewrite
recorded versions, or combine their SQL to resolve a numeric-prefix collision.

## Explicit allowlist

`fork-prefix-allowlist.json` is the single reviewed list of permitted collisions.
It is read by tests only; it does not change runtime migration ordering,
readiness, SQL, history, or concurrent-index cleanup hooks.

Each entry has a numeric `prefix`, the **complete set** of exact `versions`
(without `.up.sql` / `.down.sql`), and a non-empty `reason`. For example:

```json
{
  "prefix": 585,
  "versions": [
    "585_issue_origin_mattermost_chat",
    "585_issue_workflow_owner"
  ],
  "reason": "Independently published migrations; preserve persisted identities."
}
```

An entry does not permit every migration bearing that number. A third version,
renamed/missing member, stale entry, duplicate allowance, duplicate member,
wildcard, or unexplained collision fails CI. All named versions must exist in
both directions. Historical upstream duplicates through 128 keep the existing
upstream policy; other unlisted prefixes remain unique.

## Reviewing a future collision

1. Keep both published identities and their SQL unchanged. Inspect the actual
   operations, dependencies, forward/reverse ordering, and migration hooks.
   Distinct names alone do not establish that their DDL is compatible.
2. Add one entry for a new prefix, or add an exact version to the existing group
   when a third migration collides. Explain why they coexist in `reason`.
3. Add or extend PostgreSQL-backed upgrade coverage in the same PR. Exercise
   fresh schema, already-applied fork and already-applied upstream paths,
   repeated apply without replay, retained data/history/indexes, and safe
   rollback/reapply. Do not mark an upgrade test successful when no database ran.
4. Run the migration lint/identity tests and relevant runner tests, then the full
   applicable CI. The allowlist is not a waiver for a failing SQL migration.

Current collision upgrade coverage lives in
`server/cmd/migrate/fork_prefix_upgrade_test.go`. It exercises the six colliding
SQL files with the actual migration runner in isolated schemas; full clean
installation remains covered by CI's normal migration step. It is not a full
production backup/restore test. Rollback of Mattermost origins must continue to
fail while rows using that origin remain; this policy does not remove that guard.

From the repository root, with the checkout's managed test environment active:

```sh
(cd server && go test -race ./internal/migrations -count=1)
(cd server && go test -race ./cmd/migrate -run '^TestForkPrefixCollisions' -count=1 -v)
```
