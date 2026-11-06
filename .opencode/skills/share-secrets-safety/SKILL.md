---
name: share-secrets-safety
description: Review Share Secrets storage, one-time consumption, erasure, migrations, blue-green operation and performance claims. Use before changing its persistence, lifecycle, backup policy or capacity.
---

# Share Secrets safety review

Read the repository `AGENTS.md`, `docs/security-usage-design.md`,
`docs/storage-and-erasure.md`, `docs/roadmap.md`, `docs/performance.md` and
`DEPLOY.md`. Follow current authorization; a roadmap is not implementation
approval. This skill is project-local and does not alter global skills.

## Establish reality

- Inspect `main.go`, `sqlite_store.go`, `main_test.go`, `web/app.js`,
  `compose.yaml` and deployment code before making claims. The default is
  volatile; SQLite and a blue/green volume guard exist but have not been
  verified on the VPS. PostgreSQL support does not exist.
- Protect all tokens and secret-bearing URLs. Use synthetic local data only,
  no production environment files, live restores or implicit VPS operations.

## Storage/lifecycle gate

- Follow the accepted no-payload-backups/snapshots policy and accepted loss of
  links after disk loss. Verify operator exclusions before persistence; finalize
  cleanup/ACK failure behavior. Distinguish logical deletion, physical
  sanitization and cryptographic erasure. No external key system is selected.
- Preserve single-winner reservations, valid-proof ACK, safe release, expiry,
  scoped status-only tokens and no resurrection after crash/restart/rollback.
- Keep plaintext/fragment keys/full links out of server persistence and browser
  history storage. Retain only documented terminal metadata after consumption.
- For SQLite, use modernc only once selected; timestamped SQL migrations must
  be created via a pinned Goose CLI. Do not invent installed tool versions or
  hand-name migrations. Verify real DB connections and schema version.
- Coordinate one local database directory across blue/green slots. Require
  compatible migrations and explicit ownership of their execution. Never run
  migrations against live/shared infrastructure without authorization.
- For proposed erasure controls inspect DB/WAL/SHM, long readers, temporary
  files, copies, logs, snapshots/backups, dumps/swap and key custody. Test failures
  and crash recovery. DELETE/secure_delete/checkpoint/VACUUM are not proof of
  erasure from all media or historical backups.
- If per-secret key destruction is selected, verify that old backups cannot
  recover the key and restored payload. Treat uncertain key deletion as pending,
  with fail-closed reads and explicit recovery; do not claim cross-system atomicity.
- For future PostgreSQL, re-evaluate MVCC, WAL/PITR, replicas and backups; keep
  operator selection and lifecycle contracts explicit rather than copying PRAGMAs.

## Evidence gate

- Use the repository's `Makefile` quality gate and Playwright journeys, plus
  real-file/real-process DB integration tests when a backend exists.
- Verify invalid key does not consume; valid open consumes once; history stores
  no key/link/plaintext; tracking updates in result view; PWA fails offline.
- Missing Chromium libraries are an environment blocker, not a passed test.
  Package installation requires approval. Never weaken assertions to get green.
- Use performance-optimization and performance-benchmarking-and-load-testing
  before tuning or claiming capacity. `make bench` includes legacy fresh-store
  benchmarks and new explicitly named shared-store benchmarks. Neither proves
  HTTP capacity; use `make bench-http` and read `docs/performance.md`. Run
  sequentially, never against a shared/live VPS or alongside the quality gate.
- Include deletion work, durability, resource caps, rejection rates and p99 in
  capacity tests. Separate configured limits from measured healthy capacity.

Record outcomes and limitations in the existing design/performance documents.
Label implemented, verified, proposed and blocked work separately. Do not
promise deletion of recipient copies or physical sanitization outside the
application's verified control.
