# Fork migration rules

Read [FORK-MIGRATIONS.md](FORK-MIGRATIONS.md) before resolving a fork/upstream
numeric-prefix collision. Preserve published full migration names and SQL.

Add reviewed collisions to `fork-prefix-allowlist.json` as exact version sets
with a reason and PostgreSQL-backed upgrade coverage. Never permit a whole
numeric prefix, use wildcards, automatically approve a new collision, or disable
the uniqueness/direction tests. A third colliding version needs explicit review
and an update to that same entry.
