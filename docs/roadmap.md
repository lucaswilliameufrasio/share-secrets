# Roadmap

## Current state

The default store is volatile and process-local. An optional SQLite backend and
Goose migration now exist for local testing; blue/green integration, full
durability/performance validation and production release remain unfinished.
No PostgreSQL backend or distributed admission control exists. See
`storage-implementation.md` for current evidence. This roadmap does not
authorize deployment.

## Next: persistence on one VPS

Deployment direction confirmed: one VPS for now. Proposed backend: SQLite via
`modernc.org/sqlite`, preserving a CGO-free build. Before implementation:

1. Implement the accepted no-payload-backups policy and verify operator
   exclusions; finalize cleanup/ACK failure semantics (`storage-and-erasure.md`).
2. Establish a shared-store and HTTP performance baseline (`performance.md`).
   Baseline executed: the full-history diagnostic bracket is 25 passing to 50
   failing cycles/s at 0.5 CPU/128 MiB. Preserve this workload for comparison;
   explicitly address indexed expiry/terminal cleanup and bounded eviction in
   the storage design instead of carrying repeated full-store scans into SQL.
3. Design atomic create/claim/ACK/release/expiry and restart/crash recovery;
   separate payload storage from retained terminal tracking metadata.
4. Create timestamped SQL migrations through a pinned Goose CLI. Document the
   create/status/up/validation commands once the tool is actually installed.
5. Use a local persistent directory for database, WAL and SHM; design bounded
   writes, busy handling, durability and compatible blue/green schema changes.
6. Test against real SQLite files, independent connections/processes, crashes,
   expiry, disk/lock failures, erasure behavior and deploy overlap. Measure the
   complete lifecycle including cleanup before selecting capacity defaults.

Do not silently carry today's 500/700 caps or 0.5 CPU/128 MiB budget forward as
validated sizing for durable storage. Persistence does not distribute metrics
or rate limits automatically.

## Future: operator-selectable PostgreSQL

Requested roadmap direction: support open-source PostgreSQL as an alternative
chosen by the operator, alongside SQLite; do not require a managed provider.
It is not implemented and no delivery date is committed.

- Preserve backend-independent lifecycle semantics, error/status contracts and
  an equivalent integration/concurrency contract suite.
- Keep dialect-specific SQL/migrations explicit, versioned and tested. Use the
  Goose CLI migration workflow rather than assuming SQLite SQL is portable.
- Plan backend selection, configuration, connection budgeting, safe data
  migration/cutover and rollback; no automatic live backend switch.
- Reassess shared rate limiting, telemetry and coordination before multiple
  hosts. PostgreSQL alone does not make the whole service distributed-safe.
- Reassess erasure: MVCC dead tuples, WAL, replicas, base backups, PITR archives,
  snapshots and restored copies. DELETE/VACUUM do not erase historical copies.
- Compare both backends under equivalent workloads including secure cleanup;
  choose based on operator needs and evidence, not a claimed universal winner.
