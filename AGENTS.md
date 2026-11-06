# Project rules

## Scope and decisions

- Read `docs/security-usage-design.md`, `docs/storage-and-erasure.md`,
  `docs/roadmap.md` and `docs/performance.md` before changing storage, lifecycle,
  deployment or capacity. Distinguish current implementation from proposals.
- The default store is volatile and process-local. Optional SQLite with
  `modernc.org/sqlite` is implemented for local testing, and the blue/green
  configuration has not been validated on a VPS. Production readiness remains
  unverified. The deployment
  direction is one VPS.
- Future PostgreSQL support must be an operator-selectable open-source backend,
  not a mandatory hosted service. Keep lifecycle contracts backend-independent;
  do not build an unused adapter or generic repository framework prematurely.
- If SQLite is implemented, create timestamped SQL migrations with the Goose
  CLI, pin the CLI version and document commands. Do not hand-name migrations,
  run schema changes automatically in every app process, or run migrations on
  a shared/live database without explicit authorization.

## Security and data lifecycle

- Read `.opencode/skills/share-secrets-safety/SKILL.md` for lifecycle reviews.
- Preserve atomic claim/ACK/release, proof verification, expiry and scoped
  tracking-token authorization across restarts and overlapping deploy slots.
- Never persist or log plaintext, full share URLs, fragment keys, claim tokens
  or resolved operator credentials. Server storage keeps only hashes of
  authorization tokens; the browser may persist the scoped status-only token
  for local tracking, never the decryption key or share URL.
- Consumption and expiry must remove payload and verification material from
  live application state; keep only documented terminal tracking metadata.
- Do not claim that dropping references, SQL DELETE, VACUUM, checkpoints,
  secure_delete or disk encryption prove erasure from all historical copies.
  Treat database/WAL, backups, snapshots, replicas, dumps, swap, crash dumps,
  browser state and storage-controller copies as separate boundaries.
- The owner accepted no backups/snapshots of active payloads and loss of links
  after disk loss. This does not authorize an external key system or promise
  physical sanitization. Verify operator exclusions and define cleanup/ACK
  failure semantics before durable payload storage. Never label policy enforced
  based on documentation alone.
- Keep PWA network-only, without offline caches of assets or API responses.
- Never deploy, prune operator resources or alter remote hosts implicitly.

## Verification and performance

- Use `make all`, `make lint`, `make security`, `make e2e`, `make e2e-sqlite`,
  `make migrations-check`,
  `npm audit --audit-level=moderate` and `docker compose config --quiet`.
  Run JS syntax checks and `git diff --check` as appropriate. Report blocked
  checks honestly; Chromium needs system dependencies beyond its download.
- Use local synthetic data and isolated resources, never production secrets.
- Use the performance-optimization and performance-benchmarking-and-load-testing
  skills before performance claims or tuning. Preserve comparable repeated runs.
- `make bench` is a microbenchmark, not a service-capacity test. Legacy
  `BenchmarkSecretStoreConcurrentConsume` uses independent stores; the new
  `BenchmarkSharedStore*` cases explicitly measure shared contention.
- `make bench-http` owns local test resources, verifies rootless CPU/RAM limits
  and retains results in ignored `benchmark-results/`. Do not run competing
  tests/builds/benchmarks during a measurement campaign. Operator no-backup
  acceptance is not authorization to modify real backup or VPS configuration.
- Configured capacity limits are not measured sustainable throughput. Do not
  describe the app as ready for heavy load without the campaign in
  `docs/performance.md`, including the erasure cost of the selected backend.
