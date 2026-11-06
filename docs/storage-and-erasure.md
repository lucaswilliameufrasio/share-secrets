# Persistence and erasure: design discussion

Date: 2026-10-29. Status: backup policy accepted; optional SQLite implemented,
performance and production deployment not validated.

## Agreed direction and unresolved gate

Run on one VPS now. SQLite with `modernc.org/sqlite` is the optional first
backend; timestamped SQL migrations are generated/managed via the Goose
CLI. PostgreSQL must remain a future operator-selectable
open-source option (see `roadmap.md`).

The owner accepted excluding active payloads from backups/snapshots and losing
active links after disk loss. This selects Option A below, not an external key
system or a guarantee of physical erasure from arbitrary media. Enforcement
in host backup/snapshot tooling is an operator prerequisite, not implemented
or remotely verified here. Expiry needs the same treatment as consumption.
Logical deletion, physical sanitization and cryptographic erasure are different
guarantees; cryptographic erasure leaves encrypted bytes behind and therefore
is not literal removal of all ciphertext.

## Current guarantees and limits

ACK validates the active reservation and the proof hash, marks the link used
and clears payload/verification references in the in-memory store. Expiry also
clears these references. Terminal tracking metadata is retained separately in
the record for bounded status lookup. Optional SQLite stores payloads durably
until consumption or expiry; the app does not implement payload backups and
external host backup/snapshot policy has not been inspected.
Go strings/heap copies are not securely zeroized. No guarantee covers swap,
core dumps, host snapshots, storage hardware or a recipient's copies. A person
who already received/decrypted a secret cannot be made to forget it.

The browser key stays in the share URL fragment, not server storage. That key
may survive with the sender/recipient, so server deletion cannot rely on its
destruction. Network-only PWA behavior prevents intentional offline caching,
not arbitrary browser/OS memory retention or user copying.

## SQLite controls and operational boundaries

- Local persistent directory, no network filesystem, shared by overlapping
  blue/green slots. Account for DB, WAL and SHM files and restrictive ownership.
- Atomic claim/ACK/release and consumption/expiry transactions. A restart or
  old deploy slot must never resurrect a consumed link.
- Short transactions, bounded writers, WAL, an explicit busy timeout and
  durability policy. Measure these choices, do not disable durability to win
  benchmarks.
- Enable and verify `PRAGMA secure_delete=ON` on every relevant connection,
  including tools that delete payloads. Avoid auxiliary structures that retain
  payload copies. This reduces ordinary SQLite page remnants, not all copies.
- Treat WAL checkpoint/truncation as explicit cleanup with observed outcomes.
  Long-lived readers can block it; monitor/retry with bounded work. A successful
  ACK commit alone does not prove a completed physical purge. Define the
  response/cleanup contract before coding; retrying a committed consumption
  after a cleanup error must not permit a second disclosure.
- Do not run VACUUM per ACK or regard it as SSD sanitization. Compaction and
  truncation cannot erase copy-on-write snapshots, existing backups, SSD
  wear-leveling copies, swap or dumps. Whole-disk encryption does not provide
  per-secret erasure while its key remains available.
- SQLite can create statement journals outside the database directory, including
  when using savepoints. Container configuration uses `SQLITE_TMPDIR=/tmp`
  with a 16 MiB noexec tmpfs; the operator must account for host swap, memory
  dumps and snapshots too. A writable tmpfs avoids writing these temporary
  files to an ordinary container filesystem; it is not a promise of secure
  physical deletion. Local non-container runs must choose and audit their own
  SQLite temporary-directory and swap policy.
- Retain only status/timestamps/counts and the scoped tracking-token hash after
  terminal transition, not ciphertext, IV, proof or reservation material.

## Backup and cryptographic-erasure options to decide

**Option A (accepted): exclude active payloads from backups/snapshots.** Separate payload
storage from backed-up metadata and enforce exclusions across the operator's
backup, VM snapshot and crash-dump tooling. This sacrifices payload recovery
after disk loss and still cannot promise physical SSD/heap sanitization.

**Option B (not selected): additional server-side per-secret encryption.** Wrap the existing
browser ciphertext with an independent per-secret key. Destroy that key on
consumption/expiry using a key system whose deletion and backup behavior are
defined and verified. Keys must not be recoverable from the same backups,
replicas, snapshots, logs or caches as the encrypted payload. Merely putting
the key in another SQLite table or encrypting it with a retained master key
does not solve recovery from old snapshots. Volatile keys lose all active
links on restart; a durable external key system adds operational complexity.

Option B can target unrecoverability of residual wrapped data, not physical
removal of every byte or revocation of already-disclosed copies. It also needs
a crash-safe protocol across DB and key custody: pending deletion, fail-closed
reads, idempotent recovery, reconciliation and observable deletion failures.
No such protocol or external dependency has been selected or authorized.

## Required evidence before a stronger claim

- Agree threat boundary, backup policy, cleanup deadline, ACK semantics, failure
  behavior and what an operator may claim to users.
- Real DB tests for duplicate ACK, concurrent claims, lease expiry, restarts,
  mixed old/new slots and crashes at each consumption/deletion boundary.
- Synthetic unique-marker checks of DB/WAL and application-owned artifacts,
  with long readers and forced failures. Passing marker scans is supporting
  evidence, not proof of arbitrary hardware or external-copy erasure.
- If using key destruction, exercise loss/retry/crash recovery and an isolated
  restoration of old backups: consumed/expired data must remain unrecoverable.
  Never run a live restore or retrieve real secret values for this test.
- Include cleanup latency, backlog and failure counts in load tests; never
  omit deletion work from the performance result.
