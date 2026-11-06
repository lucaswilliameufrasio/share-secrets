# Storage implementation decisions

Date: 2026-10-29. Status: implementation authorized; verification in progress.

## Journeys and ownership

Creators submit browser-encrypted payloads and receive a status-only token;
recipients reserve, decrypt and acknowledge once. Operators select local
storage, explicitly migrate schema, and exclude payload files from backups and
snapshots. Tracking retains bounded terminal metadata, not payloads. HTTP and
browser contracts remain unchanged. No remote operation is authorized here.

## Slices and verification

1. Optimize memory store: indexed min-heaps for active deadlines and terminal
   eviction, direct-ID expiry checks, bounded maintenance batches, independent
   read locking for status. Retain one short write lock for atomic transitions;
   no actor queue, sharding or CAS state machine without measured need.
2. Prove invariants and repeat the retained/full-history campaign before adding
   persistence. Hypothesis: removing full-map scans eliminates history-dependent
   CPU amplification without weakening cleanup or single consumption.
3. Add a narrow lifecycle storage contract and SQLite/modernc adapter with real
   file integration tests, CLI-generated timestamped Goose SQL migrations and
   explicit operator migration commands. PostgreSQL stays on the roadmap.
4. Exercise restarts, independent connections/processes, expiry, failed proofs,
   crash/lock recovery, bounded capacity and removal from live DB/WAL artifacts.
5. Repeat functional/security/E2E gates and benchmark SQLite separately; do not
   attribute memory-store speedups to a different backend.

## Persistent deletion contract

Memory-store experiment executed: `benchmark-results/20261029T214150-dd5024/`
(local-only, ignored). Under the same 0.5 CPU/128 MiB, payload mix and retained
history sequence, median run p99 at 50 cycles/s fell from 2,037 ms to 5.60 ms;
1,500/1,500 cycles completed in each campaign. At 200 cycles/s, 6,000/6,000
completed across three optimized runs, p99 median 5.13 ms, versus 1,601/6,000
and 4,087 ms before. Short windows are not a VPS or SQLite capacity claim.
Burst case still passed only 1/3 delivery screens; do not claim overload has
been solved. Shared-store 9,500-history lifecycle median fell from 2,047,689
to 24,499 ns/op (parallel amortized, adaptive benchmarks). The baseline and
candidate generator digest matched; only the server build changed.

Payload and claim/proof material is removed in the transaction that consumes or
expires it. Consumption remains final even if a later checkpoint cannot finish;
never restore payload or disclose again to compensate for cleanup failure.
Track/checkpoint pending WAL cleanup and retry in bounded maintenance. A busy
reader can postpone truncation; report pending cleanup, not physical erasure.
Use secure_delete, WAL, FULL synchronous durability, bounded busy timeout and a
small connection budget. Validate schema at startup, do not auto-migrate.
Production payload persistence requires an explicit operator acknowledgement of
the no-backup/no-snapshot policy. This records responsibility, not verification
of the VPS/provider's exclusions. No promise covers hardware copies or RAM.

Blue/green slots must use the same local database directory and compatible
schema; old in-memory versions cannot participate in a durable cutover. Migrate
once before starting candidates, never per replica. Do not silently migrate
volatile links or provide a backup/restore command for active payloads.

## SQLite implementation choices

### Group-commit experiment (2026-11-06)

Observed bottleneck: single SQLite writer, FULL synchronous commits on the
test's rotational ext4; 5 cycles/s failed the provisional latency threshold.
Hypothesis: combining independent concurrent operations into one SQLite
transaction can amortize disk synchronization without acknowledging early.

Candidate: one bounded per-process writer mailbox, at most 64 queued and 32
operations per transaction, with up to 2ms collection time. Each callback has
its own savepoint; domain failures roll back only that callback. Database or
commit failure fails the whole batch; successful callbacks receive success only
after the shared FULL-synchronous commit. Excess is rejected with existing
storage-unavailable 503. Queued operations are volatile, not durably accepted;
a process crash before commit may lose them and clients can receive ambiguous
outcomes if a response is lost after commit. One-time claim/ACK rules still
apply, and a second process still arbitrates through SQLite's writer lock.

The batch worker must not outlive its store: close stops admission, drains
accepted work and closes the database; process termination may interrupt an
uncommitted batch and must not be described as durable acceptance. An operation
whose queue deadline expired must never execute; once execution begins the
caller must wait until callback and commit have resolved, avoiding dangling
closures or falsely reporting a cancellation of a committed mutation. Keep
pending-checkpoint generation and secure deletion unchanged. A failed
experiment must be reverted rather than weaken correctness or increase the
erasure deadline. Validate real SQLite concurrency, isolated callback errors,
commit/lock failure, queue overload, cancellation, close and crash semantics;
compare repeated identical workloads and monitor errors/p99, not just RPS.

Candidate implemented and local targeted regression checks passed, including
savepoint isolation, whole-batch rollback on commit failure, expired-queue
skipping, full-queue rejection, bounded drain, two concurrent application
processes and a real SIGTERM during an in-flight SQLite write (3 repetitions).
The app now stops admitting HTTP work on SIGTERM/SIGINT and drains for at most
8 seconds, shorter than Docker's usual 10-second default. A forced stop or
expired deadline can still interrupt volatile queued requests, and loss of a
response after commit leaves an ambiguous client outcome; no durable queued-job
guarantee is claimed. `storageQueueDepth`, `storageQueueCapacity`,
`storageQueueRejectedTotal` and batch-stage/error-category counters are
low-cardinality protected metrics. Queue is per instance, not distributed.

The 10/25-cycles/s experiments initially saw `SQLITE_IOERR_GETTEMPPATH` (6410)
because the read-only benchmark container had no writable SQLite temp path.
Container configuration and runner now use a bounded 16-MiB tmpfs. This
configuration change makes the candidate's test environment different from
the initial failed group-commit run; do not attribute all change to batching.
With tmpfs, fresh-store 10/s and 25/s completed every offered cycle across
three short repetitions, but median run p99 was 1.13s and 1.96s respectively,
so both rates still failed the provisional latency criterion in at least two
of three runs. At 5/s, all 150 cycles completed and 3/3 runs passed (median
p99 894ms). This is not a VPS or full-history SLO. The complete SQLite campaign
finished: 25/s with 9,510 records completed 750/750 across three windows but
median p99 was 1.87s; 10/s soak completed 3,600/3,600 but median p99 was 1.44s.
Both missed the provisional <1s threshold. See
`benchmarks/2026-10-30-sqlite-baseline.md` for error/baseline distinctions.

- Upstream versions checked on 2026-10-29: modernc SQLite v1.61.0 (module proxy
  and package docs) and Goose v3.29.0 (upstream release). Pin modernc's required
  libc v1.79.0 rather than independently updating its fragile ABI dependency.
- Keep `memory` as the backwards-compatible default. Select SQLite explicitly
  with a local file path and no-backup acknowledgement; never silently fall
  back to memory if a durable store cannot open, migrate or validate.
- Split status metadata and active encrypted payload into separate tables in
  one transactionally consistent database. Exclude the **entire** database
  directory (DB/WAL/SHM) from backups/snapshots; no metadata backup/export is
  implemented by this choice.
- Index active expiry and terminal age. Maintenance handles bounded batches;
  claim/status/ACK check the addressed record's expiry immediately. SQLite
  transactions, not an application-global mutex, arbitrate concurrent writers
  across processes. Keep one connection per instance and bounded operation
  deadlines, avoiding unconstrained connection pools.
- Persist lifecycle counters alongside transitions so overlapping slots do not
  lose counts. Rate-limit/rejection counters and uptime remain process-local.
- Record a purge generation in the same transaction as payload removal; a
  successful TRUNCATE checkpoint acknowledges only the generation observed
  before it. Newer removals stay pending. A busy checkpoint is observable via
  `cleanupPending`, never reported as completed physical sanitization.
- Goose CLI must use `-env none` and explicit SQLite selection: its default
  behavior loads `.env`, which must not happen in local test/migration tooling.
- No replica starts schema migrations. Startup requires the exact supported
  Goose schema version; a missing/newer/older schema is a startup error.

## Implementation state and known gaps

The optional SQLite backend and explicit Goose migration are implemented for
local use; memory is still the default. Tests have verified concurrent claims
over two connections, ACK/proof behavior, retained status after reopen, TTL,
capacity, pending WAL checkpoint under a long reader, fail-closed HTTP 503 on a
writer lock, and rollback after abrupt child-process termination. A separate
real-process integration test starts two local application processes on one
SQLite file and verifies only one concurrent claim wins and ACK across slots;
it passed three successive runs. Four real Playwright journeys passed against
SQLite and four against memory in a rootless test container. The E2E SQLite file
was temporary, not evidence of durable physical-disk behavior. These tests do
not verify VPS filesystem, real snapshots, SSD sanitization or crash timing at
every possible boundary.

`compose.sqlite.yaml` is an opt-in local bind-mount override. The blue/green
CLI has a same-directory guard and explicit volatile-cutover acknowledgement;
it does not run migrations, verify backups or provide a deploy lock. Its
end-to-end operation on the VPS is unverified; do not deploy SQLite yet. The
initial SQLite benchmark failed the chosen delivery/latency criteria at 10
cycles/s on local rotating ext4 (`benchmarks/2026-10-30-sqlite-baseline.md`).
The pre-batching full-history retry reached 9,510 tracked entries and failed the 25 cycles/s
diagnostic threshold in two complete windows; a third window was truncated by
the test harness deadline. A separate corrected-deadline repetition also failed
(53/250 cycles, p99 9.65s). See `benchmarks/2026-10-30-sqlite-baseline.md` for
the exact evidence and comparability limits.
`make all`, `make lint`, `make security`, `make migrations-check`, both Compose
configuration checks and npm audit passed on 2026-11-06 after the batch and
shutdown changes. `make e2e` and `make e2e-sqlite` each passed four journeys
in an isolated rootless test container with a real Goose migration; an ARM64
CGO-free binary built successfully, but was not executed on ARM64. The format
gate now checks Go files using the selected mise toolchain and was tested to
fail when its formatter cannot execute.

Acceptance is **partial for intense use**: memory optimization and SQLite
lifecycle behavior have local evidence, but the SQLite workload did not pass
the diagnostic criteria at 10 cycles/s on the test disk. No production SLO,
VPS capacity, physical erase guarantee, provider backup exclusion, deployment
lock or live blue/green execution has been verified. A lower-rate SQLite
campaign has now checked bounded short soak and full-history behavior, with
the latency failure reported above. Never represent
this as a production-ready intense-use deployment or claim the operator's
backup exclusion has been verified. The pre-batching short local fresh-store
SQLite staircase passed at 2 cycles/s and failed latency at 5 cycles/s; the
current candidate passed at 5 cycles/s in short runs. Neither is a full-history
limit or a prediction of VPS behavior; see `benchmarks/2026-10-30-sqlite-baseline.md`.
